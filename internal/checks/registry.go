package checks

import (
	"context"
	"fmt"
	"slices"
	"sync"
)

// Registry owns the goroutines of every scheduler the daemon runs and is the
// source of the per-VIP scheduler list the HTTP API shows. Schedulers can be
// added, removed and replaced while the daemon runs, which is how a config
// reload swaps checks without restarting.
//
// A shadow scheduler runs like any other but is not listed: a reload starts
// the new definition of a changed check in shadow and promotes it once it
// reached a verdict, so the old check keeps governing the VIP until then.
type Registry struct {
	mu      sync.Mutex
	started bool
	visible map[string][]*Scheduler // by VIP name, config order
	running map[*Scheduler]*runningScheduler
}

type runningScheduler struct {
	cancel context.CancelFunc
	done   chan struct{}
	shadow bool
}

func NewRegistry() *Registry {
	return &Registry{
		visible: make(map[string][]*Scheduler),
		running: make(map[*Scheduler]*runningScheduler),
	}
}

// Add registers a scheduler under its VIP and, once the registry is started,
// runs it.
func (r *Registry) Add(s *Scheduler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.visible[s.VIPName()] = append(r.visible[s.VIPName()], s)
	if r.started {
		r.launchLocked(s, false)
	}
}

// AddShadow runs a scheduler without listing it. The registry must be
// started.
func (r *Registry) AddShadow(s *Scheduler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.launchLocked(s, true)
}

// Promote lists a shadow scheduler in place of the visible scheduler with
// the same VIP and check name (which is stopped) or appends it when there is
// none. It returns the scheduler it replaced, if any. The call returns once
// the replaced scheduler's goroutine has exited.
func (r *Registry) Promote(s *Scheduler) *Scheduler {
	r.mu.Lock()
	run, ok := r.running[s]
	if ok {
		run.shadow = false
	}
	var old *Scheduler
	list := r.visible[s.VIPName()]
	idx := slices.IndexFunc(list, func(x *Scheduler) bool { return x.CheckName() == s.CheckName() })
	if idx >= 0 {
		old = list[idx]
		list[idx] = s
	} else {
		list = append(list, s)
	}
	r.visible[s.VIPName()] = list
	var oldRun *runningScheduler
	if old != nil {
		oldRun = r.running[old]
		delete(r.running, old)
	}
	r.mu.Unlock()
	stopRunning(oldRun)
	return old
}

// Remove stops a scheduler (visible or shadow) and waits for its goroutine.
func (r *Registry) Remove(s *Scheduler) {
	r.mu.Lock()
	run := r.running[s]
	delete(r.running, s)
	list := r.visible[s.VIPName()]
	if idx := slices.Index(list, s); idx >= 0 {
		list = slices.Delete(list, idx, idx+1)
	}
	if len(list) == 0 {
		delete(r.visible, s.VIPName())
	} else {
		r.visible[s.VIPName()] = list
	}
	r.mu.Unlock()
	stopRunning(run)
}

// RemoveVIP stops every visible scheduler of a VIP.
func (r *Registry) RemoveVIP(vip string) {
	r.mu.Lock()
	list := r.visible[vip]
	r.mu.Unlock()
	for _, s := range list {
		r.Remove(s)
	}
}

// Schedulers returns the visible schedulers per VIP (a copy).
func (r *Registry) Schedulers() map[string][]*Scheduler {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string][]*Scheduler, len(r.visible))
	for vip, list := range r.visible {
		out[vip] = slices.Clone(list)
	}
	return out
}

// Start runs every registered scheduler; later Add calls run immediately.
func (r *Registry) Start() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.started = true
	for _, list := range r.visible {
		for _, s := range list {
			if _, running := r.running[s]; !running {
				r.launchLocked(s, false)
			}
		}
	}
}

// Stop cancels every scheduler and waits for them, bounded by ctx.
func (r *Registry) Stop(ctx context.Context) error {
	r.mu.Lock()
	r.started = false
	runs := make([]*runningScheduler, 0, len(r.running))
	for s, run := range r.running {
		run.cancel()
		runs = append(runs, run)
		delete(r.running, s)
	}
	r.mu.Unlock()
	for _, run := range runs {
		select {
		case <-run.done:
		case <-ctx.Done():
			return fmt.Errorf("waiting for schedulers to stop: %w", ctx.Err())
		}
	}
	return nil
}

func (r *Registry) launchLocked(s *Scheduler, shadow bool) {
	ctx, cancel := context.WithCancel(context.Background())
	run := &runningScheduler{cancel: cancel, done: make(chan struct{}), shadow: shadow}
	r.running[s] = run
	go func() {
		defer close(run.done)
		s.Run(ctx)
	}()
}

func stopRunning(run *runningScheduler) {
	if run == nil {
		return
	}
	run.cancel()
	<-run.done
}
