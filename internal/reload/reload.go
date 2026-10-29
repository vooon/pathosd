// Package reload applies a changed config file to a running daemon.
//
// Only the vips list and logging.level reload live; a change to any other
// section rejects the whole reload, so the daemon either runs exactly what
// the file says or reports why not. Removed VIPs are withdrawn, added VIPs
// start withdrawn and prove health through rise, and a VIP whose checks
// changed keeps running its old checks until every new one has reached a
// verdict in shadow (make-before-break): a healthy target never withdraws
// for a check tweak, an unhealthy one withdraws as soon as the new check
// says so.
package reload

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/vooon/pathosd/internal/bgp"
	"github.com/vooon/pathosd/internal/checks"
	"github.com/vooon/pathosd/internal/config"
	"github.com/vooon/pathosd/internal/logging"
	"github.com/vooon/pathosd/internal/metrics"
	"github.com/vooon/pathosd/internal/model"
	"github.com/vooon/pathosd/internal/policy"
)

// Policy is the part of policy.Manager a reload drives.
type Policy interface {
	AddVIP(cfg *config.VIPConfig)
	RemoveVIP(ctx context.Context, name string) error
	CommitVIP(ctx context.Context, cfg *config.VIPConfig, seeds map[string]policy.CheckSeed) error
	OnHealthTransition(t checks.HealthTransition)
	OnCheckResult(vipName, checkName string, result checks.Result)
}

// Reloader holds the running config and applies new versions of the file.
type Reloader struct {
	path string
	pol  Policy
	reg  *checks.Registry
	m    *metrics.Metrics
	bgp  *bgp.Manager // may be nil in tests
	// onChange is called after every reload attempt and whenever the
	// file-changed flag flips, outside any lock (systemd status line).
	onChange func()
	// newChecker makes the checker of one check; tests swap it.
	newChecker func(c *config.CheckConfig) (checks.Checker, error)
	// lockLogLevel keeps the level `run --debug` forced.
	lockLogLevel bool

	mu          sync.Mutex
	cur         *config.Config
	curHash     [32]byte
	loadedAt    time.Time
	generation  int
	last        *model.ReloadStatus
	staging     map[string]*stagedVIP
	fileChanged bool

	// verdicts are shadow schedulers that reached rise or fall, reported by
	// their result callback and consumed by the commit loop. The callback
	// never takes mu, so a Reload that is stopping shadows (and waits for
	// their goroutines) cannot deadlock with it.
	vmu      sync.Mutex
	verdicts []*checks.Scheduler
	wake     chan struct{}
}

// stagedVIP is a changed VIP waiting for its new checks' verdicts.
type stagedVIP struct {
	cfg *config.VIPConfig
	// old is the definition still governing the VIP until the commit.
	old     *config.VIPConfig
	shadows map[string]*checks.Scheduler // by check name
	// seeds collects the verdict of each shadow as it arrives.
	seeds map[string]policy.CheckSeed
	// removed are the running checks the new config no longer has.
	removed []string
}

// New returns a reloader for the config loaded from path. raw is the file
// as read at startup.
func New(path string, cfg *config.Config, raw []byte, pol Policy, reg *checks.Registry, m *metrics.Metrics, bgpMgr *bgp.Manager, onChange func()) *Reloader {
	r := &Reloader{
		path:       path,
		pol:        pol,
		reg:        reg,
		m:          m,
		bgp:        bgpMgr,
		onChange:   onChange,
		newChecker: checks.NewChecker,
		cur:        cfg,
		curHash:    sha256.Sum256(raw),
		loadedAt:   time.Now(),
		generation: 1,
		staging:    make(map[string]*stagedVIP),
		wake:       make(chan struct{}, 1),
	}
	if onChange == nil {
		r.onChange = func() {}
	}
	m.ConfigLoadedTimestamp.Set(float64(r.loadedAt.Unix()))
	return r
}

// LockLogLevel makes reloads ignore logging.level, for `run --debug`.
func (r *Reloader) LockLogLevel() { r.lockLogLevel = true }

// Run drives shadow promotions and polls the file for edits until ctx ends.
func (r *Reloader) Run(ctx context.Context, pollInterval time.Duration) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.wake:
			r.commitReady(ctx)
		case <-ticker.C:
			r.pollFile()
		}
	}
}

// Status describes the running config and the last reload.
func (r *Reloader) Status() model.ConfigStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := model.ConfigStatus{
		Path:        r.path,
		LoadedAt:    r.loadedAt,
		Generation:  r.generation,
		FileChanged: r.fileChanged,
		PendingVIPs: r.pendingLocked(),
	}
	if r.last != nil {
		cp := *r.last
		st.LastReload = &cp
	}
	return st
}

