package reload

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vooon/pathosd/internal/checks"
	"github.com/vooon/pathosd/internal/config"
	"github.com/vooon/pathosd/internal/metrics"
	"github.com/vooon/pathosd/internal/model"
	"github.com/vooon/pathosd/internal/policy"
)

// notifier records BGP operations by prefix.
type notifier struct {
	mu           sync.Mutex
	announces    []string
	withdraws    []string
	prepends     []int
	failWithdraw bool // when set, WithdrawVIP fails without recording
}

func (n *notifier) AnnounceVIP(_ context.Context, prefix string, attrs model.RouteAttrs) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.announces = append(n.announces, prefix)
	n.prepends = append(n.prepends, attrs.ASPathPrepend)
	return nil
}

func (n *notifier) WithdrawVIP(_ context.Context, prefix string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.failWithdraw {
		return assert.AnError
	}
	n.withdraws = append(n.withdraws, prefix)
	return nil
}

func (n *notifier) setFailWithdraw(v bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.failWithdraw = v
}

func (n *notifier) PessimizeVIP(ctx context.Context, prefix string, attrs model.RouteAttrs) error {
	return n.AnnounceVIP(ctx, prefix, attrs)
}

func (n *notifier) snapshot() (announces, withdraws []string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.announces...), append([]string(nil), n.withdraws...)
}

// portChecker returns a fixed result selected by the tcp port in the config:
// port 1 fails, anything else passes.
type portChecker struct{ port uint16 }

func (p portChecker) Type() string { return "tcp" }
func (p portChecker) Check(context.Context) checks.Result {
	if p.port == 1 {
		return checks.Result{Detail: "port 1 refused"}
	}
	return checks.Result{Success: true, Detail: "ok"}
}

// closeChecker counts Close calls on the harness so a test can verify that
// discarded checkers release their resources.
type closeChecker struct {
	checks.Checker
	closes *atomic.Int32
}

func (c closeChecker) Close() error {
	c.closes.Add(1)
	return nil
}

// ctrlChecker is steered by the test: pass, fail, or alternate (which never
// reaches rise or fall with both >= 2).
type ctrlChecker struct {
	mode *atomic.Int32 // 0 fail, 1 pass, 2 alternate
	n    atomic.Int32
}

const (
	ctrlFail int32 = iota
	ctrlPass
	ctrlFlap
)

func (c *ctrlChecker) Type() string { return "tcp" }
func (c *ctrlChecker) Check(context.Context) checks.Result {
	n := c.n.Add(1)
	switch c.mode.Load() {
	case ctrlPass:
		return checks.Result{Success: true, Detail: "ok"}
	case ctrlFlap:
		return checks.Result{Success: n%2 == 0, Detail: "flap"}
	}
	return checks.Result{Detail: "refused"}
}

const header = `schema: v1
router: {asn: 65001, router_id: 192.0.2.1, local_address: 192.0.2.1}
api: {listen: "127.0.0.1:59179"}
bgp: {neighbors: [{name: p, address: 192.0.2.2, peer_asn: 65002}]}
`

func vip(name, prefix string, port int, extra string) string {
	return "  - name: " + name + "\n    prefix: " + prefix + "\n" + extra +
		"    check: {type: tcp, interval: 20ms, timeout: 10ms, rise: 2, fall: 2, tcp: {host: 127.0.0.1, port: " + itoa(port) + "}}\n" +
		"    policy: {fail_action: withdraw}\n"
}

// vipChecks is a VIP with a named tcp check per port (c<port>).
func vipChecks(name, prefix string, ports ...int) string {
	s := "  - name: " + name + "\n    prefix: " + prefix + "\n    checks:\n"
	for _, p := range ports {
		s += "      - {name: c" + itoa(p) + ", type: tcp, interval: 20ms, timeout: 10ms, rise: 2, fall: 2, tcp: {host: 127.0.0.1, port: " + itoa(p) + "}}\n"
	}
	return s + "    policy: {fail_action: withdraw}\n"
}

