package checks

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// HealthTransition is fired when a VIP crosses the rise or fall threshold.
// Ctx carries the trace context of the check that caused the transition so
// that downstream operations (e.g. BGP announce/withdraw) appear as child spans.
type HealthTransition struct {
	Ctx         context.Context // trace context; may be nil when called synthetically
	VIPName     string
	CheckName   string // which of the VIP's checks crossed the threshold
	Healthy     bool
	Reason      string
	CheckResult Result
}

type Scheduler struct {
	vipName       string
	checkName     string
	checker       Checker
	interval      time.Duration
	timeout       time.Duration
	rise          int
	fall          int
	onTransition  func(HealthTransition)
	onCheckResult func(vipName, checkName string, result Result)
	tracer        trace.Tracer

	// runMu serializes checks (ticker and ad-hoc triggers) so that state
	// updates and their callbacks are delivered in order.
	runMu sync.Mutex

	mu              sync.Mutex
	healthy         bool
	consecutiveOK   int
	consecutiveFail int
	lastResult      Result
}

type SchedulerConfig struct {
	VIPName string
	// CheckName identifies the check within the VIP; defaults to the checker type.
	CheckName     string
	Checker       Checker
	Interval      time.Duration
	Timeout       time.Duration
	Rise          int
	Fall          int
	OnTransition  func(HealthTransition)
	OnCheckResult func(vipName, checkName string, result Result)
}

func NewScheduler(cfg SchedulerConfig) *Scheduler {
	name := cfg.CheckName
	if name == "" && cfg.Checker != nil {
		name = cfg.Checker.Type()
	}
	return &Scheduler{
		vipName:       cfg.VIPName,
		checkName:     name,
		checker:       cfg.Checker,
		interval:      cfg.Interval,
		timeout:       cfg.Timeout,
		rise:          cfg.Rise,
		fall:          cfg.Fall,
		onTransition:  cfg.OnTransition,
		onCheckResult: cfg.OnCheckResult,
		healthy:       false,
		tracer:        otel.Tracer("pathosd/checks"),
	}
}

func (s *Scheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	s.runCheck(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runCheck(ctx)
		}
	}
}

// TriggerCheck runs an ad-hoc check. The check is detached from ctx
// cancellation (only the configured timeout applies), so an aborted caller
// cannot produce a spurious failure that counts toward fall.
func (s *Scheduler) TriggerCheck(ctx context.Context) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	return s.runCheck(context.WithoutCancel(ctx)), nil
}

func (s *Scheduler) SetCallbacks(onTransition func(HealthTransition), onCheckResult func(vipName, checkName string, result Result)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onTransition = onTransition
	s.onCheckResult = onCheckResult
}

func (s *Scheduler) VIPName() string { return s.vipName }

// CheckName returns the name of the check this scheduler runs.
func (s *Scheduler) CheckName() string { return s.checkName }

func (s *Scheduler) IsHealthy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.healthy
}

func (s *Scheduler) LastResult() Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastResult
}

func (s *Scheduler) ConsecutiveOK() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.consecutiveOK
}

func (s *Scheduler) ConsecutiveFail() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.consecutiveFail
}

func (s *Scheduler) runCheck(parentCtx context.Context) Result {
	s.runMu.Lock()
	defer s.runMu.Unlock()

	spanCtx, span := s.tracer.Start(parentCtx, "health_check",
		trace.WithAttributes(
			attribute.String("vip.name", s.vipName),
			attribute.String("check.name", s.checkName),
			attribute.String("check.type", s.checker.Type()),
		),
		trace.WithSpanKind(trace.SpanKindInternal),
	)
	defer span.End()

	ctx, cancel := context.WithTimeout(spanCtx, s.timeout)
	defer cancel()

	result := s.checker.Check(ctx)

	span.SetAttributes(
		attribute.Bool("check.success", result.Success),
		attribute.Int64("check.duration_ms", result.Duration.Milliseconds()),
		attribute.String("check.detail", result.Detail),
		attribute.Bool("check.timed_out", result.TimedOut),
	)
	if !result.Success {
		span.SetStatus(codes.Error, result.Detail)
	}

	if result.Success {
		slog.Debug("check passed", "vip", s.vipName, "check", s.checkName, "duration", result.Duration, "detail", result.Detail)
	} else {
		slog.Debug("check failed", "vip", s.vipName, "check", s.checkName, "duration", result.Duration, "detail", result.Detail)
	}

	s.mu.Lock()

	onCheckResult := s.onCheckResult
	s.lastResult = result
	var transition *HealthTransition

	if result.Success {
		s.consecutiveFail = 0
		if s.consecutiveOK < s.rise {
			s.consecutiveOK++
		}
		if !s.healthy && s.consecutiveOK >= s.rise {
			s.healthy = true
			slog.Info("check healthy", "vip", s.vipName, "check", s.checkName, "consecutive_ok", s.consecutiveOK, "rise", s.rise)
			transition = &HealthTransition{
				Ctx:         spanCtx,
				VIPName:     s.vipName,
				CheckName:   s.checkName,
				Healthy:     true,
				Reason:      "rise threshold reached",
				CheckResult: result,
			}
		}
	} else {
		s.consecutiveOK = 0
		if s.consecutiveFail < s.fall {
			s.consecutiveFail++
		}
		if s.healthy && s.consecutiveFail >= s.fall {
			s.healthy = false
			slog.Warn("check unhealthy", "vip", s.vipName, "check", s.checkName, "consecutive_fail", s.consecutiveFail, "fall", s.fall, "detail", result.Detail)
			transition = &HealthTransition{
				Ctx:         spanCtx,
				VIPName:     s.vipName,
				CheckName:   s.checkName,
				Healthy:     false,
				Reason:      "fall threshold reached",
				CheckResult: result,
			}
		}
	}

	onTransition := s.onTransition
	s.mu.Unlock()

	// Fire callbacks outside the lock so that callbacks calling scheduler
	// methods (IsHealthy, LastResult, etc.) do not deadlock.
	if onCheckResult != nil {
		onCheckResult(s.vipName, s.checkName, result)
	}

	if transition != nil && onTransition != nil {
		onTransition(*transition)
	}

	return result
}