// Config returns the running config.
func (r *Reloader) Config() *config.Config {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cur
}

// Reload reads the file and applies the difference to the running daemon.
// It is safe to call concurrently; attempts are serialized.
func (r *Reloader) Reload(ctx context.Context) model.ReloadStatus {
	r.mu.Lock()
	res := r.reloadLocked(ctx)
	r.recordLocked(&res)
	r.mu.Unlock()
	r.onChange()
	return res
}

func (r *Reloader) reloadLocked(ctx context.Context) model.ReloadStatus {
	res := model.ReloadStatus{At: time.Now()}
	cfg, raw, err := config.LoadRaw(r.path)
	if err != nil {
		res.Outcome = model.ReloadRejected
		res.Error = err.Error()
		return res
	}
	hash := sha256.Sum256(raw)
	if hash == r.curHash {
		// Pending stagings keep going; nothing new to diff.
		res.Outcome = model.ReloadUnchanged
		res.Pending = r.pendingLocked()
		r.fileChanged = false
		return res
	}
	if fields := config.RestartRequired(r.cur, cfg); len(fields) > 0 {
		res.Outcome = model.ReloadRejected
		res.Error = "restart required: " + strings.Join(fields, ", ") + " changed"
		r.fileChanged = true
		return res
	}

	// Diff against what governs the routes now: a VIP still waiting for
	// its shadow verdict is governed by its previous definition.
	diff := config.DiffVIPs(r.effectiveVIPsLocked(), cfg.VIPs)

	// Build everything that can fail before touching the running state.
	added := make(map[string][]*checks.Scheduler, len(diff.Added))
	for _, v := range diff.Added {
		scheds, err := r.buildSchedulers(v, v.CheckList())
		if err != nil {
			res.Outcome = model.ReloadRejected
			res.Error = err.Error()
			r.fileChanged = true
			return res
		}
		added[v.Name] = scheds
	}
	staged := make(map[string]*stagedVIP, len(diff.Changed))
	for _, ch := range diff.Changed {
		if len(ch.ChecksChanged) == 0 {
			continue
		}
		scheds, err := r.buildSchedulers(ch.New, ch.ChecksChanged)
		if err != nil {
			res.Outcome = model.ReloadRejected
			res.Error = err.Error()
			r.fileChanged = true
			return res
		}
		st := &stagedVIP{cfg: ch.New, old: ch.Old, shadows: make(map[string]*checks.Scheduler, len(scheds)), seeds: make(map[string]policy.CheckSeed)}
		for _, s := range scheds {
			st.shadows[s.CheckName()] = s
		}
		for _, c := range ch.ChecksRemoved {
			st.removed = append(st.removed, c.EffectiveName())
		}
		staged[ch.New.Name] = st
	}

	// Apply. Earlier staging is superseded: its shadows stop; the diff
	// above was computed against the definitions they never replaced.
	r.dropStagingLocked()
	for _, v := range diff.Removed {
		r.reg.RemoveVIP(v.Name)
		if err := r.pol.RemoveVIP(ctx, v.Name); err != nil {
			res.Errors = append(res.Errors, err.Error())
			continue
		}
		res.Removed = append(res.Removed, v.Name)
	}
	for _, v := range diff.Added {
		r.pol.AddVIP(v)
		for _, s := range added[v.Name] {
			s.SetCallbacks(r.pol.OnHealthTransition, r.pol.OnCheckResult)
			r.reg.Add(s)
		}
		res.Added = append(res.Added, v.Name)
	}
	for _, ch := range diff.Changed {
		res.Changed = append(res.Changed, ch.New.Name)
		st, pending := staged[ch.New.Name]
		if !pending {
			if err := r.pol.CommitVIP(ctx, ch.New, nil); err != nil {
				res.Errors = append(res.Errors, err.Error())
			}
			continue
		}
		r.staging[ch.New.Name] = st
		res.Pending = append(res.Pending, ch.New.Name)
		for _, s := range st.shadows {
			r.watchShadow(s, ch.New.Name)
			r.reg.AddShadow(s)
		}
	}
	if !r.lockLogLevel && cfg.Logging.Level != r.cur.Logging.Level {
		if err := logging.SetLevel(cfg.Logging.Level); err != nil {
			res.Errors = append(res.Errors, "logging.level: "+err.Error())
		} else if r.bgp != nil {
			r.bgp.SetLogLevel(cfg.Logging.Level)
		}
	}

	r.cur = cfg
	r.curHash = hash
	r.loadedAt = res.At
	r.generation++
	r.fileChanged = false
	r.m.ConfigLoadedTimestamp.Set(float64(r.loadedAt.Unix()))
	if len(res.Errors) > 0 {
		res.Outcome = model.ReloadFailed
	} else {
		res.Outcome = model.ReloadApplied
	}
	return res
}