func itoa(i int) string { return string(rune('0'+i/10)) + string(rune('0'+i%10)) }

type harness struct {
	t    *testing.T
	path string
	n    *notifier
	m    *metrics.Metrics
	pol  *policy.Manager
	reg  *checks.Registry
	r    *Reloader
	stop context.CancelFunc
	wake int
	wmu  sync.Mutex
	// ctrl steers the checkers of ports 2-9 (see ctrlChecker); nil = fail.
	ctrl map[uint16]*atomic.Int32
	// built and closed count every checker the harness made and closed.
	built, closed atomic.Int32
}

func newHarness(t *testing.T, body string) *harness {
	t.Helper()
	return newHarnessOpts(t, body, true)
}

// newHarnessOpts builds the harness; with run false the reloader's loop is
// not started, so the test drives commits by calling commitReady itself.
func newHarnessOpts(t *testing.T, body string, run bool) *harness {
	t.Helper()
	h := &harness{t: t, path: filepath.Join(t.TempDir(), "pathosd.yaml"), n: &notifier{}, m: metrics.New([]float64{0.1}),
		ctrl: make(map[uint16]*atomic.Int32)}
	for p := uint16(2); p < 10; p++ {
		h.ctrl[p] = new(atomic.Int32)
	}
	h.write(body)
	cfg, raw, err := config.LoadRaw(h.path)
	require.NoError(t, err)
	h.pol = policy.NewManager(cfg.VIPs, h.m, h.n)
	h.reg = checks.NewRegistry()
	h.r = New(h.path, cfg, raw, h.pol, h.reg, h.m, nil, func() {
		h.wmu.Lock()
		h.wake++
		h.wmu.Unlock()
	})
	h.r.newChecker = func(c *config.CheckConfig) (checks.Checker, error) {
		if c.TCP.Port == 99 {
			return nil, assert.AnError
		}
		h.built.Add(1)
		if mode, ok := h.ctrl[c.TCP.Port]; ok {
			return closeChecker{Checker: &ctrlChecker{mode: mode}, closes: &h.closed}, nil
		}
		return closeChecker{Checker: portChecker{port: c.TCP.Port}, closes: &h.closed}, nil
	}
	for i := range cfg.VIPs {
		scheds, err := h.r.buildSchedulers(&cfg.VIPs[i], cfg.VIPs[i].CheckList())
		require.NoError(t, err)
		for _, s := range scheds {
			s.SetCallbacks(h.pol.OnHealthTransition, h.pol.OnCheckResult)
			h.reg.Add(s)
		}
	}
	h.reg.Start()
	ctx, cancel := context.WithCancel(context.Background())
	h.stop = cancel
	if run {
		go h.r.Run(ctx, 10*time.Millisecond)
	}
	t.Cleanup(func() {
		cancel()
		require.NoError(t, h.reg.Stop(context.Background()))
	})
	return h
}

func (h *harness) write(body string) {
	h.t.Helper()
	require.NoError(h.t, os.WriteFile(h.path, []byte(header+"vips:\n"+body), 0o600))
}

func (h *harness) state(name string) model.VIPState {
	for _, st := range h.pol.GetVIPStatuses() {
		if st.Name == name {
			return st.State
		}
	}
	return -1
}

func (h *harness) waitState(name string, want model.VIPState) {
	h.t.Helper()
	require.Eventually(h.t, func() bool { return h.state(name) == want }, 2*time.Second, 5*time.Millisecond,
		"vip %s should reach %s, is %v", name, want, h.state(name))
}

// shadow returns a staged VIP's shadow scheduler by check name.
func (h *harness) shadow(vip, check string) *checks.Scheduler {
	h.r.mu.Lock()
	defer h.r.mu.Unlock()
	if st, ok := h.r.staging[vip]; ok {
		return st.shadows[check]
	}
	return nil
}

