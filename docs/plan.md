# pathosd — Implementation Plan

## Project
Single-process, pure-Go health-aware BGP VIP announcer with embedded GoBGP v3.
Module: `github.com/vooon/pathosd`, Go 1.26, branch `first-steps`.

## Key Dependencies

| Purpose | Library | Notes |
|---------|---------|-------|
| BGP | `github.com/osrg/gobgp/v3` (`pkg/server`) | Embedded, pure Go |
| CLI | `github.com/alecthomas/kong` | Struct-based, subcommands |
| Logging | `github.com/charmbracelet/log` | slog.Handler, text/JSON |
| Metrics | `github.com/prometheus/client_golang` | Already required by GoBGP; GoBGP's peer/route metrics free; single /metrics; configurable histogram buckets |
| DNS checks | `github.com/miekg/dns` | Pure Go |
| Ping checks | `github.com/prometheus-community/pro-bing` | Pure Go, needs cap_net_raw |
| Config YAML | `github.com/goccy/go-yaml` | Better maintained, better errors, pure Go |
| Config TOML | `github.com/BurntSushi/toml` | Pure Go |
| JSON Schema | `github.com/invopop/jsonschema` | From struct tags, Draft 2020-12 |
| DI | `go.uber.org/fx` | Lifecycle hooks, signal handling, context-based shutdown |
| HTTP checks | `net/http` (stdlib) | No dep needed |
| Build/Release | GoReleaser | Multi-platform, reproducible |

## Design Reasoning

- Go over Rust: operator knows Go, easier AI review, pure static binary
- No OTel in v1: Prometheus + slog is sufficient, no tracing needed
- Single process: fail-closed invariant (process death = route withdrawal)

---

## Phase 1: Project Foundation

**Goal**: Go module, directory tree, config structs, loading, validation, JSON Schema gen, CLI skeleton.

### Steps

1. **Init Go module** `github.com/vooon/pathosd`, create directory tree:
   - `cmd/pathosd/`
   - `internal/{config,checks,policy,bgp,metrics,httpapi,logging,model}/`
   - `schema/`, `examples/`

2. **Config structs** in `internal/config/config.go`:
   - `Config` → `Schema`, `Router`, `API`, `Logging`, `BGP`, `VIPs`
   - `RouterConfig`: `ASN` (uint32), `RouterID` (IPv4), `LocalAddress` (IP)
   - `APIConfig`: `Listen` (string)
   - `LoggingConfig`: `Level` (string), `Format` (string)
   - `BGPConfig`: `GracefulRestart` (bool), `HoldTime` (duration), `KeepaliveTime` (duration), `Neighbors` ([]NeighborConfig)
   - `NeighborConfig`: `Name`, `Address`, `PeerASN` (uint32), `Required` (bool, default true), `Port` (uint16, default 179), `Passive` (bool)
   - `VIPConfig`: `Name`, `Prefix`, `Check` (CheckConfig), `Policy` (PolicyConfig)
   - `CheckConfig`: discriminated union via `type` field + typed sub-structs
   - `PolicyConfig`: `FailAction` (`withdraw|lower_priority`, default `lower_priority`), `LowerPriority` (ASPathPrepend default 6, Communities), `LowerPriorityFile` (drain lock file)

3. **Config loading** in `internal/config/load.go`:
   - Detect format by extension (`.yaml`/`.yml` → YAML via `goccy/go-yaml`, `.toml` → TOML)
   - Parse into `Config`, apply defaults

4. **Config validation** in `internal/config/validate.go`:
   - Schema version (`v1` only, mandatory)
   - Required fields, ASN range (1–4294967295), RouterID valid IPv4
   - At least one neighbor, at least one VIP
   - Unique neighbor names, unique VIP names, unique VIP prefixes
   - Valid CIDR prefix
   - Check type supported (`http`, `dns`, `ping`)
   - `check_timeout` must be < `check_interval`
   - `rise` ≥ 1, `fall` ≥ 1
   - Field-path error messages

5. **JSON Schema generator** in `internal/config/schema_gen.go` (`//go:build ignore`):
   - Uses `invopop/jsonschema`, reflects `Config` struct
   - Writes `schema/pathosd-config-v1.schema.json`
   - `//go:generate go run ./internal/config/schema_gen.go`

6. **CLI with Kong** in `cmd/pathosd/main.go`:
   - `run` (default), `validate`, `jq-test` subcommands
   - `--version` flag via Kong + prometheus/common/version

