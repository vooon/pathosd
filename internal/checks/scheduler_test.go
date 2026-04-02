package checks

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeChecker is a controllable Checker for testing.
type fakeChecker struct {
	mu      sync.Mutex
	results []Result
	idx     int
}

func (f *fakeChecker) Type() string { return "fake" }

func (f *fakeChecker) Check(_ context.Context) Result {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.idx < len(f.results) {
		r := f.results[f.idx]
		f.idx++
		return r
	}
	// repeat last result when exhausted
	return f.results[len(f.results)-1]
}

func successResult() Result { return Result{Success: true, Detail: "ok"} }
func failResult() Result    { return Result{Success: false, Detail: "fail"} }

func makeScheduler(checker Checker, rise, fall int) *Scheduler {
	return NewScheduler(SchedulerConfig{
		VIPName:  "test-vip",
		Checker:  checker,
		Interval: 1 * time.Millisecond,
		Timeout:  1 * time.Second,
		Rise:     rise,
		Fall:     fall,
	})
}

func TestScheduler_StartUnhealthy(t *testing.T) {
	fake := &fakeChecker{results: []Result{failResult()}}
	s := makeScheduler(fake, 2, 2)
	assert.False(t, s.IsHealthy())
}

func TestScheduler_VIPName(t *testing.T) {
	fake := &fakeChecker{results: []Result{successResult()}}
	s := makeScheduler(fake, 1, 1)
	assert.Equal(t, "test-vip", s.VIPName())
}

func TestScheduler_RiseFSM(t *testing.T) {
	fake := &fakeChecker{results: []Result{successResult(), successResult()}}
	s := makeScheduler(fake, 2, 2)
	ctx := context.TODO()

	s.runCheck(ctx)
	assert.False(t, s.IsHealthy(), "not healthy after 1 success")
	assert.Equal(t, 1, s.ConsecutiveOK())
	assert.Equal(t, 0, s.ConsecutiveFail())

	s.runCheck(ctx)
	assert.True(t, s.IsHealthy(), "healthy after rise=2 successes")
	assert.Equal(t, 2, s.ConsecutiveOK())
}

func TestScheduler_FallFSM(t *testing.T) {
	fake := &fakeChecker{results: []Result{
		successResult(), successResult(), // bring to healthy
		failResult(), failResult(), // fall
	}}
	s := makeScheduler(fake, 2, 2)
	ctx := context.TODO()

	s.runCheck(ctx)
	s.runCheck(ctx)
	require.True(t, s.IsHealthy(), "precondition: should be healthy")

	s.runCheck(ctx)
	assert.True(t, s.IsHealthy(), "still healthy after 1 failure")
	assert.Equal(t, 1, s.ConsecutiveFail())

	s.runCheck(ctx)
	assert.False(t, s.IsHealthy(), "unhealthy after fall=2 failures")
	assert.Equal(t, 2, s.ConsecutiveFail())
}

func TestScheduler_MixedResultResetCounters(t *testing.T) {
	fake := &fakeChecker{results: []Result{
		successResult(), failResult(), successResult(),
	}}
	s := makeScheduler(fake, 3, 2)
	ctx := context.TODO()

	s.runCheck(ctx)
	assert.Equal(t, 1, s.ConsecutiveOK())

	s.runCheck(ctx)
	assert.Equal(t, 0, s.ConsecutiveOK(), "failure resets consecutiveOK")
	assert.Equal(t, 1, s.ConsecutiveFail())

	s.runCheck(ctx)
	assert.Equal(t, 1, s.ConsecutiveOK(), "success resets consecutiveFail")
	assert.Equal(t, 0, s.ConsecutiveFail())
}

func TestScheduler_OnTransitionCallback(t *testing.T) {
	fake := &fakeChecker{results: []Result{successResult(), successResult()}}

	var mu sync.Mutex
	var transitions []HealthTransition

	s := NewScheduler(SchedulerConfig{
		VIPName:  "test-vip",
		Checker:  fake,
		Interval: 1 * time.Millisecond,
		Timeout:  1 * time.Second,
		Rise:     2,
		Fall:     2,
		OnTransition: func(ht HealthTransition) {
			mu.Lock()
			defer mu.Unlock()
			transitions = append(transitions, ht)
		},
	})

	ctx := context.TODO()
	s.runCheck(ctx)
	s.runCheck(ctx)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, transitions, 1)
	assert.True(t, transitions[0].Healthy)
	assert.Equal(t, "test-vip", transitions[0].VIPName)
	assert.Equal(t, "rise threshold reached", transitions[0].Reason)
}

func TestScheduler_OnCheckResultCallback(t *testing.T) {
	fake := &fakeChecker{results: []Result{successResult()}}

	var mu sync.Mutex
	var results []Result

	s := NewScheduler(SchedulerConfig{
		VIPName:  "test-vip",
		Checker:  fake,
		Interval: 1 * time.Millisecond,
		Timeout:  1 * time.Second,
		Rise:     1,
		Fall:     1,
		OnCheckResult: func(_ string, r Result) {
			mu.Lock()
			defer mu.Unlock()
			results = append(results, r)
		},
	})

	s.runCheck(context.TODO())

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, results, 1)
	assert.True(t, results[0].Success)
}

func TestScheduler_LastResult(t *testing.T) {
	expected := Result{Success: true, Detail: "specific detail"}
	fake := &fakeChecker{results: []Result{expected}}
	s := makeScheduler(fake, 1, 1)

	s.runCheck(context.TODO())

	assert.Equal(t, expected, s.LastResult())
}

func TestScheduler_SetCallbacks(t *testing.T) {
	fake := &fakeChecker{results: []Result{successResult()}}
	s := makeScheduler(fake, 1, 1)

	var called bool
	s.SetCallbacks(func(ht HealthTransition) { called = true }, nil)

	s.runCheck(context.TODO())
	assert.True(t, called, "transition callback should fire when rise=1 passes")
}

func TestScheduler_TriggerCheck(t *testing.T) {
	fake := &fakeChecker{results: []Result{successResult()}}
	s := makeScheduler(fake, 1, 1)

	ctx, cancel := context.WithTimeout(context.TODO(), 2*time.Second)
	defer cancel()

	go s.Run(ctx)

	result, err := s.TriggerCheck(ctx)
	require.NoError(t, err)
	assert.True(t, result.Success)
}

func TestScheduler_TriggerCheck_ContextCancelled(t *testing.T) {
	fake := &fakeChecker{results: []Result{successResult()}}
	s := makeScheduler(fake, 1, 1)

	// Run is not started; cancel context before calling TriggerCheck.
	ctx, cancel := context.WithCancel(context.TODO())
	cancel()

	_, err := s.TriggerCheck(ctx)
	// Either the first select or second select catches cancellation.
	assert.Error(t, err)
}
