# Troubleshooting

Work from the daemon outwards: first what pathosd itself reports, then the
BGP session, then the network. Every section names the signal to look at and
the usual causes.

## First look

```bash
pathosd validate --config /etc/pathosd/pathosd.toml   # config errors without starting
systemctl status pathosd                               # Status: line = VIP/peer summary (deb unit)
journalctl -u pathosd -n 100                           # startup log, transitions
curl -s 127.0.0.1:59179/readyz | jq .                  # required peers established?
curl -s 127.0.0.1:59179/status | jq .                  # peers, VIPs, last check result
```

The same information is available without `curl` or `jq`: open
`http://127.0.0.1:59179/` in a browser. The landing page lists every VIP
with its state, health, prepend count, every check with its own health and
last detail, every peer with its session state, and has a **Trigger check**
button per VIP. Reload the page to refresh it.

`/status` is the single most useful call. Per VIP it shows `state_name`
(withdrawn / announced / pessimized), `health_name`, `consecutive_ok` /
`consecutive_fail` against `rise` / `fall`, `last_check_success` with
`last_check_detail` (the check's own error text), `last_transition_reason`,
and `bgp_pending`. A VIP with several checks lists them under `checks`, each
with its own `health_name`, counters and `last_detail`; the VIP-level fields
are the roll-up (`check_type` shows the policy, e.g. `all(http, dns)`).

Run with `--debug` (or `logging.level: debug`) to see every check result as
`check passed` / `check failed` with its detail. At the default `info` level
only transitions (`VIP state transition`, `check healthy`, `check unhealthy`)
and peer events (`BGP peer state change`) are logged.

## The daemon does not start

The `run` command exits non-zero and prints the reason; with the Debian
package `ExecStartPre` runs `validate` first, so `systemctl status pathosd`
shows the same message.

| Message | Cause |
|---------|-------|
| `validation failed: check.timeout ... interval` | `check.timeout` must be strictly less than `check.interval` |
| `missing environment variables for placeholders: X` | `%{X}` is unset. Placeholders are expanded on the raw file, so one inside a **comment** fails too. Escape a literal with `%%{X}` |
| `IPv6 route requires router.local_address_ipv6` | an IPv6 VIP needs an IPv6 next-hop; `router_id` stays IPv4 |
| `tls_ca_cert cannot be set together with tls_insecure` | pick one |
| `unrecognised OTLP endpoint scheme` | `otel.endpoint` must start with `grpc://`, `grpcs://`, `http://` or `https://` |
| `starting BGP: ... bind: permission denied` | port 179 as non-root needs `CAP_NET_BIND_SERVICE` (the packaged unit grants it) or `bgp.listen_port: -1` when all peers are active and pathosd never listens |
| `starting BGP: ... address already in use` | another BGP daemon (BIRD, FRR) owns the port on that address. Use a dedicated `bgp.listen_address` / `listen_port`, as in the OpenWrt/BIRD setup |
| `HTTP API server failed ... address already in use` | `api.listen` collides with another service; default is `:59179` |

## A required peer never becomes `established`

`/readyz` returns 503 with the `unready` list; `BGP peer state change` log
lines show the FSM moving between `idle`, `active`, `connect`, `opensent`.
With `bgp.gobgp_api.enabled: true`, `gobgp -u 127.0.0.1:50051 neighbor`
gives GoBGP's own view including the last NOTIFICATION.

- **Stuck in `active` / `connect`, nothing arrives.** The peer is not
  reachable on TCP 179, a firewall drops it, or the peer is also passive.
  Exactly one side must dial. `passive: true` on pathosd means the peer has
  to connect to `bgp.listen_address:listen_port`.
- **Both sides see SYNs but the session never opens, no errors anywhere.**
  TCP MD5 mismatch: with `auth_password` set on either side, the kernel
  silently drops unsigned or wrongly signed segments. There is no log line
  for this. Compare the key byte-for-byte on both ends (trailing newline
  from a secrets file is a classic). The same mechanism breaks `nc -z` and
  `tcpSocket` probes against an MD5-protected port, which is expected.
- **`opensent` then back to `idle`, NOTIFICATION "bad peer AS".** `peer_asn`
  does not match what the peer announces, or the peer expects a different
  local ASN than `router.asn`.
- **Session flaps every hold time.** Keepalives are not getting through.
  Check MTU/firewall state timeouts and that `hold_time` / `keepalive_time`
  are acceptable to the peer.
- **Multihop peer.** For an eBGP peer that is not on a directly connected
  subnet set `enable_multihop: true` on the neighbor (optionally
  `multihop_ttl`); routers reject TTL 1 packets from a non-adjacent source.
- **`local_address` mismatch.** If the neighbor's `local_address` is not an
  address of this host the connect fails with `cannot assign requested
  address`; if it differs from what the peer has configured as its neighbor
  address, the peer refuses the connection.

## The VIP stays `withdrawn`

VIPs always start withdrawn and need `rise` consecutive successes before
the first announcement. Look at `/status`:

- `last_check_success: false`: read `last_check_detail`. It carries the
  check's own error (`unexpected status 503`, `dial tcp ... i/o timeout`,
  `context deadline exceeded`, `not serving: NOT_SERVING`, DNS rcode, TLS
  verification failure). Fix the backend or the check, not pathosd.
