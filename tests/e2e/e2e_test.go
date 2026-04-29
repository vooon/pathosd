//go:build e2e

package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	e2eNamespace      = "pathosd-e2e"
	pathosdService    = "pathosd"
	pathosdRemotePort = 59179

	webVIPPrefix   = "10.100.1.1/32"
	dnsVIPPrefix   = "10.100.2.1/32"
	tcpVIPPrefix   = "10.100.3.1/32"
	udpVIPPrefix   = "10.100.4.1/32"
	httpsVIPPrefix = "10.100.5.1/32"
	webVIPLockFile = "/tmp/pathosd-web-vip-drain.lock"
)

var pathosdAPIBaseURL string
var communityValueRE = regexp.MustCompile(`\b\d+:\d+\b`)

// DaemonStatus is a minimal shape of /status used by e2e assertions.
type DaemonStatus struct {
	RouterID string       `json:"router_id"`
	ASN      uint32       `json:"asn"`
	Peers    []PeerStatus `json:"peers"`
	VIPs     []VIPStatus  `json:"vips"`
}

type PeerStatus struct {
	Name         string `json:"name"`
	Address      string `json:"address"`
	PeerASN      uint32 `json:"peer_asn"`
	SessionState string `json:"session_state"`
	Required     bool   `json:"required"`
}

type VIPStatus struct {
	Name            string `json:"name"`
	Prefix          string `json:"prefix"`
	State           int    `json:"state"`
	StateName       string `json:"state_name"`
	Health          int    `json:"health"`
	HealthName      string `json:"health_name"`
	ConsecutiveOK   int    `json:"consecutive_ok"`
	ConsecutiveFail int    `json:"consecutive_fail"`
	CheckType       string `json:"check_type"`
}

