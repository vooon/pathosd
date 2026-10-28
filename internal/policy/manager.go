package policy

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/vooon/pathosd/internal/checks"
	"github.com/vooon/pathosd/internal/config"
	"github.com/vooon/pathosd/internal/metrics"
	"github.com/vooon/pathosd/internal/model"
)

type StateChange struct {
	VIPName  string
	Prefix   string
	OldState model.VIPState
	NewState model.VIPState
	Reason   string
}

type BGPNotifier interface {
	AnnounceVIP(ctx context.Context, prefix string, attrs model.RouteAttrs) error
	WithdrawVIP(ctx context.Context, prefix string) error
	PessimizeVIP(ctx context.Context, prefix string, attrs model.RouteAttrs) error
}

type Manager struct {
	mu   sync.Mutex
	vips map[string]*vipEntry
	// order lists VIP names in config order, so status output is stable.
	order    []string
	metrics  *metrics.Metrics
	notifier BGPNotifier
	// onTransition, when set, is invoked (on its own goroutine, outside the
	// lock) after every VIP state transition.
	onTransition func()
}

// SetTransitionListener registers fn to be called after each VIP state
// transition. It must be set before the schedulers start.
func (m *Manager) SetTransitionListener(fn func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onTransition = fn
}

type vipEntry struct {
	cfg   *config.VIPConfig
	state vipState
}

type vipState struct {
	state                model.VIPState
	health               model.HealthStatus
	lowerPriorityFileOn  bool
	lastCheckResult      checks.Result
	lastCheckTime        time.Time
	lastTransitionAt     time.Time
	lastTransitionReason string
	// bgpPending is set when applying the current state to BGP failed; the
	// operation is retried on every subsequent check result.
	bgpPending bool
}

func NewManager(vipConfigs []config.VIPConfig, m *metrics.Metrics, notifier BGPNotifier) *Manager {
	mgr := &Manager{
		vips:     make(map[string]*vipEntry, len(vipConfigs)),
		order:    make([]string, 0, len(vipConfigs)),
		metrics:  m,
		notifier: notifier,
	}
	now := time.Now()
	for i := range vipConfigs {
		v := &vipConfigs[i]
		mgr.order = append(mgr.order, v.Name)
		mgr.vips[v.Name] = &vipEntry{
			cfg: v,
			state: vipState{
				state:                model.StateWithdrawn,
				health:               model.HealthUnknown,
				lowerPriorityFileOn:  lowerPriorityFilePresent(v.Policy.LowerPriorityFile),
				lastTransitionAt:     now,
				lastTransitionReason: "initial",
			},
		}
		m.VIPState.WithLabelValues(v.Name, v.Prefix).Set(float64(model.StateWithdrawn))
		m.VIPPriority.WithLabelValues(v.Name, v.Prefix).Set(float64(priorityFor(&v.Policy, model.StateWithdrawn)))
	}
	return mgr
}

func (m *Manager) OnHealthTransition(t checks.HealthTransition) {
	m.mu.Lock()
	defer m.mu.Unlock()

	ctx := t.Ctx
	if ctx == nil {
		ctx = context.Background()
	}

	entry, ok := m.vips[t.VIPName]
	if !ok {
		slog.Error("transition for unknown VIP", "vip", t.VIPName)
		return
	}
	cfg := entry.cfg
	vs := &entry.state

	if t.Healthy {
		vs.health = model.HealthHealthy
	} else {
		vs.health = model.HealthUnhealthy
	}

	filePresent := lowerPriorityFilePresent(cfg.Policy.LowerPriorityFile)
	vs.lowerPriorityFileOn = filePresent

	newState := Evaluate(t.Healthy, filePresent, &cfg.Policy)
	m.transitionStateLocked(ctx, vs, cfg, newState, t.Reason)
}

