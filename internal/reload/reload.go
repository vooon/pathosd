// Package reload applies a changed config file to a running daemon.
//
// Only the vips list and logging.level reload live; a change to any other
// section rejects the whole reload, so the daemon either runs exactly what
// the file says or reports why not. Removed VIPs are withdrawn, added VIPs
// start withdrawn and prove health through rise, and a VIP whose checks
// changed keeps running its old checks until every new one has reached a
// verdict in shadow (make-before-break): a healthy target never withdraws
// for a check tweak, an unhealthy one withdraws as soon as the new check
// says so. The wait is bounded: a new check that reaches no verdict in time
// is committed as unhealthy, like a check that never rose at startup.
package reload

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"maps"
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
	// verdicts marks the shadows that reached rise or fall.
	verdicts map[string]bool
	// removed are the running checks the new config no longer has.
	removed []string
	// deadline is when the VIP is committed whatever the shadows say.
	deadline time.Time
}

// stagingGraceFactor bounds how long a changed VIP waits for its shadows,
// as a multiple of the longest window a shadow needs for a verdict
// (max(rise, fall) results, one per interval). A target that alternates
// pass and fail never reaches either threshold; without the bound the VIP
// would stay on its old definition forever.
const stagingGraceFactor = 4

func stagingDeadline(now time.Time, scheds []*checks.Scheduler) time.Time {
	var window time.Duration
	for _, s := range scheds {
		window = max(window, time.Duration(max(s.Rise(), s.Fall()))*s.Interval())
	}
	return now.Add(stagingGraceFactor * window)
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
			r.expireStaged(ctx)
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
	// What was built before a rejection is never started: close it.
	added := make(map[string][]*checks.Scheduler, len(diff.Added))
	staged := make(map[string]*stagedVIP, len(diff.Changed))
	reject := func(err error) model.ReloadStatus {
		for _, scheds := range added {
			closeSchedulers(scheds)
		}
		for _, st := range staged {
			closeSchedulers(slices.Collect(maps.Values(st.shadows)))
		}
		res.Outcome = model.ReloadRejected
		res.Error = err.Error()
		r.fileChanged = true
		return res
	}
	for _, v := range diff.Added {
		scheds, err := r.buildSchedulers(v, v.CheckList())
		if err != nil {
			return reject(err)
		}
		added[v.Name] = scheds
	}
	for _, ch := range diff.Changed {
		if len(ch.ChecksChanged) == 0 {
			continue
		}
		scheds, err := r.buildSchedulers(ch.New, ch.ChecksChanged)
		if err != nil {
			return reject(err)
		}
		st := &stagedVIP{
			cfg:      ch.New,
			old:      ch.Old,
			shadows:  make(map[string]*checks.Scheduler, len(scheds)),
			verdicts: make(map[string]bool, len(scheds)),
			deadline: stagingDeadline(res.At, scheds),
		}
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
	// failed are the VIPs whose route could not be withdrawn. Their old
	// definition keeps governing and the next reload retries them.
	failed := make(map[string]*config.VIPConfig)
	for _, v := range diff.Removed {
		running := r.reg.Schedulers()[v.Name]
		r.reg.RemoveVIP(v.Name)
		if err := r.pol.RemoveVIP(ctx, v.Name); err != nil {
			// The route is still announced: put its checks back so a
			// dead backend is still withdrawn while the removal waits.
			for _, s := range running {
				r.reg.Add(s)
			}
			failed[v.Name] = v
			res.Errors = append(res.Errors, err.Error())
			continue
		}
		closeSchedulers(running)
		res.Removed = append(res.Removed, v.Name)
	}
	for _, v := range diff.Added {
		if _, kept := failed[v.Name]; kept {
			// Prefix change whose old route is still up: the old
			// definition stays until the retry removes it.
			closeSchedulers(added[v.Name])
			continue
		}
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
			// Nothing to prove in shadow: only checks were dropped or
			// the policy changed. Stop the dropped checks before the
			// policy forgets them, or they keep reporting.
			for _, c := range ch.ChecksRemoved {
				if old := r.findVisible(ch.New.Name, c.EffectiveName()); old != nil {
					r.reg.Remove(old)
					closeSchedulers([]*checks.Scheduler{old})
				}
			}
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

	// What runs is the file, except for the VIPs whose removal failed:
	// keep their old definition in the running config and forget the file
	// hash, so a reload of the same file diffs again and retries them.
	for name, old := range failed {
		if idx := slices.IndexFunc(cfg.VIPs, func(v config.VIPConfig) bool { return v.Name == name }); idx >= 0 {
			cfg.VIPs[idx] = *old
		} else {
			cfg.VIPs = append(cfg.VIPs, *old)
		}
	}
	r.cur = cfg
	r.curHash = hash
	r.fileChanged = false
	if len(failed) > 0 {
		r.curHash = [32]byte{}
		r.fileChanged = true
	}
	r.loadedAt = res.At
	r.generation++
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
		st.verdicts[s.CheckName()] = true
		if len(st.verdicts) < len(st.shadows) {
			continue
		}
		r.commitLocked(ctx, st)
		changed = true
	}
	r.m.ConfigReloadPendingVIPs.Set(float64(len(r.staging)))
	r.mu.Unlock()
	if changed {
		r.onChange()
	}
}

// expireStaged commits every staged VIP past its deadline. A shadow still
// without a verdict is unhealthy at that point (it never reached rise), so
// the VIP switches as if the check had never risen at startup.
func (r *Reloader) expireStaged(ctx context.Context) {
	now := time.Now()
	r.mu.Lock()
	changed := false
	for _, st := range r.staging {
		if now.Before(st.deadline) {
			continue
		}
		var undecided []string
		for name := range st.shadows {
			if !st.verdicts[name] {
				undecided = append(undecided, name)
			}
		}
		slices.Sort(undecided)
		slog.Warn("config reload: new checks reached no verdict in time, VIP switched with them unhealthy",
			"vip", st.cfg.Name, "checks", undecided, "deadline", st.deadline.Format(time.RFC3339))
		r.commitLocked(ctx, st)
		changed = true
	}
	if changed {
		r.m.ConfigReloadPendingVIPs.Set(float64(len(r.staging)))
	}
	r.mu.Unlock()
	if changed {
		r.onChange()
	}
}

// commitLocked switches a staged VIP to its new definition. Every seed is
// the shadow's state now, not when its verdict arrived, and the policy
// callbacks are attached only after the commit; a flip in between is
// caught by a final compare, so no transition is lost or misapplied.
func (r *Reloader) commitLocked(ctx context.Context, st *stagedVIP) {
	delete(r.staging, st.cfg.Name)
	for _, name := range st.removed {
		if old := r.findVisible(st.cfg.Name, name); old != nil {
			r.reg.Remove(old)
			closeSchedulers([]*checks.Scheduler{old})
		}
	}
	for _, sh := range st.shadows {
		if old := r.reg.Promote(sh); old != nil {
			closeSchedulers([]*checks.Scheduler{old})
		}
	}
	seeds := make(map[string]policy.CheckSeed, len(st.shadows))
	for name, sh := range st.shadows {
		seeds[name] = policy.CheckSeed{Health: healthOf(sh), LastResult: sh.LastResult(), LastTime: time.Now()}
	}
	err := r.pol.CommitVIP(ctx, st.cfg, seeds)
	for name, sh := range st.shadows {
		sh.SetCallbacks(r.pol.OnHealthTransition, r.pol.OnCheckResult)
		if h := healthOf(sh); err == nil && h != seeds[name].Health {
			r.pol.OnHealthTransition(checks.HealthTransition{
				VIPName:     st.cfg.Name,
				CheckName:   name,
				Healthy:     h == model.HealthHealthy,
				Reason:      "verdict changed while the reload committed",
				CheckResult: sh.LastResult(),
			})
		}
	}
	if err != nil {
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
}

// dropStagingLocked stops every shadow scheduler of a superseded staging.
func (r *Reloader) dropStagingLocked() {
	for name, st := range r.staging {
		for _, s := range st.shadows {
			r.reg.Remove(s)
			closeSchedulers([]*checks.Scheduler{s})
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

// closeSchedulers releases the checkers of schedulers that will not run
// again (replaced, removed, superseded or never started).
func closeSchedulers(scheds []*checks.Scheduler) {
	for _, s := range scheds {
		if err := s.Close(); err != nil {
			slog.Warn("closing checker", "vip", s.VIPName(), "check", s.CheckName(), "error", err)
		}
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
