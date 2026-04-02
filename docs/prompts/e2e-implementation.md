# Prompt: Implement E2E Tests for pathosd

You are implementing end-to-end tests for `pathosd` — a single-process, pure-Go health-aware BGP VIP announcer with embedded GoBGP v3.

## Project Context

- **Module**: `github.com/vooon/pathosd`, Go 1.26
- **Branch**: `integration-tests`
- **Test framework**: `github.com/stretchr/testify` (assert/require)
- **Build tag**: `//go:build e2e`
- **Container runtime**: Docker/podman via k3d (k3s in containers)

## What pathosd Does

pathosd runs health checks (HTTP, DNS, ping) against services and announces/withdraws/pessimizes BGP VIP routes based on check results. VIPs start **withdrawn** (fail-closed). After `rise` consecutive successful checks, a VIP transitions to **announced**. After `fall` consecutive failures, it either **withdraws** (route removed) or **pessimizes** (AS-path prepend + communities) depending on the VIP's `fail_action` policy.

## Architecture Under Test

```
┌──────────────┐     BGP (179)     ┌───────────────┐
│   pathosd    │◄─────────────────►│  FRR (bgpd)   │
│  ASN 65100   │                   │  ASN 65200    │
│              │                   │               │
│  HTTP check ─┼──── GET /healthz ►│               │
│              │                   └───────────────┘
│  DNS check ──┼──── A example.test
│              │         │
└──────────────┘         ▼
                  ┌──────────────┐
                  │   CoreDNS    │
                  └──────────────┘
                  ┌──────────────┐
                  │    nginx     │
                  └──────────────┘
```

All components run as pods in a `pathosd-e2e` Kubernetes namespace (k3d/k3s cluster).

## API Endpoints Available for Testing

- `GET /healthz` → always 200 `{"status":"ok"}`
- `GET /readyz` → 200 when all `required` BGP peers are established, 503 otherwise
- `GET /status` → full daemon status JSON (see DaemonStatus struct below)
- `GET /metrics` → Prometheus metrics text
- `POST /api/v1/vips/{name}/check` → trigger ad-hoc check, returns result

### DaemonStatus JSON Structure
```json
{
  "router_id": "10.100.0.1",
  "asn": 65100,
  "version": "...",
  "commit": "...",
  "start_time": "...",
  "peers": [
    {"name": "frr", "address": "...", "peer_asn": 65200, "session_state": "established", "required": true}
  ],
  "vips": [
    {
      "name": "web-vip",
      "prefix": "10.100.1.1/32",
      "state": 1,
      "state_name": "announced",
      "health": 1,
      "health_name": "healthy",
      "consecutive_ok": 3,
      "consecutive_fail": 0,
      "last_check_success": true,
      "last_check_detail": "HTTP 200",
      "check_type": "http"
    }
  ]
}
```

## Deliverables

Create the following files:

### 1. Kubernetes Manifests (`tests/e2e/manifests/`)

#### `namespace.yaml`
```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: pathosd-e2e
```

#### `frr-configmap.yaml`
FRR configuration:
- Router bgpd only (no other daemons)
- ASN 65200, router-id 10.200.0.1
- Accept connections from any peer in ASN 65100
- Enable `bgp listen range` for the pod CIDR so pathosd can connect from any pod IP
- Log to stdout

FRR `bgpd.conf` should look approximately like:
```
frr version 10.3
frr defaults traditional
hostname frr
log stdout informational

router bgp 65200
 bgp router-id 10.200.0.1
 bgp listen range 10.42.0.0/16 peer-group pathosd
 neighbor pathosd peer-group
 neighbor pathosd remote-as 65100
 address-family ipv4 unicast
  neighbor pathosd activate
 exit-address-family
```

Also need `daemons` file enabling only bgpd:
```
bgpd=yes
ospfd=no
ospf6d=no
ripd=no
ripngd=no
isisd=no
pimd=no
ldpd=no
nhrpd=no
eigrpd=no
babeld=no
sharpd=no
pbrd=no
bfdd=no
fabricd=no
vrrpd=no
pathd=no
```

