# pathosd

Health-aware BGP VIP announcer. Runs local health checks (HTTP, HTTPS, DNS, ICMP ping, TCP, UDP, gRPC) with HAProxy-style rise/fall hysteresis, and announces or withdraws VIP routes over BGP based on service health.

## Why pathosd?

High-availability services need a single, stable IP that fails over with the service. Classic approaches fall short:

- **Keepalived (VRRP)**: ties failover to a shared virtual MAC and requires a master/backup election. It couples the health decision to link-layer state and doesn't interoperate with routing fabrics.
- **Static VIPs**: a load balancer or router advertises a VIP unconditionally — traffic is black-holed whenever the service behind it is down.
- **Plain BGP speakers**: GoBGP/BIRD can advertise a prefix, but *you* still have to build the health-checking, hysteresis, and withdraw logic and wire it into the route originator.

`pathosd` combines the health check and the BGP originator **in one process**, so the health decision and the route state are always consistent. It is a **service route originator**, not a router: it embeds [GoBGP](https://github.com/osrg/gobgp) to advertise `/32` (or any) prefixes for Virtual IPs when the backing service is healthy, and withdraws them when it is not.

- **Fail-closed by design**: if `pathosd` dies, BGP sessions drop and every route is withdrawn. A dead health checker never leaves stale routes in the network.
- **VIPs start withdrawn**: no route is announced until the service proves healthy.
- **Single process, no IPC**: check results feed directly into route decisions. Nothing can drift out of sync.
- **Works with any BGP fabric**: peers with FRR, BIRD, or any standard BGP router — no VRRP, no shared MAC, no proprietary protocol.
- **IPv4 and IPv6**: announce IPv4 `/32` and IPv6 `/128` VIPs (or any prefix) over standard BGP/MP-BGP.
- **One moving part**: pure static binary, no sidecars, no agent to coordinate with.

## Features

- **Health checks**: HTTP/HTTPS (TLS, custom headers, response codes/text/regex/JQ, forward proxy), DNS (A/AAAA/CNAME/etc.), ICMP ping (loss ratio), TCP, UDP, gRPC (standard Health Checking Protocol + arbitrary unary methods)
- **Rise/Fall hysteresis**: configurable consecutive success/failure thresholds before state transitions (HAProxy-style)
- **Policy actions**: `withdraw` (remove route entirely) or `lower_priority` (AS-path prepend + communities), plus a `lower_priority_file` drain switch
- **Standby ordering**: per-VIP `announce.as_path_prepend` / `communities` applied while healthy, so several pathosd nodes can rank the same VIP in a fixed order over eBGP
- **IPv4 + IPv6 VIPs**: IPv6 routes are carried over IPv4-transport MP-BGP with a dedicated IPv6 next-hop
- **BGP session security**: per-neighbor TCP MD5 (`auth_password`), graceful restart, passive peers, eBGP multihop
- **Observability**: Prometheus metrics (plus GoBGP's built-in collectors), opt-in per-check-type metrics, OpenTelemetry traces/metrics/logs export, a Grafana dashboard
- **HTTP API**: landing page (`/`), `/healthz`, `/readyz`, `/status`, `/metrics`, ad-hoc check trigger
- **Optional GoBGP gRPC API**: enable for `gobgp` CLI inspection/debugging
- **YAML and TOML config** with JSON Schema validation and environment placeholders
- **Packaging**: static Linux binaries (amd64/arm64), multi-arch container image, Debian package with a hardened systemd unit

## Running

### Binary

```bash
pathosd run --config /etc/pathosd/pathosd.yaml
```

Force debug logging regardless of config:

```bash
pathosd run --debug --config /etc/pathosd/pathosd.yaml
```

### Debian package

Releases include a `.deb` that installs `/usr/bin/pathosd`, a `pathosd` system user, and a systemd unit that validates and then runs `/etc/pathosd/pathosd.toml` (kept on upgrade). The unit grants only `CAP_NET_RAW` (ICMP ping) and `CAP_NET_BIND_SERVICE` (port 179) and runs with `ProtectSystem=strict`.

```bash
sudo apt install ./pathosd_<version>_amd64.deb
sudoedit /etc/pathosd/pathosd.toml
sudo systemctl enable --now pathosd
```

### Container

```bash
docker run -d \
  --name pathosd \
  --cap-add NET_RAW \
  --network host \
  -v /etc/pathosd:/etc/pathosd:ro \
  ghcr.io/vooon/pathosd run --config /etc/pathosd/pathosd.yaml
```

> `NET_RAW` is required for ICMP ping checks when `check.ping.privileged=true` (raw ICMP mode). The default `privileged=false` uses unprivileged UDP ping.

## Configuration

Configuration uses `schema: v1` versioning. Supported formats: YAML (`.yaml`/`.yml`) and TOML (`.toml`). See [examples/pathosd.yaml](examples/pathosd.yaml) and [examples/pathosd.toml](examples/pathosd.toml) for complete, commented examples.

### Validation and JSON Schema

Validate a config file without starting the daemon:

```bash
pathosd validate --config /etc/pathosd/pathosd.yaml
```

A JSON Schema is provided at `schema/pathosd-config-v1.schema.json` for editor autocompletion and validation. It is generated from the Go config structs (`go generate ./internal/config/...`), so every field and default is documented there.

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
      auth_password: "%{PATHOSD_BGP_MD5}"
```

- `%{VAR_NAME}`: replaces with the value of `VAR_NAME` from the process environment.
- `%%{VAR_NAME}`: escapes the pattern and keeps it as literal `%{VAR_NAME}`.
- If any referenced variable is missing, startup fails with a clear error listing missing names.
- Expansion runs on the raw file before parsing, so a placeholder inside a comment is expanded (and must resolve) too.

### Key Config Rules

- `check.timeout` must be strictly less than `check.interval` (defaults: `interval: 1s`, `timeout: 100ms`)
- `rise` and `fall` must be ≥ 1 (defaults: `rise: 1`, `fall: 3`)
- Each VIP name and prefix must be unique
- At least one neighbor and one VIP are required
- `policy.lower_priority` (`as_path_prepend`, default 6; `communities`) is used whenever the VIP is announced pessimized: on check failure with `fail_action: lower_priority`, and whenever `lower_priority_file` exists, regardless of `fail_action`
- Any IPv6 VIP prefix requires `router.local_address_ipv6` (used as the IPv6 next-hop); the `router_id` stays IPv4

## Health Checks

### Rise/Fall

- **Fall**: number of consecutive failures before a healthy VIP transitions to unhealthy
- **Rise**: number of consecutive successes before an unhealthy VIP transitions to healthy
- VIPs always start in the **withdrawn** state — they must pass `rise` consecutive checks before being announced

### Ad-Hoc Check Trigger

For VIPs with long check intervals, you can trigger an immediate check:

```bash
curl -X POST http://127.0.0.1:59179/api/v1/vips/web-frontend/check
```

The result feeds into the normal rise/fall state machine — it does not bypass hysteresis. The response carries the check result including type-specific details (status code, RTTs, rcode, …).

### HTTP Check Connections

By default every HTTP check opens a new TCP (and TLS) connection, so each run exercises the full path including certificate verification. Set `http.keep_alive: true` to reuse the connection across checks; this saves the handshake on fast intervals, but the certificate is then only re-verified when the idle connection is dropped (after 90s idle, or when the server closes it).

## Announce Policy and Standby Ordering

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

`pathosd_vip_priority_status` reports the prepend count currently applied to the VIP (`0` means full priority), and `/status` exposes `as_path_prepend_base`, `as_path_prepend` (effective) and `communities` per VIP.

## BGP Peering

### Session Authentication (TCP MD5)

BGP has no TLS. The deployed protections are TCP MD5 signatures (RFC 2385), its successor TCP-AO (RFC 5925, not yet supported by GoBGP) and GTSM/TTL security (RFC 5082). pathosd supports TCP MD5 per neighbor via `auth_password`: the kernel signs and verifies every segment, so a host that does not know the key cannot open, hijack or reset the session.

```yaml
bgp:
  neighbors:
    - name: frr
      address: 10.0.0.2
      peer_asn: 65000
      auth_password: "%{PATHOSD_BGP_MD5}"   # keep the secret in the environment
```

The same key goes on the peer: `neighbor 10.0.0.1 password ...` in FRR, or `password "...";` inside the `protocol bgp` block in BIRD. Keys are at most 80 bytes (Linux `TCP_MD5SIG_MAXKEYLEN`). Setting the socket option needs no capability.

Two consequences worth knowing:

- Once a port is MD5-protected, an unsigned TCP connect to it (a `tcpSocket` readiness probe, `nc -z`, a port scanner) is silently dropped by the kernel. Monitor the session through the daemon, not by poking port 179.
- The embedded GoBGP gRPC API (`bgp.gobgp_api`) returns the password as part of the peer configuration, so keep that API on a local address or unix socket.

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

## HTTP API

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/` | GET | HTML landing page with quick links and current daemon summary |
| `/healthz` | GET | Liveness — 200 if process is running |
| `/readyz` | GET | Readiness — 200 if all required BGP peers are established |
| `/status` | GET | Full daemon state: peers, VIPs, last check results |
| `/metrics` | GET | Prometheus metrics exposition |
| `/api/v1/vips/{name}/check` | POST | Trigger ad-hoc health check |

### Readiness vs Liveness

- **`/healthz`** returns 200 as long as the process is up and config is loaded. It does NOT depend on BGP peer state.
- **`/readyz`** returns 200 only when all `required: true` BGP peers have established sessions. Returns 503 with a JSON body listing unready peers otherwise.

### API Security

The HTTP API has no authentication. Bind it to a loopback or host-local address (`api.listen`; the examples use `127.0.0.1:59179`, the built-in default `:59179` listens on every interface) and let Prometheus scrape it from there. If it must be reachable from other hosts, front it with a reverse proxy or a firewall rule.

What an unauthenticated client can do:

- **Read state**: `/status`, `/readyz` and `/metrics` expose peer addresses, ASNs, VIP prefixes and the last check result of every VIP. Treat that as internal topology information.
- **Trigger a check**: `POST /api/v1/vips/{name}/check` runs the configured check once. It cannot change the target, the request, or the route: the result goes through the normal rise/fall hysteresis, and checks for one VIP are serialized, so at most one ad-hoc check per VIP is in flight. The only effect of hammering the endpoint is that `fall` (or `rise`) is reached as fast as the backend answers instead of once per `interval`. Each trigger is logged with the caller's address.

The server applies `api.read_header_timeout`, `read_timeout`, `write_timeout` and `idle_timeout` (defaults 10s / 30s / 60s / 120s, `0` disables) so slow or idle clients cannot pin connections. `write_timeout` must exceed the longest `check.timeout` for the trigger endpoint to return a result.

## Observability

### Metrics

`/metrics` always carries the VIP state, transition, priority, route-state and check counters/histograms (`pathosd_vip_*`, `pathosd_route_state`, `pathosd_check_*`) plus GoBGP's own collectors (`bgp_peer_*`, `bgp_routes_*`, message counters). Per-check-type details are opt-in per VIP with `check.detailed_metrics: true`, because they add series per VIP (and per name for DNS):

| Check | Metric | Meaning |
|-------|--------|---------|
| ping | `pathosd_ping_rtt_seconds` (histogram) | RTT of each echo reply |
| ping | `pathosd_ping_packet_loss_ratio` | loss of the last run, 0..1 |
| http | `pathosd_http_response_status` | status code of the last response |
| http | `pathosd_http_tls_cert_expiry_timestamp_seconds` | `NotAfter` of the leaf certificate (HTTPS only) |
| dns | `pathosd_dns_rcode{name}` | rcode of the last reply, 0 = NOERROR |
| dns | `pathosd_dns_answers{name}` | answer records in the last reply |
| grpc | `pathosd_grpc_status_code` | status code of the last call, 0 = OK |

The same details are returned in the `result.details` object of the ad-hoc check trigger regardless of the flag.

### Grafana Dashboard

[contrib/grafana/pathosd.json](contrib/grafana/pathosd.json) is an importable dashboard (Grafana 11+, Prometheus-compatible datasource) with rows for VIP state, routes, health checks, the GoBGP session, and a collapsed row for the opt-in per-check details. Import it via *Dashboards → New → Import* and pick your datasource; the `instance` and `vip` variables filter every panel.

### OpenTelemetry Export

Set `otel.endpoint` to an OTLP collector to export traces, metrics (bridged from the Prometheus registry) and logs. The URL scheme selects the transport: `grpc://` / `grpcs://` for OTLP/gRPC, `http://` / `https://` for OTLP/HTTP. Each signal can override the endpoint, headers, TLS setting or be disabled individually under `otel.traces`, `otel.metrics` and `otel.logs`; `logging.otel_level` sets a separate minimum level for exported logs.

```yaml
otel:
  endpoint: grpc://otel-collector.example.com:4317
  service_name: pathosd
  # headers: { x-honeycomb-team: "%{HONEYCOMB_KEY}" }
  # traces: { enabled: false }
```

Every health check runs as a trace span; the BGP announce, pessimize or withdraw it causes appears as a child span, so a route change can be followed back to the check result that triggered it.

## Development

### Building

```bash
make build                             # static binary via go build
goreleaser build --snapshot --clean    # all release targets
docker build -t pathosd .              # container image
```

### Checks Before a PR

```bash
go build ./...
go vet -tags e2e ./...
golangci-lint run
go test -race ./...
go generate ./internal/config/... && git diff --exit-code schema/
```

CI runs the same plus `go mod tidy -diff` and `govulncheck`.

### E2E Testing

End-to-end tests run pathosd with FRR, BIRD, nginx, CoreDNS, etcd, squid and syslog inside k3d/k3s and validate announce, pessimization, and withdraw behavior across all check types, including an MD5-signed session to FRR.

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

## Version

```bash
pathosd --version
```

Build version, commit, and date are injected via ldflags at build time.

## License

See [LICENSE](LICENSE).