func (h *harness) waitShadowHealth(vip, check string, healthy bool) {
	h.t.Helper()
	require.Eventually(h.t, func() bool {
		s := h.shadow(vip, check)
		return s != nil && s.IsHealthy() == healthy
	}, 2*time.Second, 5*time.Millisecond)
}

// waitVerdict waits until a shadow's verdict is queued for the commit loop.
func (h *harness) waitVerdict(vip, check string) {
	h.t.Helper()
	require.Eventually(h.t, func() bool {
		s := h.shadow(vip, check)
		h.r.vmu.Lock()
		defer h.r.vmu.Unlock()
		return s != nil && slices.Contains(h.r.verdicts, s)
	}, 2*time.Second, 5*time.Millisecond)
}

func (h *harness) waitPending(n int) {
	h.t.Helper()
	require.Eventually(h.t, func() bool { return len(h.r.Status().PendingVIPs) == n }, 2*time.Second, 5*time.Millisecond)
}

func TestReload_UnchangedAndRejected(t *testing.T) {
	h := newHarness(t, vip("web", "10.9.0.1/32", 80, ""))
	h.waitState("web", model.StateAnnounced)

	res := h.r.Reload(context.Background())
	assert.Equal(t, model.ReloadUnchanged, res.Outcome)
	assert.Equal(t, 1, h.r.Status().Generation)

	// Invalid file: rejected, nothing touched.
	require.NoError(t, os.WriteFile(h.path, []byte("schema: v1\nvips: []\n"), 0o600))
	res = h.r.Reload(context.Background())
	assert.Equal(t, model.ReloadRejected, res.Outcome)
	assert.Contains(t, res.Error, "validation failed")
	assert.Equal(t, model.StateAnnounced, h.state("web"))
	assert.Equal(t, float64(0), testutil.ToFloat64(h.m.ConfigLastReloadSuccess))

	// Restart-only section: rejected with the section named.
	require.NoError(t, os.WriteFile(h.path, []byte(header+"logging: {format: json}\nvips:\n"+vip("web", "10.9.0.1/32", 80, "")), 0o600))
	res = h.r.Reload(context.Background())
	assert.Equal(t, model.ReloadRejected, res.Outcome)
	assert.Contains(t, res.Error, "restart required: logging (other than level) changed")
	assert.True(t, h.r.Status().FileChanged)
	assert.Equal(t, float64(1), testutil.ToFloat64(h.m.ConfigFileChanged))

	// Checker construction failure: rejected before anything runs.
	h.write(vip("web", "10.9.0.1/32", 80, "") + vip("bad", "10.9.0.2/32", 99, ""))
	res = h.r.Reload(context.Background())
	assert.Equal(t, model.ReloadRejected, res.Outcome)
	assert.Contains(t, res.Error, `creating checker "tcp" for VIP "bad"`)
	assert.Len(t, h.reg.Schedulers(), 1)

	// Back to the running content: the flag clears.
	h.write(vip("web", "10.9.0.1/32", 80, ""))
	res = h.r.Reload(context.Background())
	assert.Equal(t, model.ReloadUnchanged, res.Outcome)
	assert.False(t, h.r.Status().FileChanged)
	assert.Equal(t, float64(1), testutil.ToFloat64(h.m.ConfigLastReloadSuccess))
	assert.Equal(t, float64(3), testutil.ToFloat64(h.m.ConfigReloadTotal.WithLabelValues(model.ReloadRejected)))
}

