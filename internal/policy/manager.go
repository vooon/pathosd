package policy

import (
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
	AnnounceVIP(prefix string) error
	WithdrawVIP(prefix string) error
	PessimizeVIP(prefix string, prepend int, communities []string) error
}

type Manager struct {
	mu       sync.Mutex
	vips     map[string]*vipState
	configs  map[string]*config.VIPConfig
	metrics  *metrics.Metrics
	notifier BGPNotifier
}

type vipState struct {
	state                model.VIPState
	health               model.HealthStatus
	lowerPriorityFileOn  bool
	lastCheckResult      checks.Result
	lastCheckTime        time.Time
	lastTransitionAt     time.Time
	lastTransitionReason string
}

func NewManager(vipConfigs []config.VIPConfig, m *metrics.Metrics, notifier BGPNotifier) *Manager {
	mgr := &Manager{
		vips:     make(map[string]*vipState, len(vipConfigs)),
		configs:  make(map[string]*config.VIPConfig, len(vipConfigs)),
		metrics:  m,
		notifier: notifier,
	}
	now := time.Now()
	for i := range vipConfigs {
		v := &vipConfigs[i]
		mgr.configs[v.Name] = v
		mgr.vips[v.Name] = &vipState{
			state:                model.StateWithdrawn,
			health:               model.HealthUnknown,
			lowerPriorityFileOn:  lowerPriorityFilePresent(v.Policy.LowerPriorityFile),
			lastTransitionAt:     now,
			lastTransitionReason: "initial",
		}
		m.VIPState.WithLabelValues(v.Name, v.Prefix).Set(float64(model.StateWithdrawn))
		m.VIPPriority.WithLabelValues(v.Name, v.Prefix).Set(1)
	}
	return mgr
}

func (m *Manager) OnHealthTransition(t checks.HealthTransition) {
	m.mu.Lock()
	defer m.mu.Unlock()

	vs, ok := m.vips[t.VIPName]
	if !ok {
		slog.Error("transition for unknown VIP", "vip", t.VIPName)
		return
	}
	cfg := m.configs[t.VIPName]

	if t.Healthy {
		vs.health = model.HealthHealthy
	} else {
		vs.health = model.HealthUnhealthy
	}

	filePresent := lowerPriorityFilePresent(cfg.Policy.LowerPriorityFile)
	vs.lowerPriorityFileOn = filePresent

	newState := Evaluate(t.Healthy, filePresent, &cfg.Policy)
	m.transitionStateLocked(vs, cfg, newState, t.Reason)
}

func (m *Manager) OnCheckResult(vipName string, result checks.Result) {
	m.mu.Lock()
	vs, ok := m.vips[vipName]
	if !ok {
		m.mu.Unlock()
		return
	}
	cfg := m.configs[vipName]
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
			m.transitionStateLocked(vs, cfg, newState, reason)
		}
	}
	m.mu.Unlock()

	checkType := cfg.Check.Type

	resultLabel := "fail"
	if result.Success {
		resultLabel = "success"
		m.metrics.CheckLastResult.WithLabelValues(vipName).Set(1)
	} else {
		m.metrics.CheckLastResult.WithLabelValues(vipName).Set(0)
	}

	m.metrics.CheckTotal.WithLabelValues(vipName, checkType, resultLabel).Inc()
	m.metrics.CheckDuration.WithLabelValues(vipName, checkType).Observe(result.Duration.Seconds())

	if result.TimedOut {
		m.metrics.CheckTimeoutExceeded.WithLabelValues(vipName).Inc()
	}
}

func (m *Manager) GetVIPStatuses() []model.VIPStatus {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]model.VIPStatus, 0, len(m.vips))
	for name, vs := range m.vips {
		cfg := m.configs[name]
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
		})
	}
	return out
}

func lowerPriorityFilePresent(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

func (m *Manager) transitionStateLocked(vs *vipState, cfg *config.VIPConfig, newState model.VIPState, reason string) {
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
	m.metrics.VIPTransitions.WithLabelValues(cfg.Name, newState.String()).Inc()
	m.metrics.VIPLastTransition.WithLabelValues(cfg.Name).Set(float64(vs.lastTransitionAt.Unix()))

	priority := 1.0
	if newState == model.StatePessimized && cfg.Policy.LowerPriority != nil && cfg.Policy.LowerPriority.ASPathPrepend != nil {
		priority = float64(*cfg.Policy.LowerPriority.ASPathPrepend)
	}
	m.metrics.VIPPriority.WithLabelValues(cfg.Name, cfg.Prefix).Set(priority)

	if m.notifier != nil {
		m.applyBGP(cfg, newState)
	}
}

func (m *Manager) applyBGP(cfg *config.VIPConfig, state model.VIPState) {
	var err error
	switch state {
	case model.StateAnnounced:
		err = m.notifier.AnnounceVIP(cfg.Prefix)
	case model.StateWithdrawn:
		err = m.notifier.WithdrawVIP(cfg.Prefix)
	case model.StatePessimized:
		prepend := 6
		var communities []string
		if cfg.Policy.LowerPriority != nil {
			if cfg.Policy.LowerPriority.ASPathPrepend != nil {
				prepend = *cfg.Policy.LowerPriority.ASPathPrepend
			}
			communities = cfg.Policy.LowerPriority.Communities
		}
		err = m.notifier.PessimizeVIP(cfg.Prefix, prepend, communities)
	}
	if err != nil {
		slog.Error("BGP state change failed", "vip", cfg.Name, "prefix", cfg.Prefix, "state", state.String(), "error", err)
	}
}
