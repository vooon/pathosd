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
	Healthy     bool
	Reason      string
	CheckResult Result
}

type Scheduler struct {
	vipName       string
	checker       Checker
	interval      time.Duration
	timeout       time.Duration
	rise          int
	fall          int
	onTransition  func(HealthTransition)
	onCheckResult func(vipName string, result Result)
	tracer        trace.Tracer

	mu              sync.Mutex
	healthy         bool
	consecutiveOK   int
	consecutiveFail int
	lastResult      Result
}

type SchedulerConfig struct {
	VIPName       string
	Checker       Checker
	Interval      time.Duration
	Timeout       time.Duration
	Rise          int
	Fall          int
	OnTransition  func(HealthTransition)
	OnCheckResult func(vipName string, result Result)
}

func NewScheduler(cfg SchedulerConfig) *Scheduler {
	return &Scheduler{
		vipName:       cfg.VIPName,
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

func (s *Scheduler) TriggerCheck(ctx context.Context) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	return s.runCheck(ctx), nil
}

func (s *Scheduler) SetCallbacks(onTransition func(HealthTransition), onCheckResult func(string, Result)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onTransition = onTransition
	s.onCheckResult = onCheckResult
}

func (s *Scheduler) VIPName() string { return s.vipName }

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
	spanCtx, span := s.tracer.Start(parentCtx, "health_check",
		trace.WithAttributes(
			attribute.String("vip.name", s.vipName),
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
		slog.Debug("check passed", "vip", s.vipName, "duration", result.Duration, "detail", result.Detail)
	} else {
		slog.Debug("check failed", "vip", s.vipName, "duration", result.Duration, "detail", result.Detail)
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
			slog.Info("VIP healthy", "vip", s.vipName, "consecutive_ok", s.consecutiveOK, "rise", s.rise)
			transition = &HealthTransition{
				Ctx:         spanCtx,
				VIPName:     s.vipName,
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
			slog.Warn("VIP unhealthy", "vip", s.vipName, "consecutive_fail", s.consecutiveFail, "fall", s.fall, "detail", result.Detail)
			transition = &HealthTransition{
				Ctx:         spanCtx,
				VIPName:     s.vipName,
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
		onCheckResult(s.vipName, result)
	}

	if transition != nil && onTransition != nil {
		onTransition(*transition)
	}

	return result
}