func TestReload_AddRemoveAndPolicy(t *testing.T) {
	h := newHarness(t, vip("web", "10.9.0.1/32", 80, ""))
	h.waitState("web", model.StateAnnounced)

	h.write(vip("web", "10.9.0.1/32", 80, "") + vip("api", "10.9.0.2/32", 81, ""))
	res := h.r.Reload(context.Background())
	assert.Equal(t, model.ReloadApplied, res.Outcome)
	assert.Equal(t, []string{"api"}, res.Added)
	assert.Equal(t, 2, h.r.Status().Generation)
	h.waitState("api", model.StateAnnounced)
	assert.Len(t, h.reg.Schedulers(), 2)

	// Policy-only change: new attributes, no withdraw.
	h.write(vip("web", "10.9.0.1/32", 80, "") + "  - name: api\n    prefix: 10.9.0.2/32\n    check: {type: tcp, interval: 20ms, timeout: 10ms, rise: 2, fall: 2, tcp: {host: 127.0.0.1, port: 81}}\n    policy: {fail_action: withdraw, announce: {as_path_prepend: 3}}\n")
	res = h.r.Reload(context.Background())
	assert.Equal(t, model.ReloadApplied, res.Outcome)
	assert.Equal(t, []string{"api"}, res.Changed)
	assert.Empty(t, res.Pending)
	announces, withdraws := h.n.snapshot()
	assert.Equal(t, []string{"10.9.0.1/32", "10.9.0.2/32", "10.9.0.2/32"}, announces)
	assert.Equal(t, 3, h.n.prepends[2])
	assert.Empty(t, withdraws)

	// Removal withdraws and forgets.
	h.write(vip("web", "10.9.0.1/32", 80, ""))
	res = h.r.Reload(context.Background())
	assert.Equal(t, model.ReloadApplied, res.Outcome)
	assert.Equal(t, []string{"api"}, res.Removed)
	_, withdraws = h.n.snapshot()
	assert.Equal(t, []string{"10.9.0.2/32"}, withdraws)
	assert.Equal(t, []string{"web"}, h.pol.VIPNames())
	assert.Len(t, h.reg.Schedulers(), 1)
}

// A reload that only drops a check from a VIP commits at once and must stop
// the dropped check's scheduler; left running it would keep reporting into
// the policy under a check name it no longer knows.
func TestReload_RemovedCheckStopped(t *testing.T) {
	h := newHarness(t, vipChecks("web", "10.9.0.1/32", 80, 81))
	h.waitState("web", model.StateAnnounced)
	require.Len(t, h.reg.Schedulers()["web"], 2)

	h.write(vipChecks("web", "10.9.0.1/32", 80))
	res := h.r.Reload(context.Background())
	assert.Equal(t, model.ReloadApplied, res.Outcome)
	assert.Equal(t, []string{"web"}, res.Changed)
	assert.Empty(t, res.Pending)
	scheds := h.reg.Schedulers()["web"]
	require.Len(t, scheds, 1)
	assert.Equal(t, "c80", scheds[0].CheckName())
	assert.Equal(t, model.StateAnnounced, h.state("web"))
}

// A VIP whose route cannot be withdrawn stays, with its checks running, and
// the next reload of the same file retries the removal.
func TestReload_RemoveVIPWithdrawFails(t *testing.T) {
	h := newHarness(t, vip("web", "10.9.0.1/32", 80, "")+vip("api", "10.9.0.2/32", 81, ""))
	h.waitState("web", model.StateAnnounced)
	h.waitState("api", model.StateAnnounced)

	h.n.setFailWithdraw(true)
	h.write(vip("web", "10.9.0.1/32", 80, ""))
	res := h.r.Reload(context.Background())
	assert.Equal(t, model.ReloadFailed, res.Outcome)
	assert.Len(t, res.Errors, 1)
	assert.Empty(t, res.Removed)
	assert.Equal(t, []string{"web", "api"}, h.pol.VIPNames())
	assert.Len(t, h.reg.Schedulers()["api"], 1, "checks of the kept VIP run again")
	assert.True(t, h.r.Status().FileChanged, "running config differs from the file")
	assert.Equal(t, model.StateAnnounced, h.state("api"))

	// Same file, withdraw works now: the removal is retried.
	h.n.setFailWithdraw(false)
	res = h.r.Reload(context.Background())
	assert.Equal(t, model.ReloadApplied, res.Outcome)
	assert.Equal(t, []string{"api"}, res.Removed)
	assert.Equal(t, []string{"web"}, h.pol.VIPNames())
	assert.Empty(t, h.reg.Schedulers()["api"])
	assert.False(t, h.r.Status().FileChanged)
	_, withdraws := h.n.snapshot()
	assert.Equal(t, []string{"10.9.0.2/32"}, withdraws)
}

