# Configuration

Configuration uses `schema: v1` versioning. Supported formats: YAML (`.yaml`/`.yml`) and TOML (`.toml`). See [examples/pathosd.yaml](https://github.com/vooon/pathosd/blob/main/examples/pathosd.yaml) and [examples/pathosd.toml](https://github.com/vooon/pathosd/blob/main/examples/pathosd.toml) for complete, commented examples.

The VIP, check and policy parts are covered in [Health checks](https://github.com/vooon/pathosd/blob/main/docs/health-checks.md), the [check reference](https://github.com/vooon/pathosd/blob/main/docs/checks/README.md) and [Announce policy](https://github.com/vooon/pathosd/blob/main/docs/announce-policy.md); peers in [BGP peering](https://github.com/vooon/pathosd/blob/main/docs/bgp-peering.md).

## Validation and JSON Schema

Validate a config file without starting the daemon:

```bash
pathosd validate --config /etc/pathosd/pathosd.yaml
```

A JSON Schema is provided at `schema/pathosd-config-v1.schema.json` for editor autocompletion and validation. It is generated from the Go config structs (`go generate ./internal/config/...`), so every field and default is documented there.

## Environment placeholders

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

## Key config rules

- `check.timeout` must be strictly less than `check.interval` (defaults: `interval: 1s`, `timeout: 100ms`)
- `rise` and `fall` must be ≥ 1 (defaults: `rise: 1`, `fall: 3`)
- Exactly one of `check` or `checks` per VIP; check names must be unique within the VIP (two checks of the same type need explicit names) and `checks_policy` must compile against them
- Each VIP name and prefix must be unique
- At least one neighbor and one VIP are required
- `policy.lower_priority` (`as_path_prepend`, default 6; `communities`) is used whenever the VIP is announced pessimized: on check failure with `fail_action: lower_priority`, and whenever `lower_priority_file` exists, regardless of `fail_action`
- Any IPv6 VIP prefix requires `router.local_address_ipv6` (used as the IPv6 next-hop); the `router_id` stays IPv4

## Hot reload

A running daemon re-reads its file on `SIGHUP`, on `POST /api/v1/reload`, or through [`pathosd reload`](https://github.com/vooon/pathosd/blob/main/docs/cli.md#reload), which is what `systemctl reload pathosd` runs. Only the `vips` list and `logging.level` change live. Every other section (`router`, `bgp` and its neighbors, `api`, `otel`, `paths`, the rest of `logging`) needs a restart: a reload that touches one of them is **rejected as a whole** with the section named, and the daemon keeps running the previous config. A file that fails validation is rejected the same way, so a half-edited file never reaches the routes.

What an applied reload does per VIP:

- **Added VIP**: starts withdrawn with unknown health and proves itself through `rise`, like at startup.
- **Removed VIP**: its checks stop and its route is withdrawn immediately. A VIP whose `prefix` changed counts as removed and added.
- **Policy change only** (`policy`, `checks_policy`): takes effect immediately. The route is re-originated with the new attributes without a withdraw.
- **Check change** (a check added, removed or modified, or `require_local_address` toggled): make-before-break. The new checks run in shadow next to the old ones, which keep governing the route, until every new or modified check has reached a verdict (`rise` successes or `fall` failures). Then the VIP switches to the new definition in one step and the state is re-evaluated: a healthy target never loses its route for a check tweak, and a target the new check cannot reach is withdrawn as soon as `fall` is reached. Until then the VIP is listed as *pending* in `/status`, the status page and `systemctl status`. A reload during the wait supersedes the pending one.

Outcomes, as reported by every trigger and by `pathosd_config_reload_total{result}`:

| Outcome | Meaning | HTTP | `pathosd reload` exit |
|---------|---------|------|-----------------------|
| `applied` | the daemon now runs the file (some VIPs may still be pending their verdict) | 200 | 0 |
| `unchanged` | the file is byte-identical to the running config | 200 | 0 |
| `rejected` | nothing changed: the file is invalid, a checker could not be built, or a restart-only section differs | 422 | 1 |
| `failed` | applied, but a step reported an error (for example a withdraw of a removed VIP); the message lists them | 500 | 1 |

Knowing that a reload was *not* applied matters as much as reloading: see [Config reload alerts](https://github.com/vooon/pathosd/blob/main/docs/observability.md#config-reload) for the metrics, and note that the daemon also compares the file with the running config every 5 s and reports an edited-but-not-reloaded file (`pathosd_config_file_changed`, the `config` block of `/status`, a banner on the status page, and the `systemctl status` line).

`run --debug` pins the log level: a reload keeps `debug` whatever the file says.
