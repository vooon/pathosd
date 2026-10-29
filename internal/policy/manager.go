package policy

import (
	"context"
	"log/slog"
	"os"
	"strings"
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
	eval  *config.ChecksEvaluator
	state vipState
}

type vipState struct {
	state model.VIPState
	// health is the combined health of all checks (checks_policy).
	health               model.HealthStatus
	checks               map[string]*checkState // keyed by check name
	lowerPriorityFileOn  bool
	lastTransitionAt     time.Time
	lastTransitionReason string
	// bgpPending is set when applying the current state to BGP failed; the
	// operation is retried on every subsequent check result.
	bgpPending bool
}

// checkState is the per-check view the policy manager keeps.
type checkState struct {
	cfg        *config.CheckConfig
	health     model.HealthStatus
	lastResult checks.Result
	lastTime   time.Time
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
		mgr.vips[v.Name] = newVIPEntry(v, now)
		m.VIPState.WithLabelValues(v.Name, v.Prefix).Set(float64(model.StateWithdrawn))
		m.VIPPriority.WithLabelValues(v.Name, v.Prefix).Set(float64(priorityFor(&v.Policy, model.StateWithdrawn)))
	}
	return mgr
}

// newVIPEntry builds the withdrawn, unknown-health entry every VIP starts
// from, at startup or when a reload adds it.
func newVIPEntry(v *config.VIPConfig, now time.Time) *vipEntry {
	cs := make(map[string]*checkState)
	names := make([]string, 0, len(v.Checks)+1)
	for _, c := range v.CheckList() {
		cs[c.EffectiveName()] = &checkState{cfg: c, health: model.HealthUnknown}
		names = append(names, c.EffectiveName())
	}
	eval, err := config.CompileChecksPolicy(v.ChecksPolicy, names)
	if err != nil {
		// Config validation rejects this earlier; fail closed to "all".
		slog.Error("invalid checks_policy, falling back to all", "vip", v.Name, "error", err)
		eval, _ = config.CompileChecksPolicy(config.ChecksPolicyAll, names)
	}
	return &vipEntry{
		cfg:  v,
		eval: eval,
		state: vipState{
			state:                model.StateWithdrawn,
			health:               model.HealthUnknown,
			checks:               cs,
			lowerPriorityFileOn:  lowerPriorityFilePresent(v.Policy.LowerPriorityFile),
			lastTransitionAt:     now,
			lastTransitionReason: "initial",
		},
	}
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

	cs := vs.checks[t.CheckName]
	if cs == nil && len(vs.checks) == 1 {
		// Single-check VIP: accept transitions that do not name the check.
		for _, only := range vs.checks {
			cs = only
		}
	}
	if cs == nil {
		slog.Error("transition for unknown check", "vip", t.VIPName, "check", t.CheckName)
		return
	}
	if t.Healthy {
		cs.health = model.HealthHealthy
	} else {
		cs.health = model.HealthUnhealthy
	}

	combined := combineHealth(entry.eval, vs.checks)
	reason := t.Reason
	if len(vs.checks) > 1 {
		reason = t.CheckName + ": " + t.Reason
	}
	if combined == vs.health {
		slog.Info("check transition without VIP health change", "vip", cfg.Name, "check", t.CheckName,
			"check_healthy", t.Healthy, "vip_health", combined.String(), "checks_policy", entry.eval.Policy())
		return
	}
	vs.health = combined

	filePresent := lowerPriorityFilePresent(cfg.Policy.LowerPriorityFile)
	vs.lowerPriorityFileOn = filePresent

	newState := Evaluate(combined == model.HealthHealthy, filePresent, &cfg.Policy)
	m.transitionStateLocked(ctx, vs, cfg, newState, reason)
}

// combineHealth folds the per-check health into the VIP health following
// checks_policy. Unknown counts as not healthy; the result is Unknown only
// while no check has reported yet.
func combineHealth(eval *config.ChecksEvaluator, cs map[string]*checkState) model.HealthStatus {
	anyKnown := false
	healthy := make(map[string]bool, len(cs))
	for name, c := range cs {
		if c.health != model.HealthUnknown {
			anyKnown = true
		}
		healthy[name] = c.health == model.HealthHealthy
	}
	if !anyKnown {
		return model.HealthUnknown
	}
	if eval.Eval(healthy) {
		return model.HealthHealthy
	}
	return model.HealthUnhealthy
}

// CombineChecks applies the VIP's checks_policy to an ad-hoc per-check
// outcome (e.g. a triggered run). ok is false for an unknown VIP.
func (m *Manager) CombineChecks(vipName string, healthy map[string]bool) (result, ok bool) {
	m.mu.Lock()
	entry, ok := m.vips[vipName]
	m.mu.Unlock()
	if !ok {
		return false, false
	}
	return entry.eval.Eval(healthy), true
}