// A prefix change is a removal plus an add under the same name. When the
// old route cannot be withdrawn the old definition stays whole: no second
// VIP, no schedulers for a prefix that is not announced.
func TestReload_PrefixChangeWithdrawFails(t *testing.T) {
	h := newHarness(t, vip("api", "10.9.0.2/32", 81, ""))
	h.waitState("api", model.StateAnnounced)

	h.n.setFailWithdraw(true)
	h.write(vip("api", "10.9.0.3/32", 81, ""))
	res := h.r.Reload(context.Background())
	assert.Equal(t, model.ReloadFailed, res.Outcome)
	assert.Empty(t, res.Added)
	assert.Equal(t, "10.9.0.2/32", h.pol.GetVIPStatuses()[0].Prefix)
	assert.Len(t, h.reg.Schedulers()["api"], 1)

	h.n.setFailWithdraw(false)
	res = h.r.Reload(context.Background())
	assert.Equal(t, model.ReloadApplied, res.Outcome)
	assert.Equal(t, []string{"api"}, res.Removed)
	assert.Equal(t, []string{"api"}, res.Added)
	h.waitState("api", model.StateAnnounced)
	assert.Equal(t, "10.9.0.3/32", h.pol.GetVIPStatuses()[0].Prefix)
	announces, withdraws := h.n.snapshot()
	assert.Equal(t, []string{"10.9.0.2/32"}, withdraws)
	assert.Equal(t, "10.9.0.3/32", announces[len(announces)-1])
}

func TestReload_MakeBeforeBreak(t *testing.T) {
	h := newHarness(t, vip("web", "10.9.0.1/32", 80, ""))
	h.waitState("web", model.StateAnnounced)
	old := h.reg.Schedulers()["web"][0]

	// Healthy target, tweaked check: promoted without a withdraw.
	h.write(vip("web", "10.9.0.1/32", 82, ""))
	res := h.r.Reload(context.Background())
	assert.Equal(t, model.ReloadApplied, res.Outcome)
	assert.Equal(t, []string{"web"}, res.Pending)
	assert.Equal(t, []string{"web"}, h.r.Status().PendingVIPs)
	h.waitPending(0)
	assert.NotSame(t, old, h.reg.Schedulers()["web"][0], "new scheduler promoted")
	_, withdraws := h.n.snapshot()
	assert.Empty(t, withdraws)
	assert.Equal(t, model.StateAnnounced, h.state("web"))
	assert.Empty(t, h.r.Status().LastReload.Pending)

	// New check fails: the old one governs until fall, then withdraw.
	h.write(vip("web", "10.9.0.1/32", 1, ""))
	res = h.r.Reload(context.Background())
	assert.Equal(t, model.ReloadApplied, res.Outcome)
	assert.Equal(t, model.StateAnnounced, h.state("web"), "old check still governs")
	h.waitState("web", model.StateWithdrawn)
	h.waitPending(0)
	_, withdraws = h.n.snapshot()
	assert.Equal(t, []string{"10.9.0.1/32"}, withdraws)

	// Back to a passing check: proves itself in shadow, then announces.
	h.write(vip("web", "10.9.0.1/32", 80, ""))
	assert.Equal(t, model.ReloadApplied, h.r.Reload(context.Background()).Outcome)
	h.waitState("web", model.StateAnnounced)
	h.waitPending(0)
}