#### `frr-pod.yaml`
- Image: `quay.io/frrouting/frr:10.3.1`
- Single Pod (not Deployment — we need stable name for kubectl exec)
- Mount configmap as `/etc/frr/`
- Service: ClusterIP, port 179
- Labels: `app: frr`

#### `nginx-configmap.yaml`
nginx.conf snippet:
```nginx
server {
    listen 80;
    location /healthz {
        return 200 '{"status":"ok"}';
        add_header Content-Type application/json;
    }
}
```

#### `nginx-deployment.yaml`
- Image: `nginx:1.27-alpine`
- Replicas: 1
- Mount configmap to `/etc/nginx/conf.d/`
- Service: ClusterIP, port 80
- Labels: `app: nginx`

#### `coredns-configmap.yaml`
Corefile:
```
example.test:53 {
    file /etc/coredns/db.example.test
    log
}
```

Zone file `db.example.test`:
```
$ORIGIN example.test.
@       3600    IN  SOA  ns.example.test. admin.example.test. 2024010100 3600 600 86400 60
        3600    IN  NS   ns.example.test.
ns      3600    IN  A    10.100.99.1
@       60      IN  A    10.100.99.2
www     60      IN  A    10.100.99.3
```

#### `coredns-deployment.yaml`
- Image: `coredns/coredns:1.12.1`
- Args: `-conf /etc/coredns/Corefile`
- Replicas: 1
- Mount configmap to `/etc/coredns/`
- Service: ClusterIP, port 53 (TCP+UDP)
- Labels: `app: coredns`

#### `pathosd-configmap.yaml`
pathosd configuration (see pathosd config struct reference below for field names):
```yaml
schema: v1
router:
  asn: 65100
  router_id: 10.100.0.1
api:
  listen: ":59179"
logging:
  level: debug
  format: text
bgp:
  hold_time: 30s
  keepalive_time: 10s
  neighbors:
    - name: frr
      address: frr.pathosd-e2e.svc.cluster.local
      peer_asn: 65200
      required: true
      port: 179
vips:
  - name: web-vip
    prefix: 10.100.1.1/32
    check:
      type: http
      interval: 2s
      timeout: 1s
      rise: 3
      fall: 3
      http:
        url: /healthz
        host: nginx.pathosd-e2e.svc.cluster.local
        port: 80
        response_codes: [200]
    policy:
      fail_action: lower_priority
      lower_priority:
        as_path_prepend: 6
        communities: ["65100:666"]
  - name: dns-vip
    prefix: 10.100.2.1/32
    check:
      type: dns
      interval: 2s
      timeout: 1s
      rise: 3
      fall: 3
      dns:
        names: ["example.test."]
        resolver: coredns.pathosd-e2e.svc.cluster.local
        port: 53
        query_type: A
    policy:
      fail_action: withdraw
```

**Key design choices**:
- `web-vip` uses `lower_priority` fail action — when nginx is down, route should be pessimized (not withdrawn)
- `dns-vip` uses `withdraw` fail action — when CoreDNS is down, route should be fully withdrawn
- Short intervals (2s) and low rise/fall (3) for faster test convergence
- `hold_time: 30s` / `keepalive_time: 10s` for faster BGP state detection

#### `pathosd-deployment.yaml`
- Image: `pathosd:e2e` (loaded via `k3d image import`)
- Replicas: 1
- Args: `["run", "--config", "/etc/pathosd/pathosd.yaml"]`
- Mount configmap to `/etc/pathosd/`
- Service: ClusterIP, port 59179
- Labels: `app: pathosd`

### 2. E2E Test File (`tests/e2e/e2e_test.go`)

Build tag: `//go:build e2e`

Package: `e2e_test`

**External dependencies** (only stdlib + testify + k8s client):
```go
import (
    "context"
    "encoding/json"
    "fmt"
    "io"
    "net/http"
    "os"
    "os/exec"
    "strings"
    "testing"
    "time"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)
```

