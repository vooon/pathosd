package checks

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

type HealthTransition struct {
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
	ctx, cancel := context.WithTimeout(parentCtx, s.timeout)
	defer cancel()

	result := s.checker.Check(ctx)

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