func (m *Manager) OnCheckResult(vipName string, result checks.Result) {
	m.mu.Lock()
	entry, ok := m.vips[vipName]
	if !ok {
		m.mu.Unlock()
		return
	}
	cfg := entry.cfg
	vs := &entry.state
	vs.lastCheckResult = result
	vs.lastCheckTime = time.Now()

	filePresent := lowerPriorityFilePresent(cfg.Policy.LowerPriorityFile)
	fileChanged := filePresent != vs.lowerPriorityFileOn
	vs.lowerPriorityFileOn = filePresent

	if vs.health != model.HealthUnknown {
		healthy := vs.health == model.HealthHealthy
		newState := Evaluate(healthy, filePresent, &cfg.Policy)
		if newState != vs.state {
			reason := "policy reevaluation"
			if fileChanged && cfg.Policy.LowerPriorityFile != "" {
				if filePresent {
					reason = "lower_priority_file created"
				} else {
					reason = "lower_priority_file removed"
				}
			}
			m.transitionStateLocked(context.Background(), vs, cfg, newState, reason)
		}
	}
	if vs.bgpPending && m.notifier != nil {
		if err := m.applyBGP(context.Background(), cfg, vs.state); err == nil {
			vs.bgpPending = false
			slog.Info("BGP state change retry succeeded", "vip", cfg.Name, "prefix", cfg.Prefix, "state", vs.state.String())
		}
	}
	m.mu.Unlock()

	checkType := cfg.Check.Type

	resultLabel := "fail"
	if result.Success {
		resultLabel = "success"
		m.metrics.CheckLastResult.WithLabelValues(vipName, cfg.Prefix).Set(1)
	} else {
		m.metrics.CheckLastResult.WithLabelValues(vipName, cfg.Prefix).Set(0)
	}

	m.metrics.CheckTotal.WithLabelValues(vipName, cfg.Prefix, checkType, resultLabel).Inc()
	m.metrics.CheckDuration.WithLabelValues(vipName, cfg.Prefix, checkType).Observe(result.Duration.Seconds())

	if result.TimedOut {
		m.metrics.CheckTimeoutExceeded.WithLabelValues(vipName, cfg.Prefix).Inc()
	}

	if cfg.Check.DetailedMetrics {
		m.metrics.ObserveCheckDetails(vipName, cfg.Prefix, result.Details)
	}
}

func (m *Manager) GetVIPStatuses() []model.VIPStatus {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]model.VIPStatus, 0, len(m.vips))
	for _, name := range m.order {
		entry := m.vips[name]
		cfg := entry.cfg
		vs := entry.state
		attrs := RouteAttrsFor(&cfg.Policy, vs.state)
		out = append(out, model.VIPStatus{
			Name:                 name,
			Prefix:               cfg.Prefix,
			State:                vs.state,
			StateName:            vs.state.String(),
			Health:               vs.health,
			HealthName:           vs.health.String(),
			LastCheckSuccess:     vs.lastCheckResult.Success,
			LastCheckDetail:      vs.lastCheckResult.Detail,
			LastCheckTime:        vs.lastCheckTime,
			LastTransitionTime:   vs.lastTransitionAt,
			LastTransitionReason: vs.lastTransitionReason,
			CheckType:            cfg.Check.Type,
			BGPPending:           vs.bgpPending,
			ASPathPrependBase:    BaseRouteAttrs(&cfg.Policy).ASPathPrepend,
			ASPathPrepend:        attrs.ASPathPrepend,
			Communities:          attrs.Communities,
		})
	}
	return out
}

// priorityFor returns the value of pathosd_vip_priority_status for a state:
// the prepend count the route carries while pessimized, otherwise the base
// prepend this node would announce with.
func priorityFor(p *config.PolicyConfig, state model.VIPState) int {
	if state == model.StatePessimized {
		return RouteAttrsFor(p, state).ASPathPrepend
	}
	return BaseRouteAttrs(p).ASPathPrepend
}

func lowerPriorityFilePresent(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

func (m *Manager) transitionStateLocked(ctx context.Context, vs *vipState, cfg *config.VIPConfig, newState model.VIPState, reason string) {
	oldState := vs.state
	if newState == oldState {
		return
	}

	vs.state = newState
	vs.lastTransitionAt = time.Now()
	vs.lastTransitionReason = reason

	slog.Info("VIP state transition",
		"vip", cfg.Name, "prefix", cfg.Prefix,
		"from", oldState.String(), "to", newState.String(),
		"reason", reason)

	m.metrics.VIPState.WithLabelValues(cfg.Name, cfg.Prefix).Set(float64(newState))
	m.metrics.VIPTransitions.WithLabelValues(cfg.Name, cfg.Prefix, newState.String()).Inc()
	m.metrics.VIPLastTransition.WithLabelValues(cfg.Name, cfg.Prefix).Set(float64(vs.lastTransitionAt.Unix()))

	m.metrics.VIPPriority.WithLabelValues(cfg.Name, cfg.Prefix).Set(float64(priorityFor(&cfg.Policy, newState)))

	if m.notifier != nil {
		vs.bgpPending = m.applyBGP(ctx, cfg, newState) != nil
	}
	if m.onTransition != nil {
		go m.onTransition()
	}
}

func (m *Manager) applyBGP(ctx context.Context, cfg *config.VIPConfig, state model.VIPState) error {
	var err error
	switch state {
	case model.StateAnnounced:
		err = m.notifier.AnnounceVIP(ctx, cfg.Prefix, RouteAttrsFor(&cfg.Policy, state))
	case model.StateWithdrawn:
		err = m.notifier.WithdrawVIP(ctx, cfg.Prefix)
	case model.StatePessimized:
		err = m.notifier.PessimizeVIP(ctx, cfg.Prefix, RouteAttrsFor(&cfg.Policy, state))
	}
	if err != nil {
		slog.Error("BGP state change failed", "vip", cfg.Name, "prefix", cfg.Prefix, "state", state.String(), "error", err)
	}
	return err
}
