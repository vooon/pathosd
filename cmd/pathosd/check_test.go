package main

import (
	"bytes"
	"context"
	"encoding/json"
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
	"github.com/vooon/pathosd/internal/config"
)

func writeCheckConfig(t *testing.T, vips string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pathosd.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`schema: v1
router: {asn: 65001, router_id: 192.0.2.1, local_address: 192.0.2.1}
bgp: {neighbors: [{name: p, address: 192.0.2.2, peer_asn: 65002}]}
`+vips), 0o600))
	return path
}

func closedPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	return port
}

func TestCheckCmd(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			time.Sleep(300 * time.Millisecond)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(ts.Close)
	host, port, err := net.SplitHostPort(strings.TrimPrefix(ts.URL, "http://"))
	require.NoError(t, err)
	dead := closedPort(t)

	cfgPath := writeCheckConfig(t, fmt.Sprintf(`vips:
  - name: web
    prefix: 10.99.0.1/32
    checks_policy: "http || tcp"
    checks:
      - type: http
        interval: 1s
        timeout: 500ms
        http: {url: /, host: %[1]s, port: %[2]s, response_jq: ".ok"}
      - type: tcp
        interval: 1s
        timeout: 200ms
        tcp: {host: 127.0.0.1, port: %[3]d}
  - name: slow
    prefix: 10.99.0.2/32
    check:
      type: http
      interval: 1s
      timeout: 100ms
      http: {url: /slow, host: %[1]s, port: %[2]s}
    policy: {fail_action: withdraw}
`, host, port, dead))

	load := func(t *testing.T) *config.Config {
		cfg, err := config.Load(cfgPath)
		require.NoError(t, err)
		return cfg
	}

	t.Run("all vips, policy combines, text output", func(t *testing.T) {
		cmd := &CheckCmd{Config: cfgPath, Format: "text"}
		reports, err := cmd.execute(context.Background(), load(t))
		require.NoError(t, err)
		require.Len(t, reports, 2)

		web := reports[0]
		assert.True(t, web.Healthy, "http passes, tcp fails, policy is ||")
		assert.Equal(t, "http || tcp", web.Policy)
		assert.True(t, web.Checks[0].Success)
		assert.False(t, web.Checks[1].Success)
		assert.Contains(t, web.Checks[1].Detail, "refused")

		slow := reports[1]
		assert.False(t, slow.Healthy)
		assert.True(t, slow.Checks[0].TimedOut)

		var out bytes.Buffer
		require.NoError(t, cmd.print(&out, reports))
		text := out.String()
		assert.Contains(t, text, "VIP web (10.99.0.1/32): healthy [http || tcp]")
		assert.Contains(t, text, "OK")
		assert.Contains(t, text, "FAIL")
		assert.Contains(t, text, "VIP slow (10.99.0.2/32): unhealthy [all]")
		assert.Contains(t, text, "TIMEOUT")
	})

	t.Run("timeout override rescues the slow check", func(t *testing.T) {
		cmd := &CheckCmd{Config: cfgPath, VIP: []string{"slow"}, Timeout: 2 * time.Second}
		reports, err := cmd.execute(context.Background(), load(t))
		require.NoError(t, err)
		require.Len(t, reports, 1)
		assert.True(t, reports[0].Healthy)
	})

	t.Run("check filter: unselected checks count as failed", func(t *testing.T) {
		cmd := &CheckCmd{Config: cfgPath, VIP: []string{"web"}, Check: []string{"tcp"}}
		reports, err := cmd.execute(context.Background(), load(t))
		require.NoError(t, err)
		assert.False(t, reports[0].Healthy)
		assert.False(t, reports[0].Checks[0].Ran)
		assert.Equal(t, "not selected", reports[0].Checks[0].Detail)
		assert.True(t, reports[0].Checks[1].Ran)
	})

	t.Run("json output", func(t *testing.T) {
		cmd := &CheckCmd{Config: cfgPath, VIP: []string{"web"}, Format: "json"}
		reports, err := cmd.execute(context.Background(), load(t))
		require.NoError(t, err)
		var out bytes.Buffer
		require.NoError(t, cmd.print(&out, reports))
		var decoded []struct {
			VIP     string `json:"vip"`
			Healthy bool   `json:"healthy"`
			Checks  []struct {
				Name    string `json:"name"`
				Success bool   `json:"success"`
				Details struct {
					StatusCode int `json:"status_code"`
				} `json:"details"`
			} `json:"checks"`
		}
		require.NoError(t, json.Unmarshal(out.Bytes(), &decoded))
		require.Len(t, decoded, 1)
		assert.Equal(t, "web", decoded[0].VIP)
		assert.Equal(t, 200, decoded[0].Checks[0].Details.StatusCode)
	})

	t.Run("unknown names are errors", func(t *testing.T) {
		_, err := (&CheckCmd{Config: cfgPath, VIP: []string{"nope"}}).execute(context.Background(), load(t))
		require.ErrorContains(t, err, `no VIP named "nope"`)
		_, err = (&CheckCmd{Config: cfgPath, Check: []string{"dns"}}).execute(context.Background(), load(t))
		require.ErrorContains(t, err, `no check named "dns"`)
	})

	t.Run("exit codes follow test(1): 0 healthy, 1 unhealthy, 200 internal error", func(t *testing.T) {
		code := func(err error) int {
			if err == nil {
				return 0
			}
			var ec *exitCodeError
			require.ErrorAs(t, err, &ec)
			return ec.code
		}
		assert.Equal(t, 0, code((&CheckCmd{Config: cfgPath, VIP: []string{"web"}, Format: "json"}).Run()))
		err := (&CheckCmd{Config: cfgPath, VIP: []string{"slow"}, Format: "json"}).Run()
		assert.Equal(t, 1, code(err))
		assert.Equal(t, "1 of 1 VIPs unhealthy", err.Error())
		err = (&CheckCmd{Config: cfgPath, VIP: []string{"nope"}, Format: "json"}).Run()
		assert.Equal(t, 200, code(err))
		assert.Contains(t, err.Error(), `no VIP named "nope"`)
		err = (&CheckCmd{Config: filepath.Join(t.TempDir(), "missing.yaml"), Format: "json"}).Run()
		assert.Equal(t, 200, code(err))
	})
}
