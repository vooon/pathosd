package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/vooon/pathosd/internal/bgp"
	"github.com/vooon/pathosd/internal/checks"
	"github.com/vooon/pathosd/internal/config"
	"github.com/vooon/pathosd/internal/metrics"
	"github.com/vooon/pathosd/internal/policy"
)

type mockChecker struct {
	mock.Mock
}

func (m *mockChecker) Check(ctx context.Context) checks.Result {
	args := m.Called(ctx)
	result, _ := args.Get(0).(checks.Result)
	return result
}

func (m *mockChecker) Type() string {
	return "fake"
}

func newTestServer(t *testing.T, checker checks.Checker) *http.Server {
	t.Helper()

	cfg := &config.Config{
		Router: config.RouterConfig{
			ASN:          65001,
			RouterID:     "127.0.0.1",
			LocalAddress: "127.0.0.1",
		},
		API: config.APIConfig{
			Listen: ":0",
		},
		BGP: config.BGPConfig{
			ListenPort: 11794,
		},
		VIPs: []config.VIPConfig{
			{
				Name:   "vip-1",
				Prefix: "10.10.1.1/32",
				Check: config.CheckConfig{
					Type: config.CheckTypeHTTP,
				},
				Policy: config.PolicyConfig{
					FailAction: "withdraw",
				},
			},
		},
	}

	m := metrics.New([]float64{0.1, 0.5, 1.0})
	pol := policy.NewManager(cfg.VIPs, m, nil)
	bgpMgr := bgp.NewManager(cfg, m)
	startCtx, startCancel := context.WithTimeout(context.Background(), 2*time.Second)
	require.NoError(t, bgpMgr.Start(startCtx))
	startCancel()
	t.Cleanup(func() {
		bgpMgr.Stop(context.Background())
	})

	sched := checks.NewScheduler(checks.SchedulerConfig{
		VIPName:  "vip-1",
		Checker:  checker,
		Interval: time.Second,
		Timeout:  time.Second,
		Rise:     1,
		Fall:     1,
	})

	return NewServer(ServerDeps{
		Config:  cfg,
		Metrics: m,
		BGP:     bgpMgr,
		Policy:  pol,
		Schedulers: map[string][]*checks.Scheduler{
			"vip-1": {sched},
		},
	})
}

func performRequest(t *testing.T, srv *http.Server, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	rr := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rr, req)
	return rr
}

func TestNewServer_Healthz(t *testing.T) {
	srv := newTestServer(t, &mockChecker{})

	rr := performRequest(t, srv, http.MethodGet, "/healthz")

	require.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Header().Get("Content-Type"), "application/json")
	assert.JSONEq(t, `{"status":"ok"}`, rr.Body.String())
}

func TestNewServer_LandingAndAssets(t *testing.T) {
	srv := newTestServer(t, &mockChecker{})

	t.Run("landing page", func(t *testing.T) {
		rr := performRequest(t, srv, http.MethodGet, "/")
		require.Equal(t, http.StatusOK, rr.Code)
		assert.Contains(t, rr.Header().Get("Content-Type"), "text/html")
		assert.Contains(t, rr.Body.String(), "pathosd")
		assert.Contains(t, rr.Body.String(), "/metrics")
		assert.Contains(t, rr.Body.String(), "Trigger check")
		assert.Contains(t, rr.Body.String(), "vip-1")
		assert.Contains(t, rr.Body.String(), "data-auto-refresh")
		for _, id := range []string{"updated-at", "summary", "vips-body", "peers-body"} {
			assert.Contains(t, rr.Body.String(), `id="`+id+`"`, "live fragment swapped by landing.js")
		}
	})

	t.Run("embedded css asset", func(t *testing.T) {
		rr := performRequest(t, srv, http.MethodGet, "/assets/landing.css")
		require.Equal(t, http.StatusOK, rr.Code)
		assert.Contains(t, rr.Header().Get("Content-Type"), "text/css")
		assert.Contains(t, rr.Body.String(), ":root")
	})

	t.Run("embedded js asset", func(t *testing.T) {
		rr := performRequest(t, srv, http.MethodGet, "/assets/landing.js")
		require.Equal(t, http.StatusOK, rr.Code)
		assert.Contains(t, rr.Header().Get("Content-Type"), "text/javascript")
		assert.Contains(t, rr.Body.String(), "data-trigger-check")
	})

	t.Run("missing asset", func(t *testing.T) {
		rr := performRequest(t, srv, http.MethodGet, "/assets/missing.js")
		require.Equal(t, http.StatusNotFound, rr.Code)
	})
}