Do NOT use client-go or any Kubernetes Go client library. Use `kubectl` via `exec.Command` for all K8s operations. This keeps dependencies minimal and the tests readable.

#### Helper Functions

```go
// kubectl runs a kubectl command and returns stdout. Fails the test on error.
func kubectl(t *testing.T, args ...string) string

// kubectlNoFail runs kubectl and returns stdout + error without failing.
func kubectlNoFail(args ...string) (string, error)

// waitForPodReady waits until at least one pod matching the label selector is Ready.
func waitForPodReady(t *testing.T, namespace, labelSelector string, timeout time.Duration)

// waitForCondition polls fn every interval until it returns true or timeout expires.
func waitForCondition(t *testing.T, description string, timeout, interval time.Duration, fn func() bool)

// scaleDeploy scales a deployment to the given replica count.
func scaleDeploy(t *testing.T, namespace, name string, replicas int)

// getPathosdStatus fetches /status from pathosd via kubectl port-forward.
// Returns parsed DaemonStatus.
func getPathosdStatus(t *testing.T) DaemonStatus

// getVIPState returns the state_name of a specific VIP from /status.
func getVIPState(t *testing.T, vipName string) string

// frrShowBGP runs "vtysh -c 'show bgp ipv4 unicast json'" on the FRR pod
// and returns the raw JSON output.
func frrShowBGP(t *testing.T) string
```

**Port forwarding approach**: Start `kubectl port-forward` as a background process (`exec.Command` with `Start()`), use a random local port, wait briefly for it to be ready, then make HTTP requests to `localhost:<port>`. Kill the process in cleanup (`t.Cleanup`). Alternatively, use `kubectl exec` + `wget`/`curl` inside the pathosd or FRR pod to reach the pathosd API — this avoids port-forward complexity entirely. Choose whichever is simpler.

#### Status Types (for JSON parsing)

Define minimal structs matching the `/status` JSON response:

```go
type DaemonStatus struct {
    RouterID string      `json:"router_id"`
    ASN      uint32      `json:"asn"`
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
```

#### Test Cases

Implement these as a sequence of subtests within `TestE2E(t *testing.T)`. They run in order (not parallel) because each depends on the previous state.

```go
func TestE2E(t *testing.T) {
    // Setup: ensure namespace and all pods exist and are ready
    // Could re-apply manifests or just verify existing deployment

    t.Run("pods_ready", func(t *testing.T) {
        // Verify all pods in pathosd-e2e namespace are Running/Ready
        // waitForPodReady for each: frr, nginx, coredns, pathosd
    })

    t.Run("healthz", func(t *testing.T) {
        // GET /healthz → 200, body contains "ok"
    })

    t.Run("readyz_established", func(t *testing.T) {
        // GET /readyz → 200 (BGP peer established)
        // May need to wait/poll — BGP session takes a few seconds
    })

    t.Run("vips_announced", func(t *testing.T) {
        // Poll /status until both web-vip and dns-vip reach state_name="announced"
        // Timeout: 30s (rise=3 × interval=2s = 6s minimum, plus margin)
    })

    t.Run("frr_receives_routes", func(t *testing.T) {
        // kubectl exec frr -- vtysh -c "show bgp ipv4 unicast json"
        // Parse JSON, verify 10.100.1.1/32 and 10.100.2.1/32 are present
        // Verify AS path contains 65100
    })

    t.Run("nginx_down_web_vip_pessimized", func(t *testing.T) {
        // Scale nginx to 0 replicas
        // Poll /status until web-vip state_name="pessimized"
        // Timeout: 20s (fall=3 × interval=2s = 6s + margin)
        // dns-vip should remain "announced"
    })

    t.Run("frr_pessimized_route", func(t *testing.T) {
        // Verify FRR's BGP table shows 10.100.1.1/32 with prepended AS path
        // AS path should be [65100 65100 65100 65100 65100 65100] (prepend=6)
        // Optionally verify community 65100:666
    })

    t.Run("nginx_up_web_vip_recovers", func(t *testing.T) {
        // Scale nginx back to 1
        // waitForPodReady for nginx
        // Poll /status until web-vip state_name="announced"
        // Timeout: 30s (rise=3 × interval=2s + nginx startup)
    })

    t.Run("coredns_down_dns_vip_withdrawn", func(t *testing.T) {
        // Scale coredns to 0 replicas
        // Poll /status until dns-vip state_name="withdrawn"
        // Timeout: 20s
        // web-vip should remain "announced"
    })

    t.Run("frr_route_withdrawn", func(t *testing.T) {
        // Verify FRR's BGP table does NOT contain 10.100.2.1/32
        // 10.100.1.1/32 should still be present
    })

    t.Run("coredns_up_dns_vip_recovers", func(t *testing.T) {
        // Scale coredns back to 1
        // waitForPodReady for coredns
        // Poll /status until dns-vip state_name="announced"
        // Timeout: 30s
    })

    t.Run("metrics_endpoint", func(t *testing.T) {
        // GET /metrics
        // Assert body contains:
        //   pathosd_check_duration_seconds (histogram)
        //   pathosd_check_status (gauge)
        //   pathosd_vip_state (gauge)
        //   pathosd_build_info
        //   pathosd_bgp_peer_state
    })

    t.Run("trigger_check_api", func(t *testing.T) {
        // POST /api/v1/vips/web-vip/check
        // Assert 200 response with result
        // POST /api/v1/vips/nonexistent/check
        // Assert 404
    })
}
```