func TestE2E(t *testing.T) {
	if _, err := exec.LookPath("kubectl"); err != nil {
		t.Skipf("kubectl not found in PATH: %v", err)
	}

	// Keep stack reusable for subsequent runs.
	t.Cleanup(func() {
		_, _ = kubectlNoFail("-n", e2eNamespace, "scale", "deployment/nginx", "--replicas=1")
		_, _ = kubectlNoFail("-n", e2eNamespace, "scale", "deployment/nginx-tls", "--replicas=1")
		_, _ = kubectlNoFail("-n", e2eNamespace, "scale", "deployment/coredns", "--replicas=1")
		_, _ = kubectlNoFail("-n", e2eNamespace, "scale", "deployment/syslog", "--replicas=1")
	})

	t.Run("pods_ready", func(t *testing.T) {
		waitForPodReady(t, e2eNamespace, "app=frr", 120*time.Second)
		waitForPodReady(t, e2eNamespace, "app=nginx", 120*time.Second)
		waitForPodReady(t, e2eNamespace, "app=nginx-tls", 120*time.Second)
		waitForPodReady(t, e2eNamespace, "app=coredns", 120*time.Second)
		waitForPodReady(t, e2eNamespace, "app=syslog", 120*time.Second)
		waitForPodReady(t, e2eNamespace, "app=pathosd", 120*time.Second)
	})

	pathosdAPIBaseURL = startPortForward(t, e2eNamespace, pathosdService, pathosdRemotePort)

	t.Run("healthz", func(t *testing.T) {
		status, body := apiRequest(t, http.MethodGet, "/healthz", nil)
		require.Equal(t, http.StatusOK, status)
		assert.Contains(t, string(body), "ok")
	})

	t.Run("readyz_established", func(t *testing.T) {
		waitForCondition(t, "pathosd /readyz to report established required peers", 60*time.Second, 1*time.Second, func() bool {
			status, _, err := apiRequestNoFail(http.MethodGet, "/readyz", nil)
			return err == nil && status == http.StatusOK
		})
	})

	t.Run("vips_announced", func(t *testing.T) {
		// udp-vip may take a bit longer: socat starts after apk add (~5s).
		waitForCondition(t, "all VIPs announced", 60*time.Second, 1*time.Second, func() bool {
			status, err := getPathosdStatusNoFail()
			if err != nil {
				return false
			}
			return vipStateFromStatus(status, "web-vip") == "announced" &&
				vipStateFromStatus(status, "dns-vip") == "announced" &&
				vipStateFromStatus(status, "tcp-vip") == "announced" &&
				vipStateFromStatus(status, "udp-vip") == "announced" &&
				vipStateFromStatus(status, "https-vip") == "announced"
		})
	})

	t.Run("frr_receives_routes", func(t *testing.T) {
		waitForCondition(t, "FRR receives all routes", 45*time.Second, 1*time.Second, func() bool {
			routes, err := frrRoutesNoFail()
			if err != nil {
				return false
			}
			_, webOK := routes[webVIPPrefix]
			_, dnsOK := routes[dnsVIPPrefix]
			_, tcpOK := routes[tcpVIPPrefix]
			_, udpOK := routes[udpVIPPrefix]
			_, httpsOK := routes[httpsVIPPrefix]
			return webOK && dnsOK && tcpOK && udpOK && httpsOK
		})

		routes := frrRoutes(t)
		webPath := firstRoutePath(t, routes, webVIPPrefix)
		dnsPath := firstRoutePath(t, routes, dnsVIPPrefix)
		tcpPath := firstRoutePath(t, routes, tcpVIPPrefix)
		udpPath := firstRoutePath(t, routes, udpVIPPrefix)
		httpsPath := firstRoutePath(t, routes, httpsVIPPrefix)

		assert.Contains(t, extractASPath(webPath), "65100")
		assert.Contains(t, extractASPath(dnsPath), "65100")
		assert.Contains(t, extractASPath(tcpPath), "65100")
		assert.Contains(t, extractASPath(udpPath), "65100")
		assert.Contains(t, extractASPath(httpsPath), "65100")
	})

	t.Run("web_vip_lock_file_pessimized", func(t *testing.T) {
		t.Cleanup(func() {
			_, _ = pathosdExecNoFail("rm -f " + webVIPLockFile)
		})

		pathosdExec(t, "rm -f "+webVIPLockFile)
		pathosdExec(t, "touch "+webVIPLockFile)

		waitForCondition(t, "web-vip pessimized by lower_priority_file while healthy", 30*time.Second, 1*time.Second, func() bool {
			status, err := getPathosdStatusNoFail()
			if err != nil {
				return false
			}
			return vipStateFromStatus(status, "web-vip") == "pessimized" &&
				vipHealthFromStatus(status, "web-vip") == "healthy" &&
				vipStateFromStatus(status, "dns-vip") == "announced"
		})

		webPrefixPath := frrPrefixPath(t, webVIPPrefix)
		assert.GreaterOrEqual(t, countASN(extractASPath(webPrefixPath), "65100"), 6)
		assert.Contains(t, extractCommunity(webPrefixPath), "65100:666")

		pathosdExec(t, "rm -f "+webVIPLockFile)

		waitForCondition(t, "web-vip recovers to announced after lower_priority_file removal", 30*time.Second, 1*time.Second, func() bool {
			status, err := getPathosdStatusNoFail()
			if err != nil {
				return false
			}
			return vipStateFromStatus(status, "web-vip") == "announced" &&
				vipStateFromStatus(status, "dns-vip") == "announced"
		})
	})

	t.Run("nginx_down_web_vip_pessimized", func(t *testing.T) {
		scaleDeploy(t, e2eNamespace, "nginx", 0)

		// udp-vip checks syslog (independent of nginx) so it remains announced.
		waitForCondition(t, "web-vip pessimized, tcp-vip withdrawn, dns-vip and udp-vip remain announced", 30*time.Second, 1*time.Second, func() bool {
			status, err := getPathosdStatusNoFail()
			if err != nil {
				return false
			}
			return vipStateFromStatus(status, "web-vip") == "pessimized" &&
				vipStateFromStatus(status, "tcp-vip") == "withdrawn" &&
				vipStateFromStatus(status, "dns-vip") == "announced" &&
				vipStateFromStatus(status, "udp-vip") == "announced"
		})
	})

	t.Run("frr_pessimized_route", func(t *testing.T) {
		waitForCondition(t, "FRR shows prepended AS path for web-vip", 30*time.Second, 1*time.Second, func() bool {
			routes, err := frrRoutesNoFail()
			if err != nil {
				return false
			}
			path, ok := firstRoutePathNoFail(routes, webVIPPrefix)
			if !ok {
				return false
			}
			return countASN(extractASPath(path), "65100") >= 6
		})

		routes := frrRoutes(t)
		webPath := firstRoutePath(t, routes, webVIPPrefix)
		webPrefixPath := frrPrefixPath(t, webVIPPrefix)
		extractedASPath := extractASPath(webPrefixPath)
		extractedCommunity := extractCommunity(webPrefixPath)

		assert.GreaterOrEqual(t, countASN(extractedASPath, "65100"), 6)
		if !assert.Contains(t, extractedCommunity, "65100:666", "extracted community=%q", extractedCommunity) {
			t.Logf("selected web-vip path from show ... unicast json:\n%s", prettyJSON(webPath))
			t.Logf("selected web-vip path from show ... unicast <prefix> json:\n%s", prettyJSON(webPrefixPath))
			t.Logf("all web-vip paths JSON:\n%s", prettyJSON(routes[webVIPPrefix]))
			t.Logf("raw FRR web-vip JSON:\n%s", frrShowBGPPrefix(t, webVIPPrefix))
			t.Logf("raw FRR full BGP JSON:\n%s", frrShowBGP(t))
		}

		// tcp-vip route should be withdrawn from FRR while nginx is down.
		_, tcpExists := routes[tcpVIPPrefix]
		assert.False(t, tcpExists, "tcp-vip route should be withdrawn from FRR when nginx is down")

		// udp-vip checks syslog — unaffected by nginx being down.
		_, udpOK := routes[udpVIPPrefix]
		assert.True(t, udpOK, "udp-vip route should remain in FRR when nginx is down")
	})

	t.Run("nginx_up_web_vip_recovers", func(t *testing.T) {
		scaleDeploy(t, e2eNamespace, "nginx", 1)
		waitForPodReady(t, e2eNamespace, "app=nginx", 120*time.Second)

		waitForCondition(t, "web-vip and tcp-vip recover to announced", 45*time.Second, 1*time.Second, func() bool {
			status, err := getPathosdStatusNoFail()
			if err != nil {
				return false
			}
			return vipStateFromStatus(status, "web-vip") == "announced" &&
				vipStateFromStatus(status, "tcp-vip") == "announced" &&
				vipStateFromStatus(status, "udp-vip") == "announced"
		})
	})

	t.Run("coredns_down_dns_vip_withdrawn", func(t *testing.T) {
		scaleDeploy(t, e2eNamespace, "coredns", 0)

		// udp-vip checks syslog (not coredns) so it remains announced.
		waitForCondition(t, "dns-vip withdrawn, web-vip, tcp-vip and udp-vip remain announced", 30*time.Second, 1*time.Second, func() bool {
			status, err := getPathosdStatusNoFail()
			if err != nil {
				return false
			}
			return vipStateFromStatus(status, "dns-vip") == "withdrawn" &&
				vipStateFromStatus(status, "web-vip") == "announced" &&
				vipStateFromStatus(status, "tcp-vip") == "announced" &&
				vipStateFromStatus(status, "udp-vip") == "announced"
		})
	})

	t.Run("frr_route_withdrawn", func(t *testing.T) {
		waitForCondition(t, "FRR withdraws dns-vip route", 30*time.Second, 1*time.Second, func() bool {
			routes, err := frrRoutesNoFail()
			if err != nil {
				return false
			}
			_, webOK := routes[webVIPPrefix]
			_, tcpOK := routes[tcpVIPPrefix]
			_, udpOK := routes[udpVIPPrefix]
			_, dnsExists := routes[dnsVIPPrefix]
			return webOK && tcpOK && udpOK && !dnsExists
		})

		routes := frrRoutes(t)
		_, webOK := routes[webVIPPrefix]
		_, dnsExists := routes[dnsVIPPrefix]
		_, tcpOK := routes[tcpVIPPrefix]
		_, udpOK := routes[udpVIPPrefix]
		assert.True(t, webOK, "web-vip route should remain present")
		assert.False(t, dnsExists, "dns-vip route should be withdrawn")
		assert.True(t, tcpOK, "tcp-vip route should remain present")
		assert.True(t, udpOK, "udp-vip route should remain present")
	})

	t.Run("coredns_up_dns_vip_recovers", func(t *testing.T) {
		scaleDeploy(t, e2eNamespace, "coredns", 1)
		waitForPodReady(t, e2eNamespace, "app=coredns", 120*time.Second)

		waitForCondition(t, "all VIPs recover to announced", 45*time.Second, 1*time.Second, func() bool {
			status, err := getPathosdStatusNoFail()
			if err != nil {
				return false
			}
			return vipStateFromStatus(status, "dns-vip") == "announced" &&
				vipStateFromStatus(status, "web-vip") == "announced" &&
				vipStateFromStatus(status, "tcp-vip") == "announced" &&
				vipStateFromStatus(status, "udp-vip") == "announced"
		})
	})

	t.Run("syslog_down_udp_vip_withdrawn", func(t *testing.T) {
		scaleDeploy(t, e2eNamespace, "syslog", 0)

		// When the syslog pod is gone the headless Service DNS returns no records.
		// net.Dial("udp", "syslog...:514") fails immediately → check fails → withdrawn.
		// Allow extra time for DNS TTL expiry (up to ~30s) + fall threshold (6s).
		waitForCondition(t, "udp-vip withdrawn, other VIPs remain announced", 60*time.Second, 1*time.Second, func() bool {
			status, err := getPathosdStatusNoFail()
			if err != nil {
				return false
			}
			return vipStateFromStatus(status, "udp-vip") == "withdrawn" &&
				vipStateFromStatus(status, "web-vip") == "announced" &&
				vipStateFromStatus(status, "dns-vip") == "announced" &&
				vipStateFromStatus(status, "tcp-vip") == "announced"
		})

		waitForCondition(t, "FRR withdraws udp-vip route", 30*time.Second, 1*time.Second, func() bool {
			routes, err := frrRoutesNoFail()
			if err != nil {
				return false
			}
			_, udpExists := routes[udpVIPPrefix]
			return !udpExists
		})
	})

	t.Run("syslog_up_udp_vip_recovers", func(t *testing.T) {
		scaleDeploy(t, e2eNamespace, "syslog", 1)
		waitForPodReady(t, e2eNamespace, "app=syslog", 120*time.Second)

		// Extra time: apk add socat (~5s) + DNS propagation + rise threshold.
		waitForCondition(t, "udp-vip recovers to announced", 60*time.Second, 1*time.Second, func() bool {
			status, err := getPathosdStatusNoFail()
			if err != nil {
				return false
			}
			return vipStateFromStatus(status, "udp-vip") == "announced"
		})

		waitForCondition(t, "FRR receives udp-vip route", 30*time.Second, 1*time.Second, func() bool {
			routes, err := frrRoutesNoFail()
			if err != nil {
				return false
			}
			_, udpOK := routes[udpVIPPrefix]
			return udpOK
		})
	})

	t.Run("nginx_tls_down_https_vip_withdrawn", func(t *testing.T) {
		scaleDeploy(t, e2eNamespace, "nginx-tls", 0)

		waitForCondition(t, "https-vip withdrawn when nginx-tls is down", 30*time.Second, 1*time.Second, func() bool {
			status, err := getPathosdStatusNoFail()
			if err != nil {
				return false
			}
			return vipStateFromStatus(status, "https-vip") == "withdrawn" &&
				vipStateFromStatus(status, "web-vip") == "announced"
		})

		waitForCondition(t, "FRR withdraws https-vip route", 30*time.Second, 1*time.Second, func() bool {
			routes, err := frrRoutesNoFail()
			if err != nil {
				return false
			}
			_, httpsExists := routes[httpsVIPPrefix]
			return !httpsExists
		})
	})

	t.Run("nginx_tls_up_https_vip_recovers", func(t *testing.T) {
		scaleDeploy(t, e2eNamespace, "nginx-tls", 1)
		waitForPodReady(t, e2eNamespace, "app=nginx-tls", 120*time.Second)

		waitForCondition(t, "https-vip recovers to announced", 45*time.Second, 1*time.Second, func() bool {
			status, err := getPathosdStatusNoFail()
			if err != nil {
				return false
			}
			return vipStateFromStatus(status, "https-vip") == "announced"
		})

		waitForCondition(t, "FRR receives https-vip route", 30*time.Second, 1*time.Second, func() bool {
			routes, err := frrRoutesNoFail()
			if err != nil {
				return false
			}
			_, httpsOK := routes[httpsVIPPrefix]
			return httpsOK
		})
	})

	t.Run("metrics_endpoint", func(t *testing.T) {
		status, body := apiRequest(t, http.MethodGet, "/metrics", nil)
		require.Equal(t, http.StatusOK, status)

		metricsText := string(body)
		assert.Contains(t, metricsText, "pathosd_check_duration_seconds")
		assert.Contains(t, metricsText, "pathosd_check_last_result")
		assert.Contains(t, metricsText, "pathosd_check_total")
		assert.Contains(t, metricsText, "pathosd_vip_state")
		assert.Contains(t, metricsText, "pathosd_build_info")
	})

	t.Run("trigger_check_api", func(t *testing.T) {
		status, body := apiRequest(t, http.MethodPost, "/api/v1/vips/web-vip/check", nil)
		require.Equal(t, http.StatusOK, status)
		assert.Contains(t, string(body), "\"vip\":\"web-vip\"")

		status, body = apiRequest(t, http.MethodPost, "/api/v1/vips/nonexistent/check", nil)
		require.Equal(t, http.StatusNotFound, status)
		assert.Contains(t, string(body), "VIP not found")
	})
}

