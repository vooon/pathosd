# pathosd

Health-aware BGP VIP announcer. Runs local health checks (HTTP, HTTPS, DNS, ICMP ping, TCP, UDP, gRPC, SQL, Kafka, listening socket) with HAProxy-style rise/fall hysteresis, and announces or withdraws VIP routes over BGP based on service health.

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

- **Health checks**: HTTP/HTTPS (TLS, custom headers, response codes/text/regex/JQ, forward proxy), DNS (A/AAAA/CNAME/etc.), ICMP ping (loss ratio), TCP, UDP, gRPC (standard Health Checking Protocol + arbitrary unary methods), SQL (MySQL/MariaDB and PostgreSQL, custom queries with jq conditions), Kafka (broker metadata with TLS/SASL, optional topic leadership), listening socket (no traffic sent)
- **Rise/Fall hysteresis**: configurable consecutive success/failure thresholds before state transitions (HAProxy-style)
- **Several checks per VIP**: each with its own interval/thresholds, combined with `all`, `any` or a boolean expression such as `web || (dns && ping)`
- **Policy actions**: `withdraw` (remove route entirely) or `lower_priority` (AS-path prepend + communities), plus a `lower_priority_file` drain switch
- **Standby ordering**: per-VIP `announce.as_path_prepend` / `communities` applied while healthy, so several pathosd nodes can rank the same VIP in a fixed order over eBGP
- **IPv4 + IPv6 VIPs**: IPv6 routes are carried over IPv4-transport MP-BGP with a dedicated IPv6 next-hop
- **BGP session security**: per-neighbor TCP MD5 (`auth_password`), graceful restart, passive peers, eBGP multihop
- **Observability**: Prometheus metrics (plus GoBGP's built-in collectors), opt-in per-check-type metrics, OpenTelemetry traces/metrics/logs export, a Grafana dashboard
- **HTTP API**: landing page (`/`), `/healthz`, `/readyz`, `/status`, `/metrics`, ad-hoc check trigger
- **Optional GoBGP gRPC API**: enable for `gobgp` CLI inspection/debugging
- **YAML and TOML config** with JSON Schema validation and environment placeholders
- **Packaging**: static Linux binaries (amd64/arm64), multi-arch non-root `FROM scratch` container image with a built-in health check, Debian and RPM packages with a hardened systemd unit
- **CLI**: `pathosd check` runs the configured checks once from a shell with `test(1)`-style exit codes; `pathosd validate` checks a config without starting; `pathosd healthcheck` calls `/readyz` for container health checks

## Quick start

Install the package for your distribution (static binaries and a container image are on the [releases page](https://github.com/vooon/pathosd/releases); details in [Installation and running](https://github.com/vooon/pathosd/blob/main/docs/install.md)):

```bash
sudo apt install ./pathosd_<version>_linux_amd64.deb      # Debian / Ubuntu
sudo dnf install ./pathosd_<version>_linux_amd64.rpm      # EL9 / EL10 / Fedora
```

Describe the router, at least one BGP neighbor and the VIPs with their health checks. The smallest useful config announces one `/32` while an HTTP endpoint on it answers `200`:

```yaml
schema: v1
router:
  asn: 65001
  router_id: 192.0.2.10
  local_address: 192.0.2.10
bgp:
  neighbors:
    - name: tor
      address: 192.0.2.1
      peer_asn: 65000
vips:
  - name: web
    prefix: 10.10.1.1/32
    check:
      type: http
      http: { url: /healthz }   # host defaults to the VIP address
    policy:
      fail_action: withdraw
```

Validate it, try the checks without touching BGP, then start the service:

```bash
pathosd validate --config /etc/pathosd/pathosd.yaml
pathosd check --config /etc/pathosd/pathosd.yaml
sudo systemctl enable --now pathosd
```

The VIP starts withdrawn and is announced once the check has passed `rise` times; `systemctl status pathosd` and `http://127.0.0.1:59179/` show the live state. The host must own the VIP address, see [Owning the VIP address](https://github.com/vooon/pathosd/blob/main/docs/install.md#owning-the-vip-address). Complete, commented configs: [examples/pathosd.yaml](https://github.com/vooon/pathosd/blob/main/examples/pathosd.yaml) and [examples/pathosd.toml](https://github.com/vooon/pathosd/blob/main/examples/pathosd.toml).

## Documentation

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

## License

See [LICENSE](https://github.com/vooon/pathosd/blob/main/LICENSE).