#### Important Implementation Notes

1. **Accessing pathosd API from test**: Use `kubectl exec` on any pod in the namespace to `wget`/`curl` the pathosd service. Example:
   ```go
   kubectl(t, "exec", "-n", "pathosd-e2e", "frr", "--",
       "wget", "-qO-", "http://pathosd.pathosd-e2e.svc.cluster.local:59179/status")
   ```
   This avoids port-forward complexity. FRR's alpine image has `wget`.

2. **FRR BGP table inspection**: Use `kubectl exec`:
   ```go
   kubectl(t, "exec", "-n", "pathosd-e2e", "frr", "--",
       "vtysh", "-c", "show bgp ipv4 unicast json")
   ```

3. **Polling**: All state transition assertions must poll with timeout, not sleep. Check results may take `rise*interval` or `fall*interval` seconds to converge.

4. **Namespace**: All resources use namespace `pathosd-e2e`. The test should NOT create/delete the namespace — assume manifests are already applied.

5. **Idempotency**: Tests should be runnable multiple times against a running cluster. Scaling operations are idempotent. Don't delete pods directly — scale deployments instead (except FRR which is a raw Pod).

6. **FRR JSON output**: `show bgp ipv4 unicast json` returns a JSON structure where routes are keyed by prefix:
   ```json
   {
     "routes": {
       "10.100.1.1/32": [{"aspath": {"string": "65100"}, ...}],
       "10.100.2.1/32": [{"aspath": {"string": "65100"}, ...}]
     }
   }
   ```
   Parse minimally — just enough to assert prefix presence and AS path content.

### 3. Multi-stage Dockerfile for E2E (`Dockerfile.e2e`)

The production Dockerfile expects a pre-built binary (`COPY pathosd`). For e2e, create a multi-stage Dockerfile that builds from source:

```dockerfile
FROM golang:1.26-alpine AS builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG LDFLAGS=""
RUN CGO_ENABLED=0 go build -ldflags "${LDFLAGS}" -o pathosd ./cmd/pathosd

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /build/pathosd /usr/local/bin/pathosd
ENTRYPOINT ["pathosd"]
CMD ["run", "--config", "/etc/pathosd/pathosd.yaml"]
```

### 4. Makefile Targets

Add these targets to the existing Makefile (append, do not replace existing targets):

