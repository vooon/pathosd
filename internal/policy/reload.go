package policy

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"time"

	"github.com/vooon/pathosd/internal/checks"
	"github.com/vooon/pathosd/internal/config"
	"github.com/vooon/pathosd/internal/model"
)

// CheckSeed is the state a check starts from when a reload commits it: the
// verdict its shadow scheduler reached and the result that produced it.
type CheckSeed struct {
	Health     model.HealthStatus
	LastResult checks.Result
	LastTime   time.Time
}

// AddVIP registers a VIP added by a config reload. It starts withdrawn with
// unknown health, exactly like a VIP at startup.
func (m *Manager) AddVIP(cfg *config.VIPConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.vips[cfg.Name]; exists {
		slog.Error("AddVIP: VIP already registered", "vip", cfg.Name)
		return
	}
	m.order = append(m.order, cfg.Name)
	m.vips[cfg.Name] = newVIPEntry(cfg, time.Now())
	m.metrics.VIPState.WithLabelValues(cfg.Name, cfg.Prefix).Set(float64(model.StateWithdrawn))
	m.metrics.VIPPriority.WithLabelValues(cfg.Name, cfg.Prefix).Set(float64(priorityFor(&cfg.Policy, model.StateWithdrawn)))
}

// RemoveVIP withdraws a VIP's route if it is installed and forgets the VIP,
// including its metric series. The schedulers must already be stopped. A
// withdraw error is returned and the VIP is kept so a later reload (or
// restart) can retry; nothing else is left half done.
func (m *Manager) RemoveVIP(ctx context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.vips[name]
	if !ok {
		return nil
	}
	if entry.state.state != model.StateWithdrawn && m.notifier != nil {
		if err := m.notifier.WithdrawVIP(ctx, entry.cfg.Prefix); err != nil {
			return fmt.Errorf("withdrawing %s (%s): %w", name, entry.cfg.Prefix, err)
		}
		slog.Info("VIP removed by reload, route withdrawn", "vip", name, "prefix", entry.cfg.Prefix)
	}
	delete(m.vips, name)
	if idx := slices.Index(m.order, name); idx >= 0 {
		m.order = slices.Delete(m.order, idx, idx+1)
	}
	m.metrics.DeleteVIPSeries(name)
	return nil
}

// CommitVIP switches a VIP to a new definition with the same name and
// prefix. seeds carries the state of every check whose definition changed
// (its shadow verdict); checks absent from seeds keep their running state,
// checks absent from the new config are dropped. The VIP health and state
// are re-evaluated once, and the route is re-originated when the announce
// attributes changed.
func (m *Manager) CommitVIP(ctx context.Context, cfg *config.VIPConfig, seeds map[string]CheckSeed) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.vips[cfg.Name]
	if !ok {
		return fmt.Errorf("CommitVIP: unknown VIP %q", cfg.Name)
	}
	if entry.cfg.Prefix != cfg.Prefix {
		return fmt.Errorf("CommitVIP: prefix of %q changed from %s to %s", cfg.Name, entry.cfg.Prefix, cfg.Prefix)
	}
	vs := &entry.state

	next := make(map[string]*checkState)
	names := make([]string, 0, len(cfg.Checks)+1)
	for _, c := range cfg.CheckList() {
		name := c.EffectiveName()
		names = append(names, name)
		if seed, ok := seeds[name]; ok {
			next[name] = &checkState{cfg: c, health: seed.Health, lastResult: seed.LastResult, lastTime: seed.LastTime}
			continue
		}
		if old, ok := vs.checks[name]; ok {
			old.cfg = c
			next[name] = old
			continue
		}
		return fmt.Errorf("CommitVIP: check %q of %q has neither a running state nor a seed", name, cfg.Name)
	}
	for name := range vs.checks {
		if _, kept := next[name]; !kept {
			m.metrics.DeleteCheckSeries(cfg.Name, name)
		}
	}
	eval, err := config.CompileChecksPolicy(cfg.EffectiveChecksPolicy(), names)
	if err != nil {
		return fmt.Errorf("CommitVIP: checks_policy of %q: %w", cfg.Name, err)
	}

	oldAttrs := RouteAttrsFor(&entry.cfg.Policy, vs.state)
	entry.cfg = cfg
	entry.eval = eval
	vs.checks = next
	m.metrics.VIPPriority.WithLabelValues(cfg.Name, cfg.Prefix).Set(float64(priorityFor(&cfg.Policy, vs.state)))

	vs.health = combineHealth(eval, next)
	if vs.health == model.HealthUnknown {
		return nil
	}
	filePresent := lowerPriorityFilePresent(cfg.Policy.LowerPriorityFile)
	vs.lowerPriorityFileOn = filePresent
	newState := Evaluate(vs.health == model.HealthHealthy, filePresent, &cfg.Policy)
	if newState != vs.state {
		m.transitionStateLocked(ctx, vs, cfg, newState, "config reload")
		return nil
	}
	// Same state, maybe new attributes: re-originate without a withdraw.
	if vs.state != model.StateWithdrawn && m.notifier != nil &&
		(vs.bgpPending || !reflect.DeepEqual(oldAttrs, RouteAttrsFor(&cfg.Policy, vs.state))) {
		vs.bgpPending = m.applyBGP(ctx, cfg, vs.state) != nil
		if !vs.bgpPending {
			slog.Info("VIP route attributes updated by reload", "vip", cfg.Name, "prefix", cfg.Prefix, "state", vs.state.String())
		}
	}
	return nil
}

// VIPNames returns the registered VIP names in order.
func (m *Manager) VIPNames() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.order)
}
