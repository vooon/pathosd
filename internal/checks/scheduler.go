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
	triggerCh       chan chan Result
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
		triggerCh:     make(chan chan Result, 1),
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
		case replyCh := <-s.triggerCh:
			result := s.runCheck(ctx)
			replyCh <- result
		}
	}
}

func (s *Scheduler) TriggerCheck(ctx context.Context) (Result, error) {
	replyCh := make(chan Result, 1)
	select {
	case s.triggerCh <- replyCh:
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
	select {
	case result := <-replyCh:
		return result, nil
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
}

func (s *Scheduler) SetCallbacks(onTransition func(HealthTransition), onCheckResult func(string, Result)) {
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

	if s.onCheckResult != nil {
		s.onCheckResult(s.vipName, result)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.lastResult = result
	transitioned := false
	var reason string

	if result.Success {
		s.consecutiveOK++
		s.consecutiveFail = 0
		if !s.healthy && s.consecutiveOK >= s.rise {
			s.healthy = true
			transitioned = true
			reason = "rise threshold reached"
			slog.Info("VIP healthy", "vip", s.vipName, "consecutive_ok", s.consecutiveOK, "rise", s.rise)
		}
	} else {
		s.consecutiveFail++
		s.consecutiveOK = 0
		if s.healthy && s.consecutiveFail >= s.fall {
			s.healthy = false
			transitioned = true
			reason = "fall threshold reached"
			slog.Warn("VIP unhealthy", "vip", s.vipName, "consecutive_fail", s.consecutiveFail, "fall", s.fall, "detail", result.Detail)
		}
	}

	if transitioned && s.onTransition != nil {
		s.onTransition(HealthTransition{
			VIPName:     s.vipName,
			Healthy:     s.healthy,
			Reason:      reason,
			CheckResult: result,
		})
	}

	return result
}
