package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vooon/pathosd/internal/model"
)

// fakeReloader answers with a fixed outcome and remembers the status.
type fakeReloader struct {
	res   model.ReloadStatus
	calls int
}

func (f *fakeReloader) Reload(context.Context) model.ReloadStatus {
	f.calls++
	cp := f.res
	cp.At = time.Now()
	f.res = cp
	return cp
}

func (f *fakeReloader) Status() model.ConfigStatus {
	st := model.ConfigStatus{Path: "/etc/pathosd/pathosd.yaml", Generation: 1 + f.calls, FileChanged: f.calls == 0}
	if f.calls > 0 {
		cp := f.res
		st.LastReload = &cp
		st.PendingVIPs = cp.Pending
	}
	return st
}

func TestNewServer_Reload(t *testing.T) {
	cases := []struct {
		name string
		res  model.ReloadStatus
		code int
	}{
		{"applied", model.ReloadStatus{Outcome: model.ReloadApplied, Added: []string{"api"}, Pending: []string{"web"}}, http.StatusOK},
		{"unchanged", model.ReloadStatus{Outcome: model.ReloadUnchanged}, http.StatusOK},
		{"rejected", model.ReloadStatus{Outcome: model.ReloadRejected, Error: "restart required: router changed"}, http.StatusUnprocessableEntity},
		{"failed", model.ReloadStatus{Outcome: model.ReloadFailed, Errors: []string{"withdrawing old: boom"}}, http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rl := &fakeReloader{res: tc.res}
			srv := newTestServerWith(t, &mockChecker{}, func(d *ServerDeps) { d.Reloader = rl })

			rr := performRequest(t, srv, http.MethodPost, "/api/v1/reload")
			require.Equal(t, tc.code, rr.Code, rr.Body.String())
			var got model.ReloadStatus
			require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &got))
			assert.Equal(t, tc.res.Outcome, got.Outcome)
			assert.Equal(t, tc.res.Error, got.Error)
			assert.Equal(t, tc.res.Errors, got.Errors)
			assert.Equal(t, 1, rl.calls)

			// /status carries the config block with the last reload.
			rr = performRequest(t, srv, http.MethodGet, "/status")
			require.Equal(t, http.StatusOK, rr.Code)
			var st model.DaemonStatus
			require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &st))
			require.NotNil(t, st.Config)
			assert.Equal(t, 2, st.Config.Generation)
			require.NotNil(t, st.Config.LastReload)
			assert.Equal(t, tc.res.Outcome, st.Config.LastReload.Outcome)

			// The landing page shows a banner unless everything is applied.
			rr = performRequest(t, srv, http.MethodGet, "/")
			require.Equal(t, http.StatusOK, rr.Code)
			body := rr.Body.String()
			assert.Contains(t, body, `id="config-notice"`)
			switch tc.res.Outcome {
			case model.ReloadUnchanged:
				assert.NotContains(t, body, `class="notice"`)
			case model.ReloadApplied:
				assert.Contains(t, body, "reload pending verdict for web")
			case model.ReloadRejected:
				assert.Contains(t, body, "config reload rejected")
				assert.Contains(t, body, "restart required: router changed")
			case model.ReloadFailed:
				assert.Contains(t, body, "config reload failed")
			}
		})
	}

	t.Run("without reloader", func(t *testing.T) {
		srv := newTestServer(t, &mockChecker{})
		rr := performRequest(t, srv, http.MethodPost, "/api/v1/reload")
		assert.Equal(t, http.StatusNotImplemented, rr.Code)
		rr = performRequest(t, srv, http.MethodGet, "/status")
		var st model.DaemonStatus
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &st))
		assert.Nil(t, st.Config)
		rr = performRequest(t, srv, http.MethodGet, "/")
		assert.NotContains(t, rr.Body.String(), `class="notice"`)
	})

	t.Run("file changed banner", func(t *testing.T) {
		srv := newTestServerWith(t, &mockChecker{}, func(d *ServerDeps) { d.Reloader = &fakeReloader{} })
		rr := performRequest(t, srv, http.MethodGet, "/")
		assert.Contains(t, rr.Body.String(), "config file changed on disk, not reloaded")
	})
}