func TestNewServer_Status(t *testing.T) {
	srv := newTestServer(t, &mockChecker{})

	rr := performRequest(t, srv, http.MethodGet, "/status")
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Header().Get("Content-Type"), "application/json")

	var payload map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &payload))
	assert.Equal(t, "127.0.0.1", payload["router_id"])

	vipsRaw, ok := payload["vips"].([]any)
	require.True(t, ok)
	require.Len(t, vipsRaw, 1)

	vip, ok := vipsRaw[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "vip-1", vip["name"])
	assert.Equal(t, "10.10.1.1/32", vip["prefix"])
}

func TestNewServer_TriggerCheck(t *testing.T) {
	checker := &mockChecker{}
	checker.On("Check", mock.Anything).Return(checks.Result{
		Success: true,
		Detail:  "ok",
	}).Once()
	srv := newTestServer(t, checker)

	t.Run("success", func(t *testing.T) {
		rr := performRequest(t, srv, http.MethodPost, "/api/v1/vips/vip-1/check")
		require.Equal(t, http.StatusOK, rr.Code)
		assert.Contains(t, rr.Header().Get("Content-Type"), "application/json")
		checker.AssertExpectations(t)

		var payload map[string]any
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &payload))
		assert.Equal(t, "vip-1", payload["vip"])

		result, ok := payload["result"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, true, result["success"])
		assert.Equal(t, "ok", result["detail"])
	})

	t.Run("vip not found", func(t *testing.T) {
		rr := performRequest(t, srv, http.MethodPost, "/api/v1/vips/missing/check")
		require.Equal(t, http.StatusNotFound, rr.Code)
		assert.Contains(t, rr.Header().Get("Content-Type"), "application/json")
		assert.True(t, strings.Contains(rr.Body.String(), "VIP not found"))
	})
}

// newMultiCheckServer builds a server with one VIP that has two checks
// ("a" and "b") combined by policy; each check is backed by its own fake.
func newMultiCheckServer(t *testing.T, checksPolicy string, a, b checks.Checker) *http.Server {
	t.Helper()
	cfg := &config.Config{
		Router: config.RouterConfig{ASN: 65001, RouterID: "127.0.0.1", LocalAddress: "127.0.0.1"},
		API:    config.APIConfig{Listen: ":0"},
		BGP:    config.BGPConfig{ListenPort: 11795},
		VIPs: []config.VIPConfig{{
			Name:         "multi",
			Prefix:       "10.10.2.1/32",
			ChecksPolicy: checksPolicy,
			Checks: []config.CheckConfig{
				{Name: "a", Type: config.CheckTypeHTTP},
				{Name: "b", Type: config.CheckTypeTCP},
			},
			Policy: config.PolicyConfig{FailAction: "withdraw"},
		}},
	}
	m := metrics.New([]float64{0.1, 0.5, 1.0})
	pol := policy.NewManager(cfg.VIPs, m, nil)
	bgpMgr := bgp.NewManager(cfg, m)
	startCtx, startCancel := context.WithTimeout(context.Background(), 2*time.Second)
	require.NoError(t, bgpMgr.Start(startCtx))
	startCancel()
	t.Cleanup(func() { bgpMgr.Stop(context.Background()) })

	mk := func(name string, c checks.Checker) *checks.Scheduler {
		s := checks.NewScheduler(checks.SchedulerConfig{
			VIPName: "multi", CheckName: name, Checker: c,
			Interval: time.Second, Timeout: time.Second, Rise: 1, Fall: 1,
		})
		s.SetCallbacks(pol.OnHealthTransition, pol.OnCheckResult)
		return s
	}
	return NewServer(ServerDeps{
		Config: cfg, Metrics: m, BGP: bgpMgr, Policy: pol,
		Schedulers: map[string][]*checks.Scheduler{"multi": {mk("a", a), mk("b", b)}},
	})
}

