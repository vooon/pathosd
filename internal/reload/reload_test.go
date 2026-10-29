package reload

import (
	"context"
	"os"
	"path/filepath"
	"sync"
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
	mu        sync.Mutex
	announces []string
	withdraws []string
	prepends  []int
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
	n.withdraws = append(n.withdraws, prefix)
	return nil
}

func (n *notifier) PessimizeVIP(ctx context.Context, prefix string, attrs model.RouteAttrs) error {
	return n.AnnounceVIP(ctx, prefix, attrs)
}

func (n *notifier) snapshot() (announces, withdraws []string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.announces...), append([]string(nil), n.withdraws...)
}

// outcomes maps "vip/check" to the result every check run returns; the
// tcp port in the config selects it (port 1 fails, anything else passes).
type portChecker struct{ port uint16 }

func (p portChecker) Type() string { return "tcp" }
func (p portChecker) Check(context.Context) checks.Result {
	if p.port == 1 {
		return checks.Result{Detail: "port 1 refused"}
	}
	return checks.Result{Success: true, Detail: "ok"}
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
}

func newHarness(t *testing.T, body string) *harness {
	t.Helper()
	h := &harness{t: t, path: filepath.Join(t.TempDir(), "pathosd.yaml"), n: &notifier{}, m: metrics.New([]float64{0.1})}
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
		return portChecker{port: c.TCP.Port}, nil
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
	go h.r.Run(ctx, 10*time.Millisecond)
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
	h.wmu.Lock()
	assert.GreaterOrEqual(t, h.wake, 2, "onChange fired on each flip")
	h.wmu.Unlock()
}
