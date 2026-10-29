package checks

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func namedScheduler(vip, check string, checker Checker) *Scheduler {
	return NewScheduler(SchedulerConfig{
		VIPName: vip, CheckName: check, Checker: checker,
		Interval: time.Millisecond, Timeout: time.Second, Rise: 1, Fall: 1,
	})
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	require.Eventually(t, cond, 2*time.Second, time.Millisecond)
}

func TestRegistry_StartStopAndListing(t *testing.T) {
	r := NewRegistry()
	a := namedScheduler("web", "http", &fakeChecker{results: []Result{successResult()}})
	b := namedScheduler("web", "tcp", &fakeChecker{results: []Result{successResult()}})
	r.Add(a)
	r.Add(b)
	assert.Equal(t, 0, a.ConsecutiveOK(), "not running before Start")

	r.Start()
	waitFor(t, func() bool { return a.ConsecutiveOK() > 0 && b.ConsecutiveOK() > 0 })
	assert.Equal(t, map[string][]*Scheduler{"web": {a, b}}, r.Schedulers())

	c := namedScheduler("dns", "tcp", &fakeChecker{results: []Result{successResult()}})
	r.Add(c)
	waitFor(t, func() bool { return c.ConsecutiveOK() > 0 })
	assert.Len(t, r.Schedulers(), 2)

	r.Remove(b)
	assert.Equal(t, []*Scheduler{a}, r.Schedulers()["web"])
	r.RemoveVIP("dns")
	assert.NotContains(t, r.Schedulers(), "dns")

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, r.Stop(ctx))
	n := a.ConsecutiveOK()
	time.Sleep(5 * time.Millisecond)
	assert.Equal(t, n, a.ConsecutiveOK(), "stopped scheduler must not run")
}

func TestRegistry_ShadowAndPromote(t *testing.T) {
	r := NewRegistry()
	old := namedScheduler("web", "http", &fakeChecker{results: []Result{successResult()}})
	r.Add(old)
	r.Start()
	waitFor(t, func() bool { return old.ConsecutiveOK() > 0 })

	shadow := namedScheduler("web", "http", &fakeChecker{results: []Result{successResult()}})
	r.AddShadow(shadow)
	waitFor(t, func() bool { return shadow.ConsecutiveOK() > 0 })
	assert.Equal(t, []*Scheduler{old}, r.Schedulers()["web"], "shadow is not listed")

	replaced := r.Promote(shadow)
	assert.Same(t, old, replaced)
	assert.Equal(t, []*Scheduler{shadow}, r.Schedulers()["web"])
	n := old.ConsecutiveOK()
	time.Sleep(5 * time.Millisecond)
	assert.Equal(t, n, old.ConsecutiveOK(), "replaced scheduler is stopped")

	// Promoting a shadow with a new check name appends it.
	extra := namedScheduler("web", "dns", &fakeChecker{results: []Result{successResult()}})
	r.AddShadow(extra)
	assert.Nil(t, r.Promote(extra))
	assert.Equal(t, []*Scheduler{shadow, extra}, r.Schedulers()["web"])

	// Removing a shadow that was never promoted stops it too.
	dropped := namedScheduler("web", "ping", &fakeChecker{results: []Result{successResult()}})
	r.AddShadow(dropped)
	waitFor(t, func() bool { return dropped.ConsecutiveOK() > 0 })
	r.Remove(dropped)
	assert.Equal(t, []*Scheduler{shadow, extra}, r.Schedulers()["web"])

	require.NoError(t, r.Stop(context.Background()))
}
