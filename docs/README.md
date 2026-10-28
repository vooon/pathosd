# pathosd documentation

Start with the [README](https://github.com/vooon/pathosd/blob/main/README.md) for what pathosd is and a quick start. The pages below go one topic at a time.

| Page | What it covers |
|------|----------------|
| [Installation and running](https://github.com/vooon/pathosd/blob/main/docs/install.md) | binary, deb/rpm with the systemd unit, container capabilities, owning the VIP address |
| [Configuration](https://github.com/vooon/pathosd/blob/main/docs/configuration.md) | YAML/TOML, validation and JSON Schema, environment placeholders, config rules |
| [CLI](https://github.com/vooon/pathosd/blob/main/docs/cli.md) | `run`, `validate`, `check`, `jq-test`, exit codes |
| [Health checks](https://github.com/vooon/pathosd/blob/main/docs/health-checks.md) | rise/fall, how a result becomes a route change, several checks per VIP, reaction time, local address guard, running checks on demand |
| [Check reference](https://github.com/vooon/pathosd/blob/main/docs/checks/README.md) | one guide per check type: `http`, `dns`, `ping`, `tcp`, `udp`, `grpc`, `listen`, `sql`, `kafka` |
| [Announce policy](https://github.com/vooon/pathosd/blob/main/docs/announce-policy.md) | `fail_action`, drain file, AS-path prepends and communities, standby ordering |
| [BGP peering](https://github.com/vooon/pathosd/blob/main/docs/bgp-peering.md) | TCP MD5, OpenWrt/BIRD localhost peering, GoBGP CLI |
| [HTTP API](https://github.com/vooon/pathosd/blob/main/docs/http-api.md) | endpoints, readiness vs liveness, security |
| [Observability](https://github.com/vooon/pathosd/blob/main/docs/observability.md) | metrics, Grafana dashboard, OpenTelemetry |
| [Troubleshooting](https://github.com/vooon/pathosd/blob/main/docs/troubleshooting.md) | peers that never establish, VIPs that stay withdrawn, rejected routes, flapping |
| [Development](https://github.com/vooon/pathosd/blob/main/docs/development.md) | building, the pre-PR gate, e2e tests |
| [Design notes](https://github.com/vooon/pathosd/blob/main/docs/design/no-exec-no-scripting.md) | why there is no `exec` check and no embedded scripting |

Reference material elsewhere in the repository:

- [examples/](https://github.com/vooon/pathosd/blob/main/examples/): complete, commented YAML and TOML configs
- [schema/pathosd-config-v1.schema.json](https://github.com/vooon/pathosd/blob/main/schema/pathosd-config-v1.schema.json): generated JSON Schema with every field and default
- [contrib/grafana/pathosd.json](https://github.com/vooon/pathosd/blob/main/contrib/grafana/pathosd.json): Grafana dashboard
- [contrib/systemd/dummy-vip/](https://github.com/vooon/pathosd/blob/main/contrib/systemd/dummy-vip/): systemd-networkd dummy interface that holds the VIP addresses
- [AGENTS.md](https://github.com/vooon/pathosd/blob/main/AGENTS.md): contributor conventions
