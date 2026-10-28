# Health Check Reference

One page per check type, each starting with the smallest working config and ending with the setups people actually run in production. Every example is a complete `vips:` fragment that passes `pathosd validate` (a unit test loads them all).

| Type | Verifies | Typical use | Page |
|------|----------|-------------|------|
| `http` | HTTP(S) request: status, body text/regex/jq, TLS, via proxy | web backends, REST health endpoints, egress proxies | [http.md](https://github.com/vooon/pathosd/blob/main/docs/checks/http.md) |
| `dns` | query answered with NOERROR and at least one record | authoritative and recursive resolvers | [dns.md](https://github.com/vooon/pathosd/blob/main/docs/checks/dns.md) |
| `ping` | ICMP echo with loss threshold and RTT metrics | plain reachability, anycast nodes | [ping.md](https://github.com/vooon/pathosd/blob/main/docs/checks/ping.md) |
| `tcp` | TCP connect succeeds | anything with a listening port and no protocol probe yet | [tcp.md](https://github.com/vooon/pathosd/blob/main/docs/checks/tcp.md) |
| `udp` | datagram accepted (no ICMP port unreachable) | syslog, NetFlow/IPFIX collectors, DNS over UDP | [udp.md](https://github.com/vooon/pathosd/blob/main/docs/checks/udp.md) |
| `grpc` | gRPC Health Checking Protocol or any unary method | gRPC services, etcd | [grpc.md](https://github.com/vooon/pathosd/blob/main/docs/checks/grpc.md) |
| `listen` | a local socket is bound to the port, no traffic sent | collectors that must not receive probes, pre-flight before announcing | [listen.md](https://github.com/vooon/pathosd/blob/main/docs/checks/listen.md) |
| `sql` | queries succeed and rows satisfy a jq condition | MySQL/MariaDB and PostgreSQL, primary/replica routing | [sql.md](https://github.com/vooon/pathosd/blob/main/docs/checks/sql.md) |
| `kafka` | broker metadata, optional topic leadership, TLS/SASL | Kafka brokers and clusters | [kafka.md](https://github.com/vooon/pathosd/blob/main/docs/checks/kafka.md) |

There is deliberately no `exec` check and no embedded scripting language. The reasons are in [Why there is no exec check](https://github.com/vooon/pathosd/blob/main/docs/design/no-exec-no-scripting.md).

## Fields shared by every check

```yaml
vips:
  - name: web
    prefix: 10.10.1.1/32
    check:
      type: http          # which probe runs
      name: web           # optional; defaults to the type, needed when a VIP repeats a type
      interval: 1s        # time between probes (default 1s)
      timeout: 100ms      # per-probe deadline, must be less than interval (default 100ms)
      rise: 1             # consecutive successes before healthy (default 1)
      fall: 3             # consecutive failures before unhealthy (default 3)
      detailed_metrics: false   # per-type metrics such as status code or query duration
      http:
        url: /healthz
    policy:
      fail_action: withdraw
```

- `timeout` bounds one probe including connect, TLS and authentication. Set it to what a healthy backend needs under load, not to the interval.
- `rise`/`fall` are HAProxy semantics: strictly consecutive results, a success resets the failure counter and vice versa. See [Rise/Fall](https://github.com/vooon/pathosd/blob/main/docs/health-checks.md#risefall) and the [reaction time table](https://github.com/vooon/pathosd/blob/main/docs/health-checks.md#reaction-time).
- `host` (or `resolver`/`dst_ip`) defaults to the VIP address when the prefix is a single host (`/32` or `/128`). For wider prefixes it is required.
- Several checks per VIP go under `checks:` with `checks_policy: all | any | <expression>`; see [Multiple Checks per VIP](https://github.com/vooon/pathosd/blob/main/docs/health-checks.md#multiple-checks-per-vip).
- Secrets go through [environment placeholders](https://github.com/vooon/pathosd/blob/main/docs/configuration.md#environment-placeholders) such as `%{DB_PASSWORD}`; the daemon refuses to start when a placeholder is unset, and no checker ever logs a credential.

## Picking a check

1. Probe the protocol the clients use. A TCP connect proves a listener; an HTTP 200 proves the application answers; a SQL query proves the database serves that query. The deeper the probe, the earlier a failure is caught, and the more carefully `timeout` has to be chosen.
2. Make the condition declarative. Body checks use `response_jq`, rows use `result_jq`, and several checks combine with `checks_policy`. If the condition needs a script, it probably needs a new probe type instead; open an issue.
3. Do not send traffic the service would mistake for data: NetFlow, syslog and similar collectors get a `listen` check or a `udp` probe with a harmless payload.
4. Announce only what the host can serve: `require_local_address: true` keeps the route withdrawn while the VIP is not configured locally.

## Reading results

- `pathosd check --config FILE [--vip NAME] [--check NAME] [--format json] [--timeout 5s]` runs the checks once from a shell, no daemon or BGP needed, prints every result and the combined VIP health, and exits like `test(1)`: 0 healthy, 1 unhealthy, 200 on a config or usage error. First stop when a config does not announce.

- `GET /status` lists every VIP and check with `health`, `consecutive_ok`/`consecutive_fail`, `last_detail` (a one-line explanation such as `query writable: result_jq is not true: false`) and `last_time`.
- `POST /api/v1/vips/<name>/check` runs the checks now and returns the full result with type-specific `details` (status code, RTTs, rows per query, brokers, ...). It feeds the normal rise/fall counters.
- `pathosd_check_last_result{vip,check}` is 1/0 per check; `detailed_metrics: true` adds the per-type gauges listed on each page.