// kubectl runs a kubectl command and returns stdout. Fails the test on error.
func kubectl(t *testing.T, args ...string) string {
	t.Helper()
	t.Logf("kubectl %s", strings.Join(args, " "))

	cmd := exec.Command("kubectl", args...)
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "kubectl %s failed:\n%s", strings.Join(args, " "), string(out))
	return string(out)
}

// kubectlNoFail runs kubectl and returns stdout + error without failing.
func kubectlNoFail(args ...string) (string, error) {
	cmd := exec.Command("kubectl", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("kubectl %s: %w\n%s", strings.Join(args, " "), err, string(out))
	}
	return string(out), nil
}

// waitForPodReady waits until at least one pod matching the label selector is Ready.
func waitForPodReady(t *testing.T, namespace, labelSelector string, timeout time.Duration) {
	t.Helper()
	kubectl(
		t,
		"-n", namespace,
		"wait",
		"--for=condition=Ready",
		"pod",
		"-l", labelSelector,
		"--timeout="+timeout.String(),
	)
}

// waitForCondition polls fn every interval until it returns true or timeout expires.
func waitForCondition(t *testing.T, description string, timeout, interval time.Duration, fn func() bool) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for {
		if fn() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s after %s", description, timeout)
		}
		time.Sleep(interval)
	}
}