- `context deadline exceeded` on every run: `check.timeout` is too tight for
  the backend. `pathosd_check_timeout_exceeded_total` counts these.
- HTTPS to a VIP or internal name fails on the certificate: the check
  verifies against the system pool by default. Point `tls_ca_cert` at the
  issuing CA, or set `host` so the SNI/Host header matches the certificate.
- Ping never succeeds with `privileged: false`: unprivileged ICMP uses UDP
  ping sockets, which the kernel only allows for GIDs inside
  `net.ipv4.ping_group_range` (default `1 0`, nobody). Either widen the
  range to include the `pathosd` group or set `privileged: true`, which
  needs `CAP_NET_RAW` (granted by the packaged unit and carried as a file
  capability in the container image, where Podman additionally needs
  `--cap-add NET_RAW`). Inside a container the sysctl is usually `0 0`,
  so `socket: permission denied` there means: use privileged ping or pass
  `--sysctl net.ipv4.ping_group_range="0 2147483647"`.
- `bind: permission denied` on port 179 in a container: the runtime dropped
  `NET_BIND_SERVICE` (`--cap-drop ALL`), or Kubernetes runs the pod with
  `allowPrivilegeEscalation: false`, which disables file capabilities.
- `consecutive_ok` climbs but never reaches `rise`: a single failure resets
  it. Lower `rise` or fix the intermittent failure first.
- `sql` check fails with `connect: ... Access denied` / `password
  authentication failed`: the `%{DB_PASSWORD}` placeholder is expanded at
  config load, so the variable must be in the daemon's environment (the
  packaged unit reads `/etc/pathosd/pathosd.env`). With `driver: postgres`
  and no `database`, the user name is used as the database name.
- `sql` check fails with `result_jq is not true: null`: the query returned
  no rows, or the column name differs (MySQL returns `@@read_only` as
  `@@read_only` unless aliased with `AS`). Run the trigger endpoint and
  look at `details.queries`, or run the same statement with the same user.
- `sql` check fails with `tls: ...`: MySQL and PostgreSQL both need
  `tls: true` before `tls_ca_cert` is used; a server that only allows TLS
  logins reports the refusal as an authentication error, not a TLS one.
- `kafka` check fails with `metadata: ... EOF` or `unexpected EOF` right after
  connect: the listener is TLS or SASL and the check is configured plain
  (or the other way round). Match `tls` and `sasl` to the listener's
  security protocol; a wrong SASL password reports `SASL_AUTHENTICATION_FAILED`.
- `kafka` check fails with `topic "x" not in metadata` or
  `UNKNOWN_TOPIC_OR_PARTITION`: the topic does not exist (the check never
  auto-creates it) or the user lacks Describe on it.
- `kafka` check fails with `partitions without leader`: the broker answered
  but leadership for that topic is unavailable, typically during a broker
  restart or an under-replicated partition losing its last in-sync replica.
