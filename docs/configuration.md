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