---

## Phase 2: Logging & Metrics Foundation

**Goal**: slog setup, Prometheus metrics, build info.

### Steps

7. **Logging setup** in `internal/logging/logging.go`:
   - `Setup(level, format string) *slog.Logger`
   - `charmbracelet/log` as `slog.Handler`

8. **Metrics** in `internal/metrics/metrics.go`:
   - Custom `prometheus.Registry`
   - Auto-generated histogram buckets from check timeout (`GenerateCheckBuckets`)
   - Build info via `pver.NewCollector("pathosd")` from client_golang/prometheus/collectors/version
   - Metrics: VIP state/transitions/priority, route state, check total/absorbed/duration/last_result/timeout_exceeded

---

## Phase 3: Check Backends

**Goal**: Checker interface, HTTP/DNS/Ping implementations, per-VIP scheduler with rise/fall.

### Steps

9. **Check interface** in `internal/checks/check.go`:
   - `Checker` interface: `Check(ctx) Result`, `Type() string`
   - `Result`: Success, Detail, Duration, Err, TimedOut

10. **HTTP check** in `internal/checks/http.go`:
    - URL parsing (full URL vs path), TLS (insecure + CA cert), response codes, body text match
    - ResponseRegex, ResponseJQ (gojq) — TODO
    - Default User-Agent, Accept for JQ

11. **DNS check** in `internal/checks/dns.go`:
    - miekg/dns client, resolver defaults to VIP IP
    - Multiple names, query types

12. **Ping check** in `internal/checks/ping.go`:
    - pro-bing, needs cap_net_raw
    - Count, loss ratio

13. **Check factory** in `internal/checks/factory.go`:
    - Dispatch by `type` field

14. **Per-VIP scheduler** in `internal/checks/scheduler.go`:
    - Rise/fall hysteresis (HAProxy-style)
    - VIPs start unhealthy/withdrawn
    - TriggerCheck for ad-hoc execution
    - Callbacks for transitions and results

---

## Phase 4: Policy Engine & State Model

**Goal**: VIP state machine, shared model, policy decisions.

15. **State model** in `internal/model/model.go`
16. **Policy engine** in `internal/policy/engine.go`
17. **VIP manager** in `internal/policy/manager.go`

---

## Phase 5: BGP Subsystem

**Goal**: Embedded GoBGP, peer management, route announce/withdraw/pessimize.

18. **BGP manager** in `internal/bgp/manager.go`
19. **Peer state watcher** in `internal/bgp/watcher.go`
20. **Path helpers** in `internal/bgp/path.go`

---

## Phase 6: HTTP API

**Goal**: /healthz, /readyz, /status, /metrics, ad-hoc check trigger.

21. HTTP server, `/healthz`, `/readyz`, `/status`, `/metrics`
22. `POST /api/v1/vips/{name}/check` — ad-hoc check trigger

---

## Phase 7: Integration & Lifecycle (FX)

27. FX application wiring
28. Validate command
29. GoReleaser config

---

## Phase 8: Samples, Docs & CI

30. Sample YAML/TOML configs
31. README
32. Makefile, Dockerfile

---

## Decisions

- **GoBGP v3 embedded** — fail-closed invariant
- **`type: "xyz"` discriminated union** for check config
- **Rise/fall** (HAProxy-style) hysteresis
- **VIPs start withdrawn** — no announcement until service proves healthy
- **`goccy/go-yaml`** supports `time.Duration` natively — Duration wrapper only needed for JSON + TOML
- **Auto-generated histogram buckets** from check timeout (no config field)
- **`lower_priority_file`** — drain lock file, only upgrades announce→pessimize, doesn't prevent withdrawal
- **`pver.NewCollector`** from client_golang for build info metric
- **Go 1.26** — use `new(value)` syntax freely
- **Ad-hoc check trigger** via `POST /api/v1/vips/{name}/check` — feeds into normal rise/fall
- **Module path**: `github.com/vooon/pathosd`

## Scope Boundaries

**Included**: HTTP/DNS/Ping checks, announce/withdraw/pessimize, multiple VIPs+neighbors, YAML+TOML, config validation CLI, JSON Schema gen, prometheus/client_golang metrics, slog+charmbracelet, /healthz /readyz /status, ad-hoc check trigger, FX lifecycle, GoReleaser

**Excluded**: MySQL/Kafka/TCP checks, OpenTelemetry/tracing, BFD, underlay/VPN, hot config reload (restart required), env-var config overrides