// A new check whose target alternates pass and fail never reaches rise or
// fall. The staging must still end: the VIP switches to the new definition
// with that check unhealthy, like a check that never rose at startup.
func TestReload_StagingDeadline(t *testing.T) {
	h := newHarness(t, vip("web", "10.9.0.1/32", 80, ""))
	h.waitState("web", model.StateAnnounced)
	old := h.reg.Schedulers()["web"][0]

	h.ctrl[2].Store(ctrlFlap)
	h.write(vip("web", "10.9.0.1/32", 2, ""))
	res := h.r.Reload(context.Background())
	assert.Equal(t, model.ReloadApplied, res.Outcome)
	assert.Equal(t, []string{"web"}, res.Pending)

	// rise=fall=2, interval 20ms: the bound is 4 x 40ms.
	h.waitPending(0)
	assert.NotSame(t, old, h.reg.Schedulers()["web"][0], "new scheduler promoted")
	assert.Equal(t, model.StateWithdrawn, h.state("web"))
	_, withdraws := h.n.snapshot()
	assert.Equal(t, []string{"10.9.0.1/32"}, withdraws)
	assert.Empty(t, h.r.Status().LastReload.Pending)
	assert.Equal(t, model.ReloadApplied, h.r.Status().LastReload.Outcome)
}

// With two changed checks, the first verdict is drained before the second
// arrives. The commit must use each shadow's health at commit time: a check
// that rose and then fell in between is unhealthy, whatever its verdict was
// when first recorded.
func TestReload_CommitUsesCurrentShadowHealth(t *testing.T) {
	h := newHarnessOpts(t, vipChecks("web", "10.9.0.1/32", 80, 81), false)
	h.waitState("web", model.StateAnnounced)

	h.ctrl[3].Store(ctrlPass)
	h.ctrl[4].Store(ctrlFlap)
	h.write(vipChecks("web", "10.9.0.1/32", 3, 4))
	res := h.r.Reload(context.Background())
	assert.Equal(t, model.ReloadApplied, res.Outcome)
	assert.Equal(t, []string{"web"}, res.Pending)

	// c03 rises: its verdict is drained, c04 has none yet, so no commit.
	h.waitVerdict("web", "c03")
	h.r.commitReady(context.Background())
	assert.Equal(t, []string{"web"}, h.r.Status().PendingVIPs)

	// c04 reaches its verdict, then c03 falls while still a shadow: both
	// are queued, c04 first, so the commit runs with c03's cached verdict
	// unless it reads the live state.
	h.ctrl[4].Store(ctrlPass)
	h.waitVerdict("web", "c04")
	h.ctrl[3].Store(ctrlFail)
	h.waitShadowHealth("web", "c03", false)
	h.waitVerdict("web", "c03")
	h.r.commitReady(context.Background())

	assert.Empty(t, h.r.Status().PendingVIPs)
	assert.Equal(t, model.StateWithdrawn, h.state("web"), "c03 is unhealthy at commit time")
	_, withdraws := h.n.snapshot()
	assert.Equal(t, []string{"10.9.0.1/32"}, withdraws)
}

