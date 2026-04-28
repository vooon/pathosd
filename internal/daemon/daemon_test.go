package daemon

import (
	"context"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/fx"

	"github.com/vooon/pathosd/internal/bgp"
	"github.com/vooon/pathosd/internal/checks"
	"github.com/vooon/pathosd/internal/config"
	"github.com/vooon/pathosd/internal/metrics"
	"github.com/vooon/pathosd/internal/policy"
)

type hookCollector struct {
	hooks []fx.Hook
}

func (c *hookCollector) Append(hook fx.Hook) {
	c.hooks = append(c.hooks, hook)
}

type fakeShutdowner struct {
	called atomic.Bool
	err    error
}

func (f *fakeShutdowner) Shutdown(_ ...fx.ShutdownOption) error {
	f.called.Store(true)
	return f.err
}

type countingChecker struct {
	calls atomic.Int64
}

func (c *countingChecker) Check(ctx context.Context) checks.Result {
	c.calls.Add(1)
	return checks.Result{Success: true, Detail: "ok", Duration: time.Millisecond}
}

func (c *countingChecker) Type() string { return "fake" }

func httpCheckConfigFromServerURL(t *testing.T, serverURL string) *config.HTTPCheckConfig {
	t.Helper()

	u, err := url.Parse(serverURL)
	require.NoError(t, err)

	host, portStr, err := net.SplitHostPort(u.Host)
	require.NoError(t, err)

	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)

	path := u.Path
	if path == "" {
		path = "/"
	}

	return &config.HTTPCheckConfig{
		URL:           path,
		Proto:         u.Scheme,
		Host:          host,
		Port:          uint16(port),
		Method:        "GET",
		ResponseCodes: []int{http.StatusOK},
	}
}

func maxFiniteDurationBucketBound(t *testing.T, m *metrics.Metrics) float64 {
	t.Helper()

	mfs, err := m.Registry.Gather()
	require.NoError(t, err)

	found := false
	maxFinite := 0.0
	for _, mf := range mfs {
		if mf.GetName() != "pathosd_check_duration_seconds" {
			continue
		}
		for _, metric := range mf.GetMetric() {
			h := metric.GetHistogram()
			if h == nil {
				continue
			}
			for _, bucket := range h.GetBucket() {
				bound := bucket.GetUpperBound()
				if math.IsInf(bound, 1) {
					continue
				}
				if !found || bound > maxFinite {
					maxFinite = bound
					found = true
				}
			}
		}
	}

	require.True(t, found, "pathosd_check_duration_seconds finite buckets not found")
	return maxFinite
}

func TestProvideMetrics(t *testing.T) {
	tests := []struct {
		name          string
		cfg           *config.Config
		expectedMaxTo time.Duration
	}{
		{
			name: "uses maximum timeout across VIPs",
			cfg: &config.Config{
				VIPs: []config.VIPConfig{
					{
						Name:   "vip-a",
						Prefix: "10.0.0.1/32",
						Check: config.CheckConfig{
							Timeout: new(config.Duration{Duration: 200 * time.Millisecond}),
						},
					},
					{
						Name:   "vip-b",
						Prefix: "10.0.0.2/32",
						Check: config.CheckConfig{
							Timeout: new(config.Duration{Duration: 2 * time.Second}),
						},
					},
				},
			},
			expectedMaxTo: 2 * time.Second,
		},
		{
			name: "handles empty VIP list",
			cfg: &config.Config{
				VIPs: []config.VIPConfig{},
			},
			expectedMaxTo: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := provideMetrics(tc.cfg)
			require.NotNil(t, m)

			// Seed one sample so histogram instances exist in gathered output.
			m.CheckDuration.WithLabelValues("test-vip", "10.0.0.1/32", "http").Observe(0.1)

			gotMaxBound := maxFiniteDurationBucketBound(t, m)
			wantBuckets := metrics.GenerateCheckBuckets(tc.expectedMaxTo)
			require.NotEmpty(t, wantBuckets)
			assert.InDelta(t, wantBuckets[len(wantBuckets)-1], gotMaxBound, 1e-9)
		})
	}
}