// scaleDeploy scales a deployment to the given replica count.
func scaleDeploy(t *testing.T, namespace, name string, replicas int) {
	t.Helper()
	kubectl(t, "-n", namespace, "scale", "deployment/"+name, "--replicas="+strconv.Itoa(replicas))
}

func startPortForward(t *testing.T, namespace, svcName string, remotePort int) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	localPort := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(
		ctx,
		"kubectl",
		"-n", namespace,
		"port-forward",
		"--address", "127.0.0.1",
		"svc/"+svcName,
		fmt.Sprintf("%d:%d", localPort, remotePort),
	)

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	require.NoError(t, cmd.Start())

	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
	})

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", localPort)
	waitForCondition(t, "kubectl port-forward to become reachable", 20*time.Second, 250*time.Millisecond, func() bool {
		status, _, err := apiRequestAbsoluteNoFail(baseURL, http.MethodGet, "/healthz", nil)
		return err == nil && status == http.StatusOK
	})

	return baseURL
}

func apiRequest(t *testing.T, method, path string, body io.Reader) (int, []byte) {
	t.Helper()
	status, respBody, err := apiRequestNoFail(method, path, body)
	require.NoErrorf(t, err, "%s %s failed", method, path)
	return status, respBody
}

func apiRequestNoFail(method, path string, body io.Reader) (int, []byte, error) {
	if pathosdAPIBaseURL == "" {
		return 0, nil, fmt.Errorf("pathosd API base URL is not initialized")
	}
	return apiRequestAbsoluteNoFail(pathosdAPIBaseURL, method, path, body)
}