// Every checker a reload discards is closed: replaced by a promoted shadow,
// dropped with its check or VIP, superseded in staging, or built for a
// reload that was then rejected. Only the running ones stay open.
func TestReload_ClosesDiscardedCheckers(t *testing.T) {
	h := newHarness(t, vipChecks("web", "10.9.0.1/32", 80, 81))
	h.waitState("web", model.StateAnnounced)
	open := func() int32 { return h.built.Load() - h.closed.Load() }
	require.Equal(t, int32(2), open())

	// Rejected reload (port 99 cannot be built): the api checker built
	// before the failure is closed.
	h.write(vipChecks("web", "10.9.0.1/32", 80, 81) + vip("api", "10.9.0.2/32", 80, "") + vip("bad", "10.9.0.3/32", 99, ""))
	assert.Equal(t, model.ReloadRejected, h.r.Reload(context.Background()).Outcome)
	assert.Equal(t, int32(2), open())

	// Dropped check: closed on the immediate path.
	h.write(vipChecks("web", "10.9.0.1/32", 80))
	assert.Equal(t, model.ReloadApplied, h.r.Reload(context.Background()).Outcome)
	assert.Equal(t, int32(1), open())

	// Changed check: the old one is closed once the shadow is promoted.
	h.write(vipChecks("web", "10.9.0.1/32", 82))
	assert.Equal(t, model.ReloadApplied, h.r.Reload(context.Background()).Outcome)
	h.waitPending(0)
	assert.Equal(t, int32(1), open())

	// Superseded staging: the shadow is closed, the running check stays.
	h.write(vipChecks("web", "10.9.0.1/32", 83))
	assert.Equal(t, []string{"web"}, h.r.Reload(context.Background()).Pending)
	h.write(vipChecks("web", "10.9.0.1/32", 82))
	assert.Equal(t, model.ReloadApplied, h.r.Reload(context.Background()).Outcome)
	assert.Empty(t, h.r.Status().PendingVIPs)
	assert.Equal(t, int32(1), open())

	// Removed VIP: closed after the withdraw.
	h.write(vipChecks("web", "10.9.0.1/32", 82) + vip("api", "10.9.0.2/32", 80, ""))
	assert.Equal(t, model.ReloadApplied, h.r.Reload(context.Background()).Outcome)
	h.waitState("api", model.StateAnnounced)
	assert.Equal(t, int32(2), open())
	h.write(vipChecks("web", "10.9.0.1/32", 82))
	assert.Equal(t, model.ReloadApplied, h.r.Reload(context.Background()).Outcome)
	assert.Equal(t, int32(1), open())
}

func TestReload_SupersededStaging(t *testing.T) {
	h := newHarness(t, vip("web", "10.9.0.1/32", 80, ""))
	h.waitState("web", model.StateAnnounced)

	// Stage a change, then reload again before its verdict: the second
	// reload wins and the first staging's shadows are gone.
	h.write(vip("web", "10.9.0.1/32", 83, "    require_local_address: false\n"))
	res := h.r.Reload(context.Background())
	assert.Equal(t, []string{"web"}, res.Pending)
	h.write(vip("web", "10.9.0.1/32", 80, "") + vip("api", "10.9.0.2/32", 81, ""))
	res = h.r.Reload(context.Background())
	assert.Equal(t, model.ReloadApplied, res.Outcome)
	assert.Equal(t, []string{"api"}, res.Added)
	assert.Empty(t, res.Pending, "web is back to its running definition")
	assert.Empty(t, h.r.Status().PendingVIPs)
	h.waitState("api", model.StateAnnounced)
	assert.Equal(t, model.StateAnnounced, h.state("web"))
}

func TestReload_FilePolling(t *testing.T) {
	h := newHarness(t, vip("web", "10.9.0.1/32", 80, ""))
	assert.False(t, h.r.Status().FileChanged)
	h.write(vip("web", "10.9.0.1/32", 80, "") + vip("api", "10.9.0.2/32", 81, ""))
	require.Eventually(t, func() bool { return h.r.Status().FileChanged }, 2*time.Second, 5*time.Millisecond)
	assert.Equal(t, float64(1), testutil.ToFloat64(h.m.ConfigFileChanged))
	h.write(vip("web", "10.9.0.1/32", 80, ""))
	require.Eventually(t, func() bool { return !h.r.Status().FileChanged }, 2*time.Second, 5*time.Millisecond)
	// The flag flips under the lock and onChange fires after it, so wait.
	require.Eventually(t, func() bool {
		h.wmu.Lock()
		defer h.wmu.Unlock()
		return h.wake >= 2
	}, 2*time.Second, 5*time.Millisecond, "onChange fired on each flip")
}