// recordLocked stores the outcome, updates the metrics and logs it.
func (r *Reloader) recordLocked(res *model.ReloadStatus) {
	cp := *res
	r.last = &cp
	r.m.ConfigReloadTotal.WithLabelValues(res.Outcome).Inc()
	r.m.ConfigLastReloadTimestamp.Set(float64(res.At.Unix()))
	if res.OK() {
		r.m.ConfigLastReloadSuccess.Set(1)
	} else {
		r.m.ConfigLastReloadSuccess.Set(0)
	}
	r.m.ConfigFileChanged.Set(boolGauge(r.fileChanged))
	r.m.ConfigReloadPendingVIPs.Set(float64(len(r.staging)))

	attrs := []any{"outcome", res.Outcome, "generation", r.generation}
	if len(res.Added) > 0 {
		attrs = append(attrs, "added", res.Added)
	}
	if len(res.Removed) > 0 {
		attrs = append(attrs, "removed", res.Removed)
	}
	if len(res.Changed) > 0 {
		attrs = append(attrs, "changed", res.Changed)
	}
	if len(res.Pending) > 0 {
		attrs = append(attrs, "pending_verdict", res.Pending)
	}
	switch res.Outcome {
	case model.ReloadRejected:
		slog.Error("config reload rejected, running config unchanged", append(attrs, "error", res.Error)...)
	case model.ReloadFailed:
		slog.Error("config reload applied with errors", append(attrs, "errors", res.Errors)...)
	case model.ReloadUnchanged:
		slog.Info("config reload: file unchanged", attrs...)
	default:
		slog.Info("config reloaded", attrs...)
	}
}

func (r *Reloader) buildSchedulers(v *config.VIPConfig, list []*config.CheckConfig) ([]*checks.Scheduler, error) {
	return NewSchedulers(v, list, r.m, r.newChecker)
}

// NewSchedulers constructs the schedulers for the given checks of a VIP,
// wrapped in the local address guard when the VIP asks for it. Nothing
// runs yet. The daemon builds its startup schedulers with it too, so a
// reloaded check behaves exactly like one configured at start.
func NewSchedulers(v *config.VIPConfig, list []*config.CheckConfig, m *metrics.Metrics, newChecker func(*config.CheckConfig) (checks.Checker, error)) ([]*checks.Scheduler, error) {
	var guard *checks.LocalAddressGuard
	if v.RequireLocalAddress {
		gauge := m.VIPLocalAddress.WithLabelValues(v.Name, v.Prefix)
		g, err := checks.NewLocalAddressGuard(v.Name, v.Prefix, v.LocalInterface, func(assigned bool) {
			if assigned {
				gauge.Set(1)
			} else {
				gauge.Set(0)
			}
		})
		if err != nil {
			return nil, fmt.Errorf("creating local address guard for VIP %q: %w", v.Name, err)
		}
		guard = g
	}
	out := make([]*checks.Scheduler, 0, len(list))
	for _, c := range list {
		checker, err := newChecker(c)
		if err != nil {
			return nil, fmt.Errorf("creating checker %q for VIP %q: %w", c.EffectiveName(), v.Name, err)
		}
		if guard != nil {
			checker = guard.Wrap(checker)
		}
		interval, timeout := time.Second, 100*time.Millisecond
		if c.Interval != nil {
			interval = c.Interval.Duration
		}
		if c.Timeout != nil {
			timeout = c.Timeout.Duration
		}
		rise, fall := 1, 3
		if c.Rise != nil {
			rise = *c.Rise
		}
		if c.Fall != nil {
			fall = *c.Fall
		}
		out = append(out, checks.NewScheduler(checks.SchedulerConfig{
			VIPName:   v.Name,
			CheckName: c.EffectiveName(),
			Checker:   checker,
			Interval:  interval,
			Timeout:   timeout,
			Rise:      rise,
			Fall:      fall,
		}))
	}
	return out, nil
}

// watchShadow makes a shadow scheduler report itself once it reached a
// verdict. Its results do not reach the policy manager.
func (r *Reloader) watchShadow(s *checks.Scheduler, vip string) {
	rise, fall := schedulerThresholds(s)
	s.SetCallbacks(nil, func(_, _ string, _ checks.Result) {
		if s.ConsecutiveOK() < rise && s.ConsecutiveFail() < fall {
			return
		}
		r.vmu.Lock()
		if !slices.Contains(r.verdicts, s) {
			r.verdicts = append(r.verdicts, s)
		}
		r.vmu.Unlock()
		select {
		case r.wake <- struct{}{}:
		default:
		}
	})
}