func apiRequestAbsoluteNoFail(baseURL, method, path string, body io.Reader) (int, []byte, error) {
	req, err := http.NewRequest(method, baseURL+path, body)
	if err != nil {
		return 0, nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, err
	}

	return resp.StatusCode, respBody, nil
}

// getPathosdStatus fetches /status from pathosd via kubectl port-forward.
// Returns parsed DaemonStatus.
func getPathosdStatus(t *testing.T) DaemonStatus {
	t.Helper()
	status, body := apiRequest(t, http.MethodGet, "/status", nil)
	require.Equal(t, http.StatusOK, status)

	var out DaemonStatus
	require.NoError(t, json.Unmarshal(body, &out))
	return out
}

func getPathosdStatusNoFail() (DaemonStatus, error) {
	status, body, err := apiRequestNoFail(http.MethodGet, "/status", nil)
	if err != nil {
		return DaemonStatus{}, err
	}
	if status != http.StatusOK {
		return DaemonStatus{}, fmt.Errorf("/status returned HTTP %d: %s", status, string(body))
	}

	var out DaemonStatus
	if err := json.Unmarshal(body, &out); err != nil {
		return DaemonStatus{}, err
	}
	return out, nil
}

// getVIPState returns the state_name of a specific VIP from /status.
func getVIPState(t *testing.T, vipName string) string {
	t.Helper()
	return vipStateFromStatus(getPathosdStatus(t), vipName)
}

