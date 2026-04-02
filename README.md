# pathosd

Health-aware BGP VIP announcer. Runs local health checks (HTTP, DNS, ICMP ping) with HAProxy-style rise/fall hysteresis, and announces or withdraws VIP routes over BGP based on service health.

## What It Is

`pathosd` is a **service route originator**, not a router. It embeds [GoBGP](https://github.com/osrg/gobgp) to advertise /32 (or other) prefixes for Virtual IPs when the backing service is healthy, and withdraws them when it is not.

### Fail-Closed Invariant

If the `pathosd` process dies, all BGP sessions drop and all routes are withdrawn. This is by design — a dead health checker must not leave stale routes in the network.

### Single Process

The health checker and BGP speaker live in the same process. There is no separate checker binary or IPC — check results feed directly into route decisions with no external dependencies.

## Features

- **Health checks**: HTTP (with TLS, custom headers, response codes/text), DNS (A/AAAA/CNAME/etc.), ICMP ping (with loss ratio thresholds)
- **Rise/Fall hysteresis**: Configurable consecutive success/failure thresholds before state transitions (HAProxy-style)
- **Policy actions**: `withdraw` (remove route entirely) or `lower_priority` (AS-path prepend + communities)
- **VIPs start withdrawn**: No route is announced until the service proves healthy
- **Prometheus metrics**: VIP state, check results, durations, peer status — plus GoBGP's built-in peer/route metrics
- **HTTP API**: `/healthz`, `/readyz`, `/status`, `/metrics`, ad-hoc check trigger
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
bgp:
  neighbors:
    - name: frr
      address: "%{FRR_PEER_IP}"
```

- `%{VAR_NAME}`: replaces with the value of `VAR_NAME` from the process environment.
- `%%{VAR_NAME}`: escapes the pattern and keeps it as literal `%{VAR_NAME}`.
- If any referenced variable is missing, startup fails with a clear error listing missing names.

### Key Config Rules

- `check.timeout` must be strictly less than `check.interval`
- `rise` and `fall` must be ≥ 1
- Each VIP name and prefix must be unique
- At least one neighbor and one VIP are required
- `lower_priority` block is only valid when `fail_action` is `lower_priority`

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

## HTTP Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/healthz` | GET | Liveness — 200 if process is running |
| `/readyz` | GET | Readiness — 200 if all required BGP peers are established |
| `/status` | GET | Full daemon state: peers, VIPs, last check results |
| `/metrics` | GET | Prometheus metrics exposition |
| `/api/v1/vips/{name}/check` | POST | Trigger ad-hoc health check |

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

> `NET_RAW` capability is required for ICMP ping checks. If only HTTP/DNS checks are used, it can be omitted.

## Version

```bash
pathosd version
```

Build version, commit, and date are injected via ldflags at build time.

## License

See [LICENSE](LICENSE).
