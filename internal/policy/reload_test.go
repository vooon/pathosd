package policy

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vooon/pathosd/internal/checks"
	"github.com/vooon/pathosd/internal/config"
	"github.com/vooon/pathosd/internal/model"
)

func loadVIPs(t *testing.T, vips string) []config.VIPConfig {
	t.Helper()
	path := filepath.Join(t.TempDir(), "p.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`schema: v1
router: {asn: 65001, router_id: 192.0.2.1, local_address: 192.0.2.1}
bgp: {neighbors: [{name: p, address: 192.0.2.2, peer_asn: 65002}]}
`+vips), 0o600))
	cfg, err := config.Load(path)
	require.NoError(t, err)
	return cfg.VIPs
}

const reloadVIPs = `vips:
  - name: web
    prefix: 10.9.0.1/32
    check: {type: tcp, tcp: {host: 127.0.0.1, port: 80}}
    policy: {fail_action: withdraw}
`

func vipByName(statuses []model.VIPStatus, name string) *model.VIPStatus {
	for i := range statuses {
		if statuses[i].Name == name {
			return &statuses[i]
		}
	}
	return nil
}

func TestManager_AddAndRemoveVIP(t *testing.T) {
	m := newTestMetrics()
	n := &fakeBGPNotifier{}
	mgr := NewManager(loadVIPs(t, reloadVIPs), m, n)

	added := loadVIPs(t, reloadVIPs+`  - name: api
    prefix: 10.9.0.2/32
    check: {type: tcp, tcp: {host: 127.0.0.1, port: 81}}
    policy: {fail_action: withdraw}
`)
	mgr.AddVIP(&added[1])
	assert.Equal(t, []string{"web", "api"}, mgr.VIPNames())
	st := vipByName(mgr.GetVIPStatuses(), "api")
	require.NotNil(t, st)
	assert.Equal(t, model.StateWithdrawn, st.State)
	assert.Equal(t, model.HealthUnknown, st.Health)

	mgr.OnHealthTransition(checks.HealthTransition{VIPName: "api", CheckName: "tcp", Healthy: true, Reason: "rise"})
	assert.Equal(t, []string{"10.9.0.2/32"}, n.announces)
	assert.Equal(t, 2, testutil.CollectAndCount(m.VIPState))

	require.NoError(t, mgr.RemoveVIP(context.Background(), "api"))
	assert.Equal(t, []string{"10.9.0.2/32"}, n.withdraws, "announced VIP is withdrawn on removal")
	assert.Equal(t, []string{"web"}, mgr.VIPNames())
	assert.Nil(t, vipByName(mgr.GetVIPStatuses(), "api"))
	assert.Equal(t, 1, testutil.CollectAndCount(m.VIPState), "metric series of the removed VIP are gone")

	require.NoError(t, mgr.RemoveVIP(context.Background(), "web"), "withdrawn VIP needs no BGP call")
	assert.Len(t, n.withdraws, 1)
	require.NoError(t, mgr.RemoveVIP(context.Background(), "ghost"))
}

func TestManager_CommitVIP(t *testing.T) {
	t.Run("policy only re-originates in place", func(t *testing.T) {
		n := &fakeBGPNotifier{}
		mgr := NewManager(loadVIPs(t, reloadVIPs), newTestMetrics(), n)
		mgr.OnHealthTransition(checks.HealthTransition{VIPName: "web", Healthy: true, Reason: "rise"})
		require.Len(t, n.announces, 1)

		next := loadVIPs(t, `vips:
  - name: web
    prefix: 10.9.0.1/32
    check: {type: tcp, tcp: {host: 127.0.0.1, port: 80}}
    policy: {fail_action: withdraw, announce: {as_path_prepend: 2, communities: ["65001:7"]}}
`)
		require.NoError(t, mgr.CommitVIP(context.Background(), &next[0], nil))
		require.Len(t, n.announces, 2)
		assert.Equal(t, 2, n.announceAttrs[1].ASPathPrepend)
		assert.Equal(t, []string{"65001:7"}, n.announceAttrs[1].Communities)
		assert.Empty(t, n.withdraws)
		st := vipByName(mgr.GetVIPStatuses(), "web")
		assert.Equal(t, model.StateAnnounced, st.State)
		assert.Equal(t, 2, st.ASPathPrepend)

		// Same attributes again: nothing to send.
		require.NoError(t, mgr.CommitVIP(context.Background(), &next[0], nil))
		assert.Len(t, n.announces, 2)
	})

	t.Run("seeded unhealthy check withdraws", func(t *testing.T) {
		n := &fakeBGPNotifier{}
		mgr := NewManager(loadVIPs(t, reloadVIPs), newTestMetrics(), n)
		mgr.OnHealthTransition(checks.HealthTransition{VIPName: "web", Healthy: true, Reason: "rise"})

		next := loadVIPs(t, reloadVIPs)
		seeds := map[string]CheckSeed{"tcp": {Health: model.HealthUnhealthy, LastResult: checks.Result{Detail: "refused"}}}
		require.NoError(t, mgr.CommitVIP(context.Background(), &next[0], seeds))
		assert.Equal(t, []string{"10.9.0.1/32"}, n.withdraws)
		st := vipByName(mgr.GetVIPStatuses(), "web")
		assert.Equal(t, model.StateWithdrawn, st.State)
		assert.Equal(t, "config reload", st.LastTransitionReason)
		assert.Equal(t, "refused", st.LastCheckDetail)
	})

	t.Run("seeded healthy check keeps the route", func(t *testing.T) {
		n := &fakeBGPNotifier{}
		mgr := NewManager(loadVIPs(t, reloadVIPs), newTestMetrics(), n)
		mgr.OnHealthTransition(checks.HealthTransition{VIPName: "web", Healthy: true, Reason: "rise"})

		next := loadVIPs(t, reloadVIPs)
		seeds := map[string]CheckSeed{"tcp": {Health: model.HealthHealthy, LastResult: checks.Result{Success: true}}}
		require.NoError(t, mgr.CommitVIP(context.Background(), &next[0], seeds))
		assert.Empty(t, n.withdraws)
		assert.Len(t, n.announces, 1, "no re-announce without attribute change")
	})

	t.Run("checks added, removed and policy expression", func(t *testing.T) {
		m := newTestMetrics()
		n := &fakeBGPNotifier{}
		mgr := NewManager(loadVIPs(t, reloadVIPs), m, n)
		mgr.OnHealthTransition(checks.HealthTransition{VIPName: "web", Healthy: true, Reason: "rise"})
		mgr.OnCheckResult("web", "tcp", checks.Result{Success: true})
		assert.Equal(t, 1, testutil.CollectAndCount(m.CheckLastResult))

		next := loadVIPs(t, `vips:
  - name: web
    prefix: 10.9.0.1/32
    checks_policy: "http || dns"
    checks:
      - {type: http, http: {url: /, host: 127.0.0.1, port: 80}}
      - {type: tcp, name: dns, tcp: {host: 127.0.0.1, port: 53}}
    policy: {fail_action: withdraw}
`)
		seeds := map[string]CheckSeed{
			"http": {Health: model.HealthUnhealthy},
			"dns":  {Health: model.HealthHealthy, LastResult: checks.Result{Success: true}},
		}
		require.NoError(t, mgr.CommitVIP(context.Background(), &next[0], seeds))
		st := vipByName(mgr.GetVIPStatuses(), "web")
		assert.Equal(t, model.StateAnnounced, st.State, "http || dns holds with dns healthy")
		assert.Equal(t, "http || dns", st.ChecksPolicy)
		require.Len(t, st.Checks, 2)
		assert.Equal(t, "http", st.Checks[0].Name)
		assert.Equal(t, "dns", st.Checks[1].Name)
		assert.Equal(t, 0, testutil.CollectAndCount(m.CheckLastResult), "series of the removed tcp check are gone")
		assert.Empty(t, n.withdraws)

		// A check without state or seed is a programming error, not a silent gap.
		bad := loadVIPs(t, reloadVIPs+"    checks_policy: all\n")
		_ = bad
		err := mgr.CommitVIP(context.Background(), &next[0], nil)
		assert.NoError(t, err, "existing checks keep state without seeds")
		other := loadVIPs(t, `vips:
  - name: web
    prefix: 10.9.0.1/32
    check: {type: tcp, name: fresh, tcp: {host: 127.0.0.1, port: 1}}
    policy: {fail_action: withdraw}
`)
		assert.ErrorContains(t, mgr.CommitVIP(context.Background(), &other[0], nil), "neither a running state nor a seed")
	})

	t.Run("prefix or name mismatch is refused", func(t *testing.T) {
		mgr := NewManager(loadVIPs(t, reloadVIPs), newTestMetrics(), &fakeBGPNotifier{})
		other := loadVIPs(t, `vips:
  - name: web
    prefix: 10.9.0.9/32
    check: {type: tcp, tcp: {host: 127.0.0.1, port: 80}}
    policy: {fail_action: withdraw}
`)
		assert.ErrorContains(t, mgr.CommitVIP(context.Background(), &other[0], nil), "prefix")
		other[0].Name = "nope"
		assert.ErrorContains(t, mgr.CommitVIP(context.Background(), &other[0], nil), "unknown VIP")
	})
}