func vipStateFromStatus(status DaemonStatus, vipName string) string {
	for _, vip := range status.VIPs {
		if vip.Name == vipName {
			return vip.StateName
		}
	}
	return ""
}

func vipHealthFromStatus(status DaemonStatus, vipName string) string {
	for _, vip := range status.VIPs {
		if vip.Name == vipName {
			return vip.HealthName
		}
	}
	return ""
}

// frrShowBGP runs "vtysh -c 'show bgp ipv4 unicast json'" on the FRR pod
// and returns the raw JSON output.
func frrShowBGP(t *testing.T) string {
	t.Helper()
	return kubectl(
		t,
		"exec", "-n", e2eNamespace, "frr", "--",
		"vtysh", "-c", "show bgp ipv4 unicast json",
	)
}

func frrShowBGPPrefix(t *testing.T, prefix string) string {
	t.Helper()
	return kubectl(
		t,
		"exec", "-n", e2eNamespace, "frr", "--",
		"vtysh", "-c", fmt.Sprintf("show bgp ipv4 unicast %s json", prefix),
	)
}

func pathosdExec(t *testing.T, script string) string {
	t.Helper()
	return kubectl(
		t,
		"-n", e2eNamespace,
		"exec", "deploy/pathosd", "--",
		"/bin/sh", "-ec", script,
	)
}

func pathosdExecNoFail(script string) (string, error) {
	return kubectlNoFail(
		"-n", e2eNamespace,
		"exec", "deploy/pathosd", "--",
		"/bin/sh", "-ec", script,
	)
}

func frrRoutes(t *testing.T) map[string][]map[string]interface{} {
	t.Helper()
	routes, err := frrRoutesNoFail()
	require.NoError(t, err)
	return routes
}

func frrRoutesNoFail() (map[string][]map[string]interface{}, error) {
	raw, err := kubectlNoFail(
		"exec", "-n", e2eNamespace, "frr", "--",
		"vtysh", "-c", "show bgp ipv4 unicast json",
	)
	if err != nil {
		return nil, err
	}

	var payload struct {
		Routes map[string][]map[string]interface{} `json:"routes"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, fmt.Errorf("unmarshal FRR JSON: %w", err)
	}
	if payload.Routes == nil {
		payload.Routes = map[string][]map[string]interface{}{}
	}

	return payload.Routes, nil
}

func frrPrefixPath(t *testing.T, prefix string) map[string]interface{} {
	t.Helper()
	path, err := frrPrefixPathNoFail(prefix)
	require.NoError(t, err)
	require.NotNilf(t, path, "route for prefix %s not found", prefix)
	return path
}

func frrPrefixPathNoFail(prefix string) (map[string]interface{}, error) {
	raw, err := kubectlNoFail(
		"exec", "-n", e2eNamespace, "frr", "--",
		"vtysh", "-c", fmt.Sprintf("show bgp ipv4 unicast %s json", prefix),
	)
	if err != nil {
		return nil, err
	}

	var payload struct {
		Paths []map[string]interface{} `json:"paths"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, fmt.Errorf("unmarshal FRR prefix JSON: %w", err)
	}

	path, ok := firstPathNoFail(payload.Paths)
	if !ok {
		return nil, nil
	}
	return path, nil
}

