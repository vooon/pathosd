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
		Schedulers: map[string]*checks.Scheduler{
			"vip-1": sched,
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