func TestRegisterBGPMetrics(t *testing.T) {
	cfg := &config.Config{
		Router: config.RouterConfig{
			ASN:      65000,
			RouterID: "10.0.0.1",
		},
		BGP: config.BGPConfig{
			ListenPort: 11790,
			Neighbors: []config.NeighborConfig{
				{
					Name:    "peer-1",
					Address: "192.0.2.1",
					PeerASN: 65001,
					Passive: true,
				},
			},
		},
	}

	m := metrics.New(metrics.GenerateCheckBuckets(time.Second))
	mgr := bgp.NewManager(cfg, m)

	require.NoError(t, mgr.RegisterMetrics(m.Registry))
	require.NoError(t, mgr.RegisterMetrics(m.Registry), "registration should be idempotent")

	require.NoError(t, mgr.Start(context.Background()))
	t.Cleanup(func() { mgr.Stop(context.Background()) })
	require.NoError(t, mgr.AddPeers(context.Background()))

	mfs, err := m.Registry.Gather()
	require.NoError(t, err)

	names := make(map[string]struct{}, len(mfs))
	for _, mf := range mfs {
		names[mf.GetName()] = struct{}{}
	}

	assert.Contains(t, names, "bgp_peer_state")
	assert.Contains(t, names, "fsm_loop_event_timing_sec")
}

func TestProvideSchedulers(t *testing.T) {
	t.Run("returns error for unsupported checker type", func(t *testing.T) {
		cfg := &config.Config{
			VIPs: []config.VIPConfig{
				{
					Name:   "vip-bad",
					Prefix: "10.0.0.10/32",
					Check: config.CheckConfig{
						Type:     "nope",
						Interval: new(config.Duration{Duration: 100 * time.Millisecond}),
						Timeout:  new(config.Duration{Duration: 50 * time.Millisecond}),
					},
				},
			},
		}

		_, err := provideSchedulers(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `creating checker for VIP "vip-bad"`)
	})

	t.Run("uses configured rise and fall values", func(t *testing.T) {
		var failMode atomic.Bool
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if failMode.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(srv.Close)

		httpCfg := httpCheckConfigFromServerURL(t, srv.URL)

		cfg := &config.Config{
			VIPs: []config.VIPConfig{
				{
					Name:   "vip-explicit",
					Prefix: "10.0.0.11/32",
					Check: config.CheckConfig{
						Type:     config.CheckTypeHTTP,
						Interval: new(config.Duration{Duration: 100 * time.Millisecond}),
						Timeout:  new(config.Duration{Duration: 50 * time.Millisecond}),
						Rise:     new(2),
						Fall:     new(1),
						HTTP:     httpCfg,
					},
				},
			},
		}

		out, err := provideSchedulers(cfg)
		require.NoError(t, err)
		sched := out.Schedulers["vip-explicit"]
		require.NotNil(t, sched)

		result, err := sched.TriggerCheck(context.Background())
		require.NoError(t, err)
		require.True(t, result.Success)
		assert.False(t, sched.IsHealthy(), "rise=2 should require two successes")

		result, err = sched.TriggerCheck(context.Background())
		require.NoError(t, err)
		require.True(t, result.Success)
		assert.True(t, sched.IsHealthy())

		failMode.Store(true)
		result, err = sched.TriggerCheck(context.Background())
		require.NoError(t, err)
		require.False(t, result.Success)
		assert.False(t, sched.IsHealthy(), "fall=1 should flip unhealthy after one failure")
	})

	t.Run("uses default rise=1 and fall=3 when unset", func(t *testing.T) {
		var failMode atomic.Bool
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if failMode.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(srv.Close)

		httpCfg := httpCheckConfigFromServerURL(t, srv.URL)
		httpCfg.Method = ""
		httpCfg.ResponseCodes = nil

		cfg := &config.Config{
			VIPs: []config.VIPConfig{
				{
					Name:   "vip-defaults",
					Prefix: "10.0.0.12/32",
					Check: config.CheckConfig{
						Type: config.CheckTypeHTTP,
						HTTP: httpCfg,
					},
				},
			},
		}
		config.ApplyDefaults(cfg)

		out, err := provideSchedulers(cfg)
		require.NoError(t, err)
		sched := out.Schedulers["vip-defaults"]
		require.NotNil(t, sched)

		result, err := sched.TriggerCheck(context.Background())
		require.NoError(t, err)
		require.True(t, result.Success)
		assert.True(t, sched.IsHealthy(), "default rise=1 should become healthy immediately")

		failMode.Store(true)

		for i := 0; i < 2; i++ {
			result, err = sched.TriggerCheck(context.Background())
			require.NoError(t, err)
			require.False(t, result.Success)
			assert.True(t, sched.IsHealthy(), "default fall=3 should absorb first two failures")
		}

		result, err = sched.TriggerCheck(context.Background())
		require.NoError(t, err)
		require.False(t, result.Success)
		assert.False(t, sched.IsHealthy(), "default fall=3 should flip unhealthy on third failure")
	})
}

func TestRegisterProcessLifecycle(t *testing.T) {
	lc := &hookCollector{}
	registerProcessLifecycle(lc)

	require.Len(t, lc.hooks, 1)
	hook := lc.hooks[0]
	require.NotNil(t, hook.OnStart)
	require.NotNil(t, hook.OnStop)
	require.NoError(t, hook.OnStart(context.Background()))
	require.NoError(t, hook.OnStop(context.Background()))
}