func TestNewServer_TriggerCheck_MultiCheck(t *testing.T) {
	okChecker := func() *mockChecker {
		c := &mockChecker{}
		c.On("Check", mock.Anything).Return(checks.Result{Success: true, Detail: "ok"})
		return c
	}
	failChecker := func() *mockChecker {
		c := &mockChecker{}
		c.On("Check", mock.Anything).Return(checks.Result{Success: false, Detail: "boom"})
		return c
	}
	decode := func(t *testing.T, rr *httptest.ResponseRecorder) (map[string]any, map[string]any) {
		t.Helper()
		var payload map[string]any
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &payload))
		result, ok := payload["result"].(map[string]any)
		require.True(t, ok)
		results, ok := payload["results"].(map[string]any)
		require.True(t, ok)
		return result, results
	}

	t.Run("all: one failure fails the combined result", func(t *testing.T) {
		srv := newMultiCheckServer(t, "", okChecker(), failChecker())
		rr := performRequest(t, srv, http.MethodPost, "/api/v1/vips/multi/check")
		require.Equal(t, http.StatusOK, rr.Code)
		result, results := decode(t, rr)
		assert.Equal(t, false, result["success"])
		assert.Equal(t, "a: ok; b: boom", result["detail"])
		assert.Len(t, results, 2)
		assert.Equal(t, true, results["a"].(map[string]any)["success"])
		assert.Equal(t, "boom", results["b"].(map[string]any)["detail"])
	})

	t.Run("any: one success passes", func(t *testing.T) {
		srv := newMultiCheckServer(t, config.ChecksPolicyAny, okChecker(), failChecker())
		rr := performRequest(t, srv, http.MethodPost, "/api/v1/vips/multi/check")
		require.Equal(t, http.StatusOK, rr.Code)
		result, _ := decode(t, rr)
		assert.Equal(t, true, result["success"])
	})

	t.Run("expression", func(t *testing.T) {
		srv := newMultiCheckServer(t, "b && !a", failChecker(), okChecker())
		rr := performRequest(t, srv, http.MethodPost, "/api/v1/vips/multi/check")
		require.Equal(t, http.StatusOK, rr.Code)
		result, _ := decode(t, rr)
		assert.Equal(t, true, result["success"])
	})

	t.Run("single check via query runs only that one", func(t *testing.T) {
		a, b := okChecker(), failChecker()
		srv := newMultiCheckServer(t, config.ChecksPolicyAny, a, b)
		rr := performRequest(t, srv, http.MethodPost, "/api/v1/vips/multi/check?check=a")
		require.Equal(t, http.StatusOK, rr.Code)
		result, results := decode(t, rr)
		assert.Len(t, results, 1)
		assert.Equal(t, true, result["success"])
		b.AssertNotCalled(t, "Check", mock.Anything)

		rr = performRequest(t, srv, http.MethodPost, "/api/v1/vips/multi/check?check=zzz")
		require.Equal(t, http.StatusNotFound, rr.Code)
		assert.Contains(t, rr.Body.String(), "check not found")
	})

	t.Run("status carries per-check counters and the roll-up", func(t *testing.T) {
		srv := newMultiCheckServer(t, "", okChecker(), failChecker())
		performRequest(t, srv, http.MethodPost, "/api/v1/vips/multi/check")
		rr := performRequest(t, srv, http.MethodGet, "/status")
		require.Equal(t, http.StatusOK, rr.Code)
		var payload struct {
			VIPs []struct {
				ConsecutiveOK   int    `json:"consecutive_ok"`
				ConsecutiveFail int    `json:"consecutive_fail"`
				CheckType       string `json:"check_type"`
				Checks          []struct {
					Name            string `json:"name"`
					ConsecutiveOK   int    `json:"consecutive_ok"`
					ConsecutiveFail int    `json:"consecutive_fail"`
				} `json:"checks"`
			} `json:"vips"`
		}
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &payload))
		require.Len(t, payload.VIPs, 1)
		v := payload.VIPs[0]
		assert.Equal(t, "all(http, tcp)", v.CheckType)
		require.Len(t, v.Checks, 2)
		assert.Equal(t, 1, v.Checks[0].ConsecutiveOK)
		assert.Equal(t, 1, v.Checks[1].ConsecutiveFail)
		assert.Equal(t, 0, v.ConsecutiveOK, "min over checks")
		assert.Equal(t, 1, v.ConsecutiveFail, "max over checks")
	})
}

func TestNewServer_Landing_MultiCheck(t *testing.T) {
	a, b := &mockChecker{}, &mockChecker{}
	a.On("Check", mock.Anything).Return(checks.Result{Success: true, Detail: "ok"})
	b.On("Check", mock.Anything).Return(checks.Result{Success: false, Detail: "boom"})
	srv := newMultiCheckServer(t, config.ChecksPolicyAny, a, b)
	performRequest(t, srv, http.MethodPost, "/api/v1/vips/multi/check")

	rr := performRequest(t, srv, http.MethodGet, "/")
	require.Equal(t, http.StatusOK, rr.Code)
	body := rr.Body.String()
	assert.Contains(t, body, "any of:")
	assert.Contains(t, body, "<strong>a:</strong> ok")
	assert.Contains(t, body, "<strong>b:</strong> boom")
	assert.Contains(t, body, `class="tag tag-healthy">healthy</span> a`)
	assert.Contains(t, body, `class="tag tag-unknown">unknown</span> b`, "never-healthy check stays unknown until a transition")
}
