# AGENTS.md

## Scope
Instructions for contributors/agents working in this repository (`pathosd`) — a single-process, pure-Go health-aware BGP VIP announcer with embedded GoBGP v3.

## Architecture Overview
- Single static binary: health checker + BGP speaker in one process.
- Fail-closed invariant: process death = BGP sessions drop = all routes withdrawn.
- VIPs start withdrawn; must prove healthy (pass `rise` consecutive checks) before announcement.
- DI and lifecycle via `go.uber.org/fx`; context everywhere.
- Config: YAML (`goccy/go-yaml`) or TOML (`BurntSushi/toml`), validated at load time.
- CLI: `github.com/alecthomas/kong` — subcommands `run`, `validate`, `version`.
- Logging: `charmbracelet/log` as `slog.Handler`.
- Metrics: `prometheus/client_golang` (shared registry with GoBGP's built-in collectors).
- Build/release: GoReleaser, multi-platform (linux amd64/arm64), Docker multi-arch.

## Project Layout
```
cmd/pathosd/          CLI entry point (Kong), run/validate/version commands
internal/
  config/             Config structs, loading, validation, defaults, JSON Schema gen
  checks/             Check interface, HTTP/DNS/Ping backends, scheduler (rise/fall FSM)
  policy/             Evaluate health → VIP state (announce/withdraw/pessimize)
  bgp/                Embedded GoBGP wrapper, peer management, route origination, watcher
  metrics/            Prometheus metrics definitions (custom registry)
  httpapi/            HTTP API (/healthz, /readyz, /status, /metrics, ad-hoc trigger)
  daemon/             FX wiring, lifecycle hooks
  logging/            slog setup (charmbracelet/log)
  model/              Shared types (VIPState, HealthStatus, DaemonStatus)
schema/               Generated JSON Schema
examples/             Sample YAML and TOML configs
```

## Critical Rules
- User directives are absolute: if the user says `DO NOT <action>`, do not perform that action without explicit permission.
- Never edit credential/config files (e.g., `clouds.yml`, `.env`, example configs with real IPs) unless the user explicitly asks.
- Never fabricate or overwrite credentials.
- If the user says something is already configured, trust that statement unless they ask you to verify.
- Preserve user data and configuration. If in doubt, ask before changing.
- Do not undo user choices because you think there is a better approach without discussing first.

## Code Conventions
- Go module: `github.com/vooon/pathosd`.
- Accept `context.Context` in all functions that do I/O or may block.
- Config defaults live in `internal/config/defaults.go` — keep documented defaults and code defaults aligned.
- `check.timeout` must be strictly less than `check.interval` (enforced at validation time).
- Rise/fall semantics follow HAProxy: `fall` consecutive failures → unhealthy, `rise` consecutive successes → healthy.
- Metrics use a custom `prometheus.Registry` — do not use the global default registry.
- JSON Schema is generated from Go structs (`go generate ./internal/config/...`); do not hand-edit `schema/`.

## Container Versioning
- Use approved SemVer image tags for long-lived defaults.
- Avoid floating tags like `latest` in committed defaults unless explicitly requested.

## Review Checklist (Before MR)
- Compare final branch state against `master` (not intermediate commits).
- `go build ./...` and `go vet ./...` must pass.
- `go generate ./internal/config/... && git diff --exit-code schema/` — schema not stale.
- Verify README and example configs match actual runtime behavior.
- Keep documented defaults and actual defaults aligned.
- CI pipeline must pass before merge.

## Editing Notes
- Keep links absolute unless explicitly requested otherwise.
- Prefer minimal, targeted patches; do not revert unrelated user changes.
- After creating or editing Go files, verify with `go build ./...` before proceeding.

## Commit Messages
- Use Conventional Commits for commit subject lines.
- Reference: `https://www.conventionalcommits.org/en/v1.0.0/#summary`
- Typical format: `<type>(<scope>): <description>`