func (m *Manager) OnCheckResult(vipName, checkName string, result checks.Result) {
	m.mu.Lock()
	entry, ok := m.vips[vipName]
	if !ok {
		m.mu.Unlock()
		return
	}
	cfg := entry.cfg
	vs := &entry.state
	cs := vs.checks[checkName]
	if cs == nil && len(vs.checks) == 1 {
		for _, only := range vs.checks {
			cs = only
		}
	}
	if cs == nil {
		m.mu.Unlock()
		slog.Error("result for unknown check", "vip", vipName, "check", checkName)
		return
	}
	checkName = cs.cfg.EffectiveName()
	cs.lastResult = result
	cs.lastTime = time.Now()
	detailed := cs.cfg.DetailedMetrics
	checkType := cs.cfg.Type

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

	resultLabel := "fail"
	if result.Success {
		resultLabel = "success"
		m.metrics.CheckLastResult.WithLabelValues(vipName, cfg.Prefix, checkName).Set(1)
	} else {
		m.metrics.CheckLastResult.WithLabelValues(vipName, cfg.Prefix, checkName).Set(0)
	}

	m.metrics.CheckTotal.WithLabelValues(vipName, cfg.Prefix, checkName, checkType, resultLabel).Inc()
	m.metrics.CheckDuration.WithLabelValues(vipName, cfg.Prefix, checkName, checkType).Observe(result.Duration.Seconds())

	if result.TimedOut {
		m.metrics.CheckTimeoutExceeded.WithLabelValues(vipName, cfg.Prefix, checkName).Inc()
	}

	if detailed {
		m.metrics.ObserveCheckDetails(vipName, cfg.Prefix, checkName, result.Details)
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
		summary := summarizeChecks(cfg, entry.eval, vs.checks)
		out = append(out, model.VIPStatus{
			Name:                 name,
			Prefix:               cfg.Prefix,
			State:                vs.state,
			StateName:            vs.state.String(),
			Health:               vs.health,
			HealthName:           vs.health.String(),
			LastCheckSuccess:     summary.lastSuccess,
			LastCheckDetail:      summary.lastDetail,
			LastCheckTime:        summary.lastTime,
			LastTransitionTime:   vs.lastTransitionAt,
			LastTransitionReason: vs.lastTransitionReason,
			CheckType:            summary.checkType,
			ChecksPolicy:         cfg.EffectiveChecksPolicy(),
			Checks:               summary.checks,
			BGPPending:           vs.bgpPending,
			ASPathPrependBase:    BaseRouteAttrs(&cfg.Policy).ASPathPrepend,
			ASPathPrepend:        attrs.ASPathPrepend,
			Communities:          attrs.Communities,
		})
	}
	return out
}

type checkSummary struct {
	checks      []model.CheckStatus
	checkType   string
	lastSuccess bool
	lastDetail  string
	lastTime    time.Time
}

// summarizeChecks renders the per-check list (config order) and the
// VIP-level roll-up: for a single check the old fields are unchanged; for
// several, check_type becomes "all(http, dns)", last_check_success follows
// checks_policy over the last results, and last_check_detail joins the
// per-check details as "name: detail".
func summarizeChecks(cfg *config.VIPConfig, eval *config.ChecksEvaluator, cs map[string]*checkState) checkSummary {
	var out checkSummary
	list := cfg.CheckList()
	types := make([]string, 0, len(list))
	details := make([]string, 0, len(list))
	lastOK := make(map[string]bool, len(list))
	for _, c := range list {
		name := c.EffectiveName()
		st := cs[name]
		if st == nil {
			continue
		}
		out.checks = append(out.checks, model.CheckStatus{
			Name:        name,
			Type:        c.Type,
			Health:      st.health,
			HealthName:  st.health.String(),
			LastSuccess: st.lastResult.Success,
			LastDetail:  st.lastResult.Detail,
			LastTime:    st.lastTime,
		})
		types = append(types, c.Type)
		lastOK[name] = st.lastResult.Success
		if st.lastTime.After(out.lastTime) {
			out.lastTime = st.lastTime
		}
		if len(list) == 1 {
			details = append(details, st.lastResult.Detail)
		} else if st.lastResult.Detail != "" {
			details = append(details, name+": "+st.lastResult.Detail)
		}
	}
	out.lastSuccess = eval.Eval(lastOK)
	if len(list) == 1 {
		out.checkType = list[0].Type
	} else {
		switch policy := eval.Policy(); policy {
		case config.ChecksPolicyAll, config.ChecksPolicyAny:
			out.checkType = policy + "(" + strings.Join(types, ", ") + ")"
		default:
			out.checkType = policy
		}
	}
	out.lastDetail = strings.Join(details, "; ")
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