func firstRoutePath(t *testing.T, routes map[string][]map[string]interface{}, prefix string) map[string]interface{} {
	t.Helper()
	path, ok := firstRoutePathNoFail(routes, prefix)
	require.Truef(t, ok, "route for prefix %s not found", prefix)
	return path
}

func firstRoutePathNoFail(routes map[string][]map[string]interface{}, prefix string) (map[string]interface{}, bool) {
	paths, ok := routes[prefix]
	if !ok || len(paths) == 0 {
		return nil, false
	}

	return firstPathNoFail(paths)
}

func firstPathNoFail(paths []map[string]interface{}) (map[string]interface{}, bool) {
	if len(paths) == 0 {
		return nil, false
	}

	for _, p := range paths {
		switch bestpath := p["bestpath"].(type) {
		case map[string]interface{}:
			overall, ok := bestpath["overall"].(bool)
			if ok && overall {
				return p, true
			}
		case bool:
			if bestpath {
				return p, true
			}
		}
	}

	return paths[0], true
}

func extractASPath(path map[string]interface{}) string {
	if s, ok := path["path"].(string); ok && s != "" {
		return s
	}

	aspath, ok := path["aspath"].(map[string]interface{})
	if !ok {
		if s, ok := path["aspath"].(string); ok {
			return s
		}
		return ""
	}

	s, _ := aspath["string"].(string)
	return s
}

func extractCommunity(path map[string]interface{}) string {
	seen := map[string]struct{}{}

	if comm, ok := path["community"].(map[string]interface{}); ok {
		s, _ := comm["string"].(string)
		if s != "" {
			for _, m := range communityValueRE.FindAllString(s, -1) {
				seen[m] = struct{}{}
			}
		}
	}
	if comms, ok := path["communities"].(map[string]interface{}); ok {
		s, _ := comms["string"].(string)
		if s != "" {
			for _, m := range communityValueRE.FindAllString(s, -1) {
				seen[m] = struct{}{}
			}
		}
	}
	if comms, ok := path["community"].(string); ok {
		for _, m := range communityValueRE.FindAllString(comms, -1) {
			seen[m] = struct{}{}
		}
	}

	collectCommunityValues(path, false, seen)

	if len(seen) == 0 {
		return ""
	}

	values := make([]string, 0, len(seen))
	for comm := range seen {
		values = append(values, comm)
	}
	sort.Strings(values)
	return strings.Join(values, " ")
}

func collectCommunityValues(value interface{}, communityContext bool, seen map[string]struct{}) {
	switch v := value.(type) {
	case map[string]interface{}:
		for key, nested := range v {
			keyIsCommunity := communityContext || strings.Contains(strings.ToLower(key), "community")
			collectCommunityValues(nested, keyIsCommunity, seen)
		}
	case []interface{}:
		for _, nested := range v {
			collectCommunityValues(nested, communityContext, seen)
		}
	case string:
		for _, match := range communityValueRE.FindAllString(v, -1) {
			seen[match] = struct{}{}
		}
	case float64:
		if !communityContext {
			return
		}
		if v < 0 || v > float64(^uint32(0)) || math.Trunc(v) != v {
			return
		}
		n := uint32(v)
		seen[fmt.Sprintf("%d:%d", n>>16, n&0xffff)] = struct{}{}
	}
}

func countASN(asPath, asn string) int {
	count := 0
	for _, part := range strings.Fields(asPath) {
		if part == asn {
			count++
		}
	}
	return count
}

func prettyJSON(v interface{}) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("JSON marshal error: %v", err)
	}
	return string(b)
}