// commitReady promotes every staged VIP whose shadows all have a verdict.
func (r *Reloader) commitReady(ctx context.Context) {
	r.vmu.Lock()
	ready := r.verdicts
	r.verdicts = nil
	r.vmu.Unlock()

	r.mu.Lock()
	changed := false
	for _, s := range ready {
		st, ok := r.staging[s.VIPName()]
		if !ok || st.shadows[s.CheckName()] != s {
			continue // superseded by a later reload
		}
		st.seeds[s.CheckName()] = policy.CheckSeed{
			Health:     healthOf(s),
			LastResult: s.LastResult(),
			LastTime:   time.Now(),
		}
		if len(st.seeds) < len(st.shadows) {
			continue
		}
		delete(r.staging, s.VIPName())
		for _, name := range st.removed {
			if old := r.findVisible(s.VIPName(), name); old != nil {
				r.reg.Remove(old)
			}
		}
		for _, sh := range st.shadows {
			sh.SetCallbacks(r.pol.OnHealthTransition, r.pol.OnCheckResult)
			r.reg.Promote(sh)
		}
		if err := r.pol.CommitVIP(ctx, st.cfg, st.seeds); err != nil {
			slog.Error("config reload: committing VIP failed", "vip", st.cfg.Name, "error", err)
			if r.last != nil {
				r.last.Outcome = model.ReloadFailed
				r.last.Errors = append(r.last.Errors, err.Error())
				r.m.ConfigLastReloadSuccess.Set(0)
			}
		} else {
			slog.Info("config reload: VIP switched to new definition", "vip", st.cfg.Name, "checks", slices.Sorted(mapsKeys(st.shadows)))
		}
		if r.last != nil {
			r.last.Pending = slices.DeleteFunc(r.last.Pending, func(n string) bool { return n == st.cfg.Name })
		}
		changed = true
	}
	r.m.ConfigReloadPendingVIPs.Set(float64(len(r.staging)))
	r.mu.Unlock()
	if changed {
		r.onChange()
	}
}

// dropStagingLocked stops every shadow scheduler of a superseded staging.
func (r *Reloader) dropStagingLocked() {
	for name, st := range r.staging {
		for _, s := range st.shadows {
			r.reg.Remove(s)
		}
		delete(r.staging, name)
	}
	r.vmu.Lock()
	r.verdicts = nil
	r.vmu.Unlock()
}

// effectiveVIPsLocked is the VIP list that governs the routes: the running
// config with every staged VIP replaced by its still-committed definition.
func (r *Reloader) effectiveVIPsLocked() []config.VIPConfig {
	if len(r.staging) == 0 {
		return r.cur.VIPs
	}
	out := slices.Clone(r.cur.VIPs)
	for i := range out {
		if st, ok := r.staging[out[i].Name]; ok {
			out[i] = *st.old
		}
	}
	return out
}

func (r *Reloader) findVisible(vip, check string) *checks.Scheduler {
	for _, s := range r.reg.Schedulers()[vip] {
		if s.CheckName() == check {
			return s
		}
	}
	return nil
}

func (r *Reloader) pendingLocked() []string {
	if len(r.staging) == 0 {
		return nil
	}
	return slices.Sorted(mapsKeys(r.staging))
}

// pollFile compares the file on disk with the running config and flips the
// file-changed flag (metric, status, systemd line) when they differ.
func (r *Reloader) pollFile() {
	raw, err := os.ReadFile(r.path)
	changed := false
	if err != nil {
		// A missing or unreadable file cannot be what is running.
		changed = true
	} else {
		r.mu.Lock()
		changed = sha256.Sum256(raw) != r.curHash
		r.mu.Unlock()
	}
	r.mu.Lock()
	flipped := changed != r.fileChanged
	r.fileChanged = changed
	r.m.ConfigFileChanged.Set(boolGauge(changed))
	r.mu.Unlock()
	if flipped {
		if changed {
			if err != nil {
				slog.Warn("config file unreadable, running config kept", "path", r.path, "error", err)
			} else {
				slog.Warn("config file changed on disk, reload to apply", "path", r.path)
			}
		} else {
			slog.Info("config file matches running config again", "path", r.path)
		}
		r.onChange()
	}
}

func healthOf(s *checks.Scheduler) model.HealthStatus {
	if s.IsHealthy() {
		return model.HealthHealthy
	}
	return model.HealthUnhealthy
}

func boolGauge(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func mapsKeys[M ~map[string]V, V any](m M) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// schedulerThresholds reports the rise and fall of a scheduler.
func schedulerThresholds(s *checks.Scheduler) (rise, fall int) {
	return s.Rise(), s.Fall()
}
