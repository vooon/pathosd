package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vooon/pathosd/internal/model"
)

// fakeAPI serves /healthz and /status like the daemon, with a mutable status.
func fakeAPI(t *testing.T, st *model.DaemonStatus) (addr string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	// Same shape and rule as httpapi.handleReadyz.
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, userAgent, r.Header.Get("User-Agent"))
		unready := model.UnreadyPeers(st.Peers)
		status, payload := http.StatusOK, map[string]any{"status": "ready", "peers": st.Peers}
		if len(unready) > 0 {
			status, payload = http.StatusServiceUnavailable, map[string]any{"status": "not_ready", "unready": unready, "peers": st.Peers}
		}
		w.WriteHeader(status)
		require.NoError(t, json.NewEncoder(w).Encode(payload))
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return strings.TrimPrefix(ts.URL, "http://")
}

func exitCode(t *testing.T, err error) (int, string) {
	t.Helper()
	var ec *exitCodeError
	require.ErrorAs(t, err, &ec)
	return ec.code, ec.msg
}

func TestHealthcheckCmd(t *testing.T) {
	st := &model.DaemonStatus{
		Peers: []model.PeerStatus{
			{Name: "frr", Required: true, SessionState: model.SessionEstablished},
			{Name: "bird", Required: true, SessionState: "active"},
		},
		VIPs: []model.VIPStatus{{Name: "web", State: model.StateAnnounced}},
	}
	addr := fakeAPI(t, st)

	t.Run("not ready while a required peer is down", func(t *testing.T) {
		var out bytes.Buffer
		err := (&HealthcheckCmd{Addr: addr, Timeout: time.Second}).run(&out)
		code, msg := exitCode(t, err)
		assert.Equal(t, exitUnhealthy, code)
		assert.Equal(t, "not ready: peers: 1/2 established (down: bird)", msg)
		assert.Empty(t, out.String())
	})

	t.Run("ready once every required peer is established", func(t *testing.T) {
		st.Peers[1].SessionState = model.SessionEstablished
		var out bytes.Buffer
		require.NoError(t, (&HealthcheckCmd{Addr: addr, Timeout: time.Second}).run(&out))
		assert.Equal(t, "ready: peers: 2/2 established\n", out.String())
	})

	t.Run("quiet prints nothing", func(t *testing.T) {
		var out bytes.Buffer
		require.NoError(t, (&HealthcheckCmd{Addr: addr, Timeout: time.Second, Quiet: true}).run(&out))
		assert.Empty(t, out.String())
	})

	t.Run("live only needs /healthz", func(t *testing.T) {
		st.Peers[1].SessionState = "idle"
		var out bytes.Buffer
		require.NoError(t, (&HealthcheckCmd{Addr: addr, Timeout: time.Second, Live: true}).run(&out))
		assert.Equal(t, "alive: HTTP API answers on "+addr+"\n", out.String())
	})

	t.Run("address from config", func(t *testing.T) {
		st.Peers[1].SessionState = model.SessionEstablished
		cfgPath := filepath.Join(t.TempDir(), "pathosd.yaml")
		require.NoError(t, os.WriteFile(cfgPath, []byte(fmt.Sprintf(`schema: v1
router: {asn: 65001, router_id: 192.0.2.1, local_address: 192.0.2.1}
api: {listen: "%s"}
bgp: {neighbors: [{name: p, address: 192.0.2.2, peer_asn: 65002}]}
vips:
  - name: web
    prefix: 10.99.0.1/32
    check: {type: tcp, tcp: {host: 127.0.0.1, port: 80}}
`, addr)), 0o600))
		var out bytes.Buffer
		require.NoError(t, (&HealthcheckCmd{Config: cfgPath, Timeout: time.Second}).run(&out))
		assert.True(t, strings.HasPrefix(out.String(), "ready: "), out.String())
	})

	t.Run("missing config is an internal error", func(t *testing.T) {
		err := (&HealthcheckCmd{Config: filepath.Join(t.TempDir(), "nope.yaml"), Timeout: time.Second}).run(&bytes.Buffer{})
		code, msg := exitCode(t, err)
		assert.Equal(t, exitInternal, code)
		assert.Contains(t, msg, "loading config")
	})

	t.Run("unreachable daemon is unhealthy", func(t *testing.T) {
		err := (&HealthcheckCmd{Addr: fmt.Sprintf("127.0.0.1:%d", closedPort(t)), Timeout: time.Second}).run(&bytes.Buffer{})
		code, msg := exitCode(t, err)
		assert.Equal(t, exitUnhealthy, code)
		assert.Contains(t, msg, "not ready: ")
		assert.Contains(t, msg, "connection refused")
	})

	t.Run("timeout is unhealthy", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		t.Cleanup(func() { _ = ln.Close() })
		// Listener that never answers.
		err = (&HealthcheckCmd{Addr: ln.Addr().String(), Timeout: 100 * time.Millisecond}).run(&bytes.Buffer{})
		code, _ := exitCode(t, err)
		assert.Equal(t, exitUnhealthy, code)
	})
}

func TestLoopbackFor(t *testing.T) {
	for in, want := range map[string]string{
		":59179":             "127.0.0.1:59179",
		"0.0.0.0:59179":      "127.0.0.1:59179",
		"[::]:59179":         "[::1]:59179",
		"127.0.0.1:1234":     "127.0.0.1:1234",
		"[fe80::1%eth0]:443": "[fe80::1%eth0]:443",
		"host.local:80":      "host.local:80",
	} {
		got, err := loopbackFor(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
	_, err := loopbackFor("59179")
	assert.Error(t, err)
	var ec *exitCodeError
	assert.False(t, errors.As(err, &ec))
}