```makefile
# --- E2E Test Targets ---

E2E_CLUSTER   ?= pathosd-e2e
E2E_IMAGE     := pathosd:e2e
E2E_NAMESPACE := pathosd-e2e

e2e-cluster:
	k3d cluster create $(E2E_CLUSTER) --wait

e2e-build:
	docker build -f Dockerfile.e2e -t $(E2E_IMAGE) .
	k3d image import $(E2E_IMAGE) -c $(E2E_CLUSTER)

e2e-deploy:
	kubectl apply -f tests/e2e/manifests/
	kubectl -n $(E2E_NAMESPACE) wait --for=condition=ready pod -l app=pathosd --timeout=120s

e2e-test:
	go test -tags=e2e -v -timeout=5m -count=1 ./tests/e2e/...

e2e-clean:
	k3d cluster delete $(E2E_CLUSTER)

e2e: e2e-cluster e2e-build e2e-deploy e2e-test

e2e-redeploy: e2e-build
	kubectl -n $(E2E_NAMESPACE) delete pod -l app=pathosd --force --grace-period=0 || true
	kubectl apply -f tests/e2e/manifests/
	kubectl -n $(E2E_NAMESPACE) wait --for=condition=ready pod -l app=pathosd --timeout=120s
```

### 5. GitHub Actions Workflow (`.github/workflows/e2e.yaml`)

```yaml
name: E2E Tests
on:
  pull_request:
  push:
    branches: [main, first-steps, integration-tests]

jobs:
  e2e:
    runs-on: ubuntu-latest
    timeout-minutes: 15
    steps:
      - uses: actions/checkout@v4

      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod

      - uses: nolar/setup-k3d-k3s@v1
        with:
          version: v1.31
          github-token: ${{ secrets.GITHUB_TOKEN }}

      - name: Build and load pathosd image
        run: |
          docker build -f Dockerfile.e2e -t pathosd:e2e .
          k3d image import pathosd:e2e

      - name: Deploy e2e stack
        run: |
          kubectl apply -f tests/e2e/manifests/
          kubectl -n pathosd-e2e wait --for=condition=ready pod -l app=frr --timeout=60s
          kubectl -n pathosd-e2e wait --for=condition=ready pod -l app=nginx --timeout=60s
          kubectl -n pathosd-e2e wait --for=condition=ready pod -l app=coredns --timeout=60s
          kubectl -n pathosd-e2e wait --for=condition=ready pod -l app=pathosd --timeout=120s

      - name: Run e2e tests
        run: go test -tags=e2e -v -timeout=5m -count=1 ./tests/e2e/...

      - name: Debug on failure
        if: failure()
        run: |
          echo "=== Pod Status ==="
          kubectl -n pathosd-e2e get pods -o wide
          echo "=== pathosd logs ==="
          kubectl -n pathosd-e2e logs -l app=pathosd --tail=100 || true
          echo "=== FRR logs ==="
          kubectl -n pathosd-e2e logs frr --tail=50 || true
          echo "=== FRR BGP summary ==="
          kubectl -n pathosd-e2e exec frr -- vtysh -c "show bgp summary" || true
```

## File Summary

Create these files:
```
tests/e2e/manifests/namespace.yaml
tests/e2e/manifests/frr-configmap.yaml
tests/e2e/manifests/frr-pod.yaml
tests/e2e/manifests/nginx-configmap.yaml
tests/e2e/manifests/nginx-deployment.yaml
tests/e2e/manifests/coredns-configmap.yaml
tests/e2e/manifests/coredns-deployment.yaml
tests/e2e/manifests/pathosd-configmap.yaml
tests/e2e/manifests/pathosd-deployment.yaml
tests/e2e/e2e_test.go
Dockerfile.e2e
.github/workflows/e2e.yaml
```

Append e2e targets to the existing `Makefile`.

## Conventions

- Use `testify` (`require` for fatal, `assert` for non-fatal)
- `//go:build e2e` tag on test file
- Package `e2e_test` (external test package)
- All kubectl calls go through the `kubectl` helper that logs the command and fails on error
- No `client-go` dependency — shell out to `kubectl`
- Manifest YAML: one resource per file, explicit namespace in metadata
- Keep timeouts generous (CI is slow): 30s for state transitions, 120s for pod readiness
- After creating all files, run `go vet ./...` to verify no compilation issues (use `-tags=e2e` for the test file)