- `listen` check reports `no socket bound` although the service runs: the
  check reads the socket table of pathosd's own network namespace. In a
  container with its own namespace mount the host's `/proc` at
  `/host/proc`, set `paths.procfs: /host/proc` and `listen.netns_pid: 1`
  (pointing `paths.procfs` at the host `/proc` alone is not enough:
  `/proc/net` resolves to the reader's namespace). Also compare the bind
  address: a service bound to `127.0.0.1` does not match a VIP `host`;
  wildcard binds do. `ss -lntu` shows what the kernel sees.
- `listen check: procfs /host/proc/1: ... no such file`: the mount is
  missing, or `netns_pid` names a pid that does not exist in the mounted
  `/proc` (pid 1 only is the host's init when the host `/proc` is mounted).
- Several checks: `health_name: healthy` on some entries of `checks` but the
  VIP stays unhealthy means `checks_policy` (default `all`) is not satisfied.
  The log line `check transition without VIP health change` names the check
  and the policy. A check that has never reported counts as unhealthy. The
  landing page shows each check's counters and last result on hover.
- The VIP reacts later than expected: each check needs `fall × interval` to
  fail and `rise × interval` to recover; with `all` the VIP announces after
  the slowest check, with `any` it withdraws after the slowest one. The
  README's [Reaction time](https://github.com/vooon/pathosd#reaction-time) table and diagrams
  show which check sets the pace.
- Need an answer faster than `interval`: `POST /api/v1/vips/<name>/check`
  runs the check now and returns the full result, including details.

## Announced in pathosd but the route is not in the peer

`/status` says `announced`, `pathosd_route_state` is 1, `gobgp global rib`
lists the prefix, but the peer does not use it.

- **`bgp_pending: true` in `/status`.** Pushing the route to GoBGP failed
  (log `BGP state change failed`); pathosd retries on every check result and
  logs `BGP state change retry succeeded`. The error text names the cause,
  usually a next-hop problem.
- **Peer rejects the next-hop.** Routes carry `router.local_address` as
  next-hop (IPv6 VIPs: `router.local_address_ipv6`). The peer must be able
  to resolve it. BIRD on the same host refuses a next-hop that is one of
  its own addresses; rewrite it in the import filter as shown in the README
  OpenWrt/BIRD section.
- **Import filter.** FRR needs `neighbor X route-map IN in` or
  `no bgp ebgp-requires-policy`; BIRD needs an `import` filter that accepts
  the VIP prefixes. `gobgp neighbor <addr> adj-out` shows what pathosd sent,
  the peer's own CLI shows what it accepted.
- **Prefix filtered by the peer's policy** because it is a `/32` or outside
  an allowed range. Compare `gobgp neighbor <addr> adj-out` with the peer's
  received-routes view.

## The route is there but traffic does not arrive

- **The host does not own the VIP.** BGP announces the prefix; something
  still has to answer for the address. Put the VIP on a dummy interface
  (`contrib/systemd/dummy-vip/`) and make sure the service listens on it.
  Set `require_local_address: true` on the VIP so pathosd refuses to
  announce in this situation: the check detail then reads `VIP address X is
  not configured on any interface`, a `VIP address missing on host` warning
  is logged, and `pathosd_vip_local_address_assigned` drops to 0.
- **Replies take a different path.** A VIP inside the subnet of a physical
  NIC makes the host reply directly to same-L2 peers, bypassing the router.
  Use a source-based policy route or pick a VIP outside the LAN subnets.
- **Several nodes announce the same VIP** and the fabric picks one you did
  not expect: look at the AS path lengths. `announce.as_path_prepend`
  orders the nodes; see the README "Announce Policy" section.

## Pessimization does not take effect

- **iBGP peer.** pathosd never prepends its own ASN towards iBGP peers and
  logs `ignoring local ASN prepend for iBGP-originated route`. Use
  communities and a route-map on the receiving side, or peer over eBGP.
- **The peer strips or ignores prepends** (`as-override`, `allowas-in`,
  `bestpath as-path ignore`). Check the route attributes on the peer.
- **`lower_priority_file` has no effect.** The file is polled when a check
  result arrives, so the change applies at the next check, not instantly.
  Confirm the path is readable by the `pathosd` user (the unit runs with
  `ProtectSystem=strict`, which only restricts writing).

## Flapping

`pathosd_vip_transitions_total` rising steadily, `VIP state transition`
lines every few seconds.

- `fall: 1` or `rise: 1` turns every single blip into a transition. The
  defaults (`rise: 1`, `fall: 3`) favour fast recovery; raise `rise` when
  the backend is noisy after restart.
- `check.timeout` close to the backend's real latency: look at
  `pathosd_check_duration_seconds` against the timeout.
- Repeated ad-hoc triggers reach `fall` faster than `interval` would. The
  `ad-hoc check triggered` log line carries the caller's address.

## Metrics and dashboard

- `pathosd_*` series missing: Prometheus must scrape `api.listen` (default
  `:59179`, examples use `127.0.0.1:59179`). `curl /metrics` from the
  scraper's host.
- `pathosd_ping_rtt_seconds`, `pathosd_http_response_status`,
  `pathosd_dns_rcode` and friends are empty: they need
  `detailed_metrics: true` on the check.
- A query that groups `pathosd_check_*` by `vip` returns several series per
  VIP: the `check` label (check name) was added with multi-check support;
  `sum by (vip)` or filter on `check`.
- The Grafana dashboard's `instance` variable is empty: it is populated
  from `pathosd_vip_state`, so no data means no scrape.
- OTel exporter errors in the log: verify the scheme (`grpc://` vs
  `http://`) and that an `http(s)://` endpoint expects the `/v1/<signal>`
  paths pathosd appends. Disable a signal with `otel.<signal>.enabled: false`
  rather than removing the endpoint.

## Shutdown and fail-closed behaviour

Stopping pathosd (SIGTERM, `systemctl stop`) withdraws nothing explicitly;
the BGP sessions close and the peers drop every route pathosd originated.
This is intended. If routes linger on a peer after pathosd died, the peer
is holding them through graceful restart: lower its restart timer or
disable graceful restart for the pathosd neighbor.
