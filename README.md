# pathosd

Health-aware BGP VIP announcer. Runs local health checks (HTTP, HTTPS, DNS, ICMP ping, TCP, UDP, gRPC) with HAProxy-style rise/fall hysteresis, and announces or withdraws VIP routes over BGP based on service health.

## Why pathosd?

High-availability services need a single, stable IP that fails over with the service. Classic approaches fall short:

- **Keepalived (VRRP)**: ties failover to a shared virtual MAC and requires a master/backup election. It couples the health decision to link-layer state and doesn't interoperate with routing fabrics.
- **Static VIPs**: a load balancer or router advertises a VIP unconditionally — traffic is black-holed whenever the service behind it is down.
- **Plain BGP speakers**: GoBGP/BIRD can advertise a prefix, but *you* still have to build the health-checking, hysteresis, and withdraw logic and wire it into the route originator.

`pathosd` combines the health check and the BGP originator **in one process**, so the health decision and the route state are always consistent. VIPs are only announced once the backing service proves healthy, and withdrawn the instant it does not — with no external dependencies and no IPC to drift out of sync.

- **Fail-closed by design**: if `pathosd` dies, BGP sessions drop and every route is withdrawn. A dead health checker never leaves stale routes in the network.
- **Works with any BGP fabric**: it is a route *originator*, so it peers with FRR, BIRD, or any standard BGP router — no VRRP, no shared MAC, no proprietary protocol.
- **IPv4 and IPv6**: announce IPv4 `/32` and IPv6 `/128` VIPs (or any prefix) over standard BGP/MP-BGP.
- **One moving part**: pure static binary, no sidecars, no agent to coordinate with.

