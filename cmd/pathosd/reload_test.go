package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vooon/pathosd/internal/model"
)

func fakeReloadAPI(t *testing.T, code int, res model.ReloadStatus) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/reload", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "pathosd-reload", r.Header.Get("User-Agent"))
		w.WriteHeader(code)
		require.NoError(t, json.NewEncoder(w).Encode(res))
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return strings.TrimPrefix(ts.URL, "http://")
}

func TestReloadCmd(t *testing.T) {
	t.Run("applied", func(t *testing.T) {
		addr := fakeReloadAPI(t, http.StatusOK, model.ReloadStatus{Outcome: model.ReloadApplied, Added: []string{"api"}, Changed: []string{"web"}, Pending: []string{"web"}})
		var out bytes.Buffer
		require.NoError(t, (&ReloadCmd{Addr: addr, Timeout: time.Second}).run(&out))
		assert.Equal(t, "applied: added api; changed web (pending verdict: web)\n", out.String())
	})

	t.Run("unchanged", func(t *testing.T) {
		addr := fakeReloadAPI(t, http.StatusOK, model.ReloadStatus{Outcome: model.ReloadUnchanged})
		var out bytes.Buffer
		require.NoError(t, (&ReloadCmd{Addr: addr, Timeout: time.Second}).run(&out))
		assert.Equal(t, "unchanged\n", out.String())
	})

	t.Run("rejected exits 1 with the reason", func(t *testing.T) {
		addr := fakeReloadAPI(t, http.StatusUnprocessableEntity, model.ReloadStatus{Outcome: model.ReloadRejected, Error: "restart required: router changed"})
		code, msg := exitCode(t, (&ReloadCmd{Addr: addr, Timeout: time.Second}).run(&bytes.Buffer{}))
		assert.Equal(t, exitUnhealthy, code)
		assert.Equal(t, "rejected: restart required: router changed", msg)
	})

	t.Run("failed exits 1 with every error", func(t *testing.T) {
		addr := fakeReloadAPI(t, http.StatusInternalServerError, model.ReloadStatus{Outcome: model.ReloadFailed, Removed: []string{"old"}, Errors: []string{"withdrawing old: boom"}})
		code, msg := exitCode(t, (&ReloadCmd{Addr: addr, Timeout: time.Second}).run(&bytes.Buffer{}))
		assert.Equal(t, exitUnhealthy, code)
		assert.Equal(t, "failed: removed old; withdrawing old: boom", msg)
	})

	t.Run("daemon without reload support", func(t *testing.T) {
		mux := http.NewServeMux()
		ts := httptest.NewServer(mux)
		t.Cleanup(ts.Close)
		code, msg := exitCode(t, (&ReloadCmd{Addr: strings.TrimPrefix(ts.URL, "http://"), Timeout: time.Second}).run(&bytes.Buffer{}))
		assert.Equal(t, exitUnhealthy, code)
		assert.Contains(t, msg, "reload not applied: POST /api/v1/reload: HTTP 404")
	})

	t.Run("unreachable", func(t *testing.T) {
		code, msg := exitCode(t, (&ReloadCmd{Addr: "127.0.0.1:1", Timeout: time.Second}).run(&bytes.Buffer{}))
		assert.Equal(t, exitUnhealthy, code)
		assert.Contains(t, msg, "reload not applied: ")
	})

	t.Run("bad config path is an internal error", func(t *testing.T) {
		code, _ := exitCode(t, (&ReloadCmd{Config: "/nonexistent.yaml", Timeout: time.Second}).run(&bytes.Buffer{}))
		assert.Equal(t, exitInternal, code)
	})
}