func TestRegisterBGPLifecycle(t *testing.T) {
	cfg := &config.Config{
		Router: config.RouterConfig{
			ASN:      65000,
			RouterID: "10.0.0.1",
		},
		BGP: config.BGPConfig{
			ListenPort: 11791,
			Neighbors:  []config.NeighborConfig{},
		},
	}
	mgr := bgp.NewManager(cfg, nil)

	lc := &hookCollector{}
	registerBGPLifecycle(lc, mgr)

	require.Len(t, lc.hooks, 1)
	hook := lc.hooks[0]
	require.NoError(t, hook.OnStart(context.Background()))
	require.NoError(t, mgr.AnnounceVIP("10.1.0.1/32"))
	require.NoError(t, hook.OnStop(context.Background()))
}

func TestRegisterPeerWatcherLifecycle(t *testing.T) {
	cfg := &config.Config{
		Router: config.RouterConfig{
			ASN:      65000,
			RouterID: "10.0.0.1",
		},
		BGP: config.BGPConfig{
			ListenPort: 11792,
			Neighbors: []config.NeighborConfig{
				{Name: "peer1", Address: "10.0.0.2", PeerASN: 65001},
			},
		},
	}
	mgr := bgp.NewManager(cfg, nil)
	require.NoError(t, mgr.Start(context.Background()))
	t.Cleanup(func() { mgr.Stop(context.Background()) })

	lc := &hookCollector{}
	m := metrics.New(metrics.GenerateCheckBuckets(time.Second))
	registerPeerWatcherLifecycle(lc, cfg, m, mgr)

	require.Len(t, lc.hooks, 1)
	hook := lc.hooks[0]
	require.NoError(t, hook.OnStart(context.Background()))
	require.NoError(t, hook.OnStop(context.Background()))
}

func TestRegisterSchedulersLifecycle(t *testing.T) {
	checker := &countingChecker{}
	sched := checks.NewScheduler(checks.SchedulerConfig{
		VIPName:  "vip-a",
		Checker:  checker,
		Interval: 20 * time.Millisecond,
		Timeout:  10 * time.Millisecond,
		Rise:     1,
		Fall:     1,
	})

	pol := policy.NewManager([]config.VIPConfig{
		{
			Name:   "vip-a",
			Prefix: "10.0.0.21/32",
			Check:  config.CheckConfig{Type: config.CheckTypeHTTP},
			Policy: config.PolicyConfig{FailAction: "withdraw"},
		},
	}, metrics.New([]float64{0.1}), nil)

	lc := &hookCollector{}
	registerSchedulersLifecycle(lc, map[string]*checks.Scheduler{"vip-a": sched}, pol)

	require.Len(t, lc.hooks, 1)
	hook := lc.hooks[0]
	require.NoError(t, hook.OnStart(context.Background()))
	require.Eventually(t, func() bool {
		return checker.calls.Load() > 0
	}, time.Second, 10*time.Millisecond)
	require.NoError(t, hook.OnStop(context.Background()))
}

func TestRegisterHTTPServerLifecycle(t *testing.T) {
	tests := []struct {
		name        string
		addr        string
		expectStart bool
	}{
		{
			name:        "invalid listen address returns start error",
			addr:        "bad addr",
			expectStart: false,
		},
		{
			name:        "ephemeral localhost address starts and stops cleanly",
			addr:        "127.0.0.1:0",
			expectStart: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := &http.Server{
				Addr: tc.addr,
				Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
				}),
			}
			sh := &fakeShutdowner{}
			lc := &hookCollector{}
			registerHTTPServerLifecycle(lc, srv, sh)

			require.Len(t, lc.hooks, 1)
			hook := lc.hooks[0]

			err := hook.OnStart(context.Background())
			if !tc.expectStart {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			require.NoError(t, hook.OnStop(stopCtx))
			assert.False(t, sh.called.Load())
		})
	}
}

func TestRun_ReturnsErrorOnStartFailure(t *testing.T) {
	cfg := &config.Config{
		Router: config.RouterConfig{
			ASN:      65000,
			RouterID: "10.0.0.1",
		},
		API: config.APIConfig{
			Listen: "bad addr",
		},
		BGP: config.BGPConfig{
			ListenPort: 11793,
			Neighbors:  []config.NeighborConfig{},
		},
		VIPs: []config.VIPConfig{},
	}

	err := Run(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "starting daemon:")
	assert.Contains(t, err.Error(), "starting HTTP API listener")
}