`pathosd` is a **service route originator**, not a router. It embeds [GoBGP](https://github.com/osrg/gobgp) to advertise /32 (or other) prefixes for Virtual IPs when the backing service is healthy, and withdraws them when it is not.

### Fail-Closed Invariant

If the `pathosd` process dies, all BGP sessions drop and all routes are withdrawn. This is by design — a dead health checker must not leave stale routes in the network.

### Single Process

The health checker and BGP speaker live in the same process. There is no separate checker binary or IPC — check results feed directly into route decisions with no external dependencies.

## Features

- **Health checks**: HTTP/HTTPS (TLS, custom headers, response codes/text/regex/JQ), DNS (A/AAAA/CNAME/etc.), ICMP ping (loss ratio), TCP, UDP, gRPC (standard Health Checking Protocol + arbitrary unary methods)
- **Rise/Fall hysteresis**: Configurable consecutive success/failure thresholds before state transitions (HAProxy-style)
- **Policy actions**: `withdraw` (remove route entirely) or `lower_priority` (AS-path prepend + communities)
- **Standby ordering**: per-VIP `announce.as_path_prepend` / `communities` applied while healthy, so several pathosd nodes can rank the same VIP in a fixed order over eBGP
- **VIPs start withdrawn**: No route is announced until the service proves healthy
- **IPv4 + IPv6 VIPs**: announce IPv4 `/32` and IPv6 `/128` prefixes (or any subnet) over BGP/MP-BGP; IPv6 routes are carried over IPv4-transport MP-BGP with a dedicated IPv6 next-hop
- **Prometheus metrics**: VIP state, check results, durations, peer status — plus GoBGP's built-in peer/route metrics
- **HTTP API**: landing page (`/`), `/healthz`, `/readyz`, `/status`, `/metrics`, ad-hoc check trigger
- **Optional GoBGP gRPC API**: enable for `gobgp` CLI inspection/debugging
- **YAML and TOML config** with JSON Schema validation
- **Graceful restart** support for BGP sessions

## Configuration

Configuration uses `schema: v1` versioning. Supported formats: YAML (`.yaml`/`.yml`) and TOML (`.toml`).

See [examples/pathosd.yaml](examples/pathosd.yaml) and [examples/pathosd.toml](examples/pathosd.toml) for complete examples.

### Validation

Validate a config file without starting the daemon:

```bash
pathosd validate --config /etc/pathosd/pathosd.yaml
```

### Environment Placeholders

Config values can reference environment variables using VictoriaMetrics-style placeholders:

```yaml
router:
  router_id: "%{POD_IP}"
  local_address: "%{PATHOSD_LOCAL_IP}"
  local_address_ipv6: "%{PATHOSD_LOCAL_IPV6}"
bgp:
  listen_address: "%{PATHOSD_LISTEN_IP}"
  listen_port: 1179
  gobgp_api:
    enabled: true
    listen: "%{PATHOSD_GOBGP_API_LISTEN}"
  neighbors:
    - name: frr
      address: "%{FRR_PEER_IP}"
      local_address: "%{PATHOSD_LOCAL_IP}"
```

- `%{VAR_NAME}`: replaces with the value of `VAR_NAME` from the process environment.
- `%%{VAR_NAME}`: escapes the pattern and keeps it as literal `%{VAR_NAME}`.
- If any referenced variable is missing, startup fails with a clear error listing missing names.

### BGP Session Authentication (TCP MD5)

BGP has no TLS. The deployed protections are TCP MD5 signatures (RFC 2385),
its successor TCP-AO (RFC 5925, not yet supported by GoBGP) and GTSM/TTL
security (RFC 5082). pathosd supports TCP MD5 per neighbor via
`auth_password`: the kernel signs and verifies every segment, so a host that
does not know the key cannot open, hijack or reset the session.

```yaml
bgp:
  neighbors:
    - name: frr
      address: 10.0.0.2
      peer_asn: 65000
      auth_password: "%{PATHOSD_BGP_MD5}"   # keep the secret in the environment
```

The same key goes on the peer: `neighbor 10.0.0.1 password ...` in FRR, or
`password "...";` inside the `protocol bgp` block in BIRD. Keys are at most
80 bytes (Linux `TCP_MD5SIG_MAXKEYLEN`). Setting the socket option needs no
capability. Note that the embedded GoBGP gRPC API (`bgp.gobgp_api`) returns the
password as part of the peer configuration, so keep that API on a local
address or unix socket.

### Key Config Rules

- `check.timeout` must be strictly less than `check.interval` (defaults: `interval: 1s`, `timeout: 100ms`)
- `rise` and `fall` must be ≥ 1 (defaults: `rise: 1`, `fall: 3`)
- Each VIP name and prefix must be unique
- At least one neighbor and one VIP are required
- `policy.lower_priority` (`as_path_prepend`, default 6; `communities`) is used whenever the VIP is announced pessimized: on check failure with `fail_action: lower_priority`, and whenever `lower_priority_file` exists, regardless of `fail_action`
- Any IPv6 VIP prefix requires `router.local_address_ipv6` (used as the IPv6 next-hop); the `router_id` stays IPv4

### Announce Policy and Standby Ordering

Each VIP has two attribute sets under `policy`:

| Block | When applied | `as_path_prepend` | `communities` |
|-------|--------------|-------------------|---------------|
| `announce` | every announcement (healthy and pessimized) | 0–16, default 0 | attached always |
| `lower_priority` | only while pessimized (check failed with `fail_action: lower_priority`, or `lower_priority_file` present) | 1–16, default 6, **added** to the `announce` value | **merged** with the `announce` list |

Use `announce.as_path_prepend` to rank nodes announcing the same VIP: a primary with `0`, a standby with `4`, and so on. Because the pessimized prepend is the sum of both blocks, a degraded primary (`0 + 6 = 6`) still ranks below a healthy standby (`4`), and a degraded standby (`4 + 6 = 10`) ranks below a degraded primary — the standby order never inverts.

```yaml
vips:
  - name: web
    prefix: 10.10.1.1/32
    check: { type: http }
    policy:
      fail_action: lower_priority
      announce:
        as_path_prepend: 4          # this node is the standby
        communities: ["65001:200"]  # tag every announcement
      lower_priority:
        as_path_prepend: 6          # pessimized: 4 + 6 = 10 prepends
        communities: ["65001:666"]  # pessimized: 65001:200 + 65001:666
```

Prepends only work over eBGP: for iBGP peers pathosd never prepends the local ASN (a warning is logged when a prepend would have applied). Communities are sent on both.

Observability: `pathosd_vip_priority_status` reports the prepend count currently applied to the VIP (`announce.as_path_prepend` while announced or withdrawn, plus `lower_priority.as_path_prepend` while pessimized; `0` means full priority). `/status` exposes `as_path_prepend_base`, `as_path_prepend` (effective) and `communities` per VIP.

### OpenWrt/BIRD Localhost Peering

On OpenWrt the mesh routers run BIRD, and pathosd lives on the same host. pathosd acts as a distinct ASN and announces anycast VIPs over a local eBGP session. The session is passive on pathosd's side: BIRD dials out to pathosd on a second IP that BIRD assigns to its own LAN interface, at port `1179`.

Corresponding pathosd config:

```yaml
router:
  local_address: "<pathosd-local-ip>"
bgp:
  listen_address: <pathosd-listen-ip>
  listen_port: 1179
  neighbors:
    - name: bird-local
      address: <bird-local-ip>
      peer_asn: <bird_as>
      passive: true
      required: true
```

BIRD-side config for the local pathosd peer:

```bird
protocol bgp peer_pathosd_local {
    local <bird-local-ip> as <bird_as>;
    neighbor <pathosd-listen-ip> port 1179 as <pathosd_as>;
    multihop;
    ipv4 {
        import filter {
            if net ~ [ <vip>/32, ... ] then {
                bgp_next_hop = net.ip;
                preference = 250;
                accept;
            }
            reject;
        };
        export none;
    };
}
```

The import filter above is the critical part. pathosd announces VIP routes with `router.local_address` (the router's own address) as next-hop. BIRD refuses to install a route whose next-hop is a local address ("Next hop address X is a local address of iface"), so a naive `import all` would leave the route unreachable and never selected. The fix is done entirely in the filter:

1. Restrict imports to the VIP prefixes only (`if net ~ [ <vip>/32, ... ]`).
2. Rewrite each VIP's next-hop to the VIP itself (`bgp_next_hop = net.ip`) so the route is resolvable via the connected `dummy_vip`.
3. Raise `preference = 250`, above the connected/device route's default of `240`, so BIRD selects pathosd's route over the connected VIP route.
4. Reject everything else.

BIRD still marks the route unreachable locally (the next-hop caveat), but it selects and exports it to eBGP mesh peers — so the VIP reaches the mesh.

Pessimization: pathosd's `fail_action: lower_priority` with `as_path_prepend` propagates through BIRD to the mesh — remote routers see a longer AS path and steer away. This works directly over eBGP, so no iBGP or communities are required.

### GoBGP CLI Debugging

Enable the embedded GoBGP gRPC API when you want to inspect live state with the `gobgp` CLI:

```yaml
bgp:
  gobgp_api:
    enabled: true
    # Optional, defaults to 127.0.0.1:50051 when enabled.
    listen: 127.0.0.1:50051
```

Then query it with:

```bash
gobgp -u 127.0.0.1:50051 neighbor
gobgp -u 127.0.0.1:50051 global rib
```

### JSON Schema

A JSON Schema is provided at `schema/pathosd-config-v1.schema.json` for editor autocompletion and validation.

Regenerate after Config struct changes:

```bash
go generate ./internal/config/...
```

## Health Check Semantics

### Rise/Fall

- **Fall**: number of consecutive failures before a healthy VIP transitions to unhealthy
- **Rise**: number of consecutive successes before an unhealthy VIP transitions to healthy
- VIPs always start in the **withdrawn** state — they must pass `rise` consecutive checks before being announced

### Ad-Hoc Check Trigger

For VIPs with long check intervals, you can trigger an immediate check:

```bash
curl -X POST http://127.0.0.1:59179/api/v1/vips/web-frontend/check
```

The result feeds into the normal rise/fall state machine — it does not bypass hysteresis.

### HTTP Check Connections

By default every HTTP check opens a new TCP (and TLS) connection, so each run
exercises the full path including certificate verification. Set
`http.keep_alive: true` to reuse the connection across checks; this saves the
handshake on fast intervals, but the certificate is then only re-verified when
the idle connection is dropped (after 90s idle, or when the server closes it).

## HTTP Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/` | GET | HTML landing page with quick links and current daemon summary |
| `/healthz` | GET | Liveness — 200 if process is running |
| `/readyz` | GET | Readiness — 200 if all required BGP peers are established |
| `/status` | GET | Full daemon state: peers, VIPs, last check results |
| `/metrics` | GET | Prometheus metrics exposition |
| `/api/v1/vips/{name}/check` | POST | Trigger ad-hoc health check |

### API Security

The HTTP API has no authentication. Bind it to a loopback or host-local
address (`api.listen`; the examples use `127.0.0.1:59179`, the built-in
default `:59179` listens on every interface) and let Prometheus scrape it
from there. If it must be reachable from other hosts, front it with a
reverse proxy or a firewall rule.

What an unauthenticated client can do:

- **Read state**: `/status`, `/readyz` and `/metrics` expose peer addresses,
  ASNs, VIP prefixes and the last check result of every VIP. Treat that as
  internal topology information.
- **Trigger a check**: `POST /api/v1/vips/{name}/check` runs the configured
  check once. It cannot change the target, the request, or the route: the
  result goes through the normal rise/fall hysteresis, and checks for one VIP
  are serialized, so at most one ad-hoc check per VIP is in flight. The only
  effect of hammering the endpoint is that `fall` (or `rise`) is reached as
  fast as the backend answers instead of once per `interval`. Each trigger is
  logged with the caller's address.

The server applies `api.read_header_timeout`, `read_timeout`, `write_timeout`
and `idle_timeout` (defaults 10s / 30s / 60s / 120s, `0` disables) so slow or
idle clients cannot pin connections. `write_timeout` must exceed the longest
`check.timeout` for the trigger endpoint to return a result.

## Readiness vs Liveness

- **`/healthz`** returns 200 as long as the process is up and config is loaded. It does NOT depend on BGP peer state.
- **`/readyz`** returns 200 only when all `required: true` BGP peers have established sessions. Returns 503 with a JSON body listing unready peers otherwise.

## Building

### From Source

```bash
make build
```

### With GoReleaser

```bash
goreleaser build --snapshot --clean
```

### Docker

```bash
docker build -t pathosd .
```

## Development Checks

Run these checks before opening a PR:

```bash
go build ./...
go test ./...
go vet ./...
golangci-lint run
go generate ./internal/config/...
git diff --exit-code schema/
```

## E2E Testing

End-to-end tests run pathosd with FRR, nginx, CoreDNS, etcd, and syslog inside k3d/k3s and validate announce, pessimization, and withdraw behavior across all check types.

- Test code: `tests/e2e/e2e_test.go` (`//go:build e2e`)
- Manifests: `tests/e2e/manifests/`
- Design notes: `docs/e2e-test-design.md`

Run locally:

```bash
make e2e
```

Or step-by-step:

```bash
make e2e-cluster
make e2e-build
make e2e-deploy
make e2e-test
```

## Running

### Binary

```bash
pathosd run --config /etc/pathosd/pathosd.yaml
```

Force debug logging regardless of config:

```bash
pathosd run --debug --config /etc/pathosd/pathosd.yaml
```

### Container

```bash
docker run -d \
  --name pathosd \
  --cap-add NET_RAW \
  --network host \
  -v /etc/pathosd:/etc/pathosd:ro \
  pathosd run --config /etc/pathosd/pathosd.yaml
```

> `NET_RAW` capability is required for ICMP ping checks when `check.ping.privileged=true` (raw ICMP mode). The default `privileged=false` uses unprivileged UDP ping.

## Version

```bash
pathosd --version
```

Build version, commit, and date are injected via ldflags at build time.

## License

See [LICENSE](LICENSE).
