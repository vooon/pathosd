# Observability

## Metrics

`/metrics` always carries the VIP state, transition, priority, route-state and check counters/histograms (`pathosd_vip_*`, `pathosd_route_state`, `pathosd_check_*`) plus GoBGP's own collectors (`bgp_peer_*`, `bgp_routes_*`, message counters). The check series are labelled `vip`, `nlri` and `check` (the check name, which is the type unless set). Per-check-type details are opt-in per check with `detailed_metrics: true`, because they add series per check (and per name for DNS):

| Check | Metric | Meaning |
|-------|--------|---------|
| ping | `pathosd_ping_rtt_seconds` (histogram) | RTT of each echo reply |
| ping | `pathosd_ping_packet_loss_ratio` | loss of the last run, 0..1 |
| http | `pathosd_http_response_status` | status code of the last response |
| http | `pathosd_http_tls_cert_expiry_timestamp_seconds` | `NotAfter` of the leaf certificate (HTTPS only) |
| dns | `pathosd_dns_rcode{name}` | rcode of the last reply, 0 = NOERROR |
| dns | `pathosd_dns_answers{name}` | answer records in the last reply |
| grpc | `pathosd_grpc_status_code` | status code of the last call, 0 = OK |
| listen | `pathosd_listen_sockets` | sockets bound to the checked port in the last run |
| sql | `pathosd_sql_query_passed{query}` | 1 when the query succeeded and its `result_jq` held |
| sql | `pathosd_sql_query_rows{query}` | rows returned by the query |
| sql | `pathosd_sql_query_duration_seconds{query}` | duration of the query in the last run |
| kafka | `pathosd_kafka_brokers` | brokers listed in the metadata of the last run |
| kafka | `pathosd_kafka_partitions_without_leader` | partitions of the configured topic without a leader |

VIPs with `require_local_address` additionally export `pathosd_vip_local_address_assigned` (1 while the address is configured on the host).

The same details are returned in the `result.details` object of the ad-hoc check trigger regardless of the flag.

## Grafana dashboard

[contrib/grafana/pathosd.json](https://github.com/vooon/pathosd/blob/main/contrib/grafana/pathosd.json) is an importable dashboard (Grafana 11+, Prometheus-compatible datasource) with rows for VIP state, routes, health checks, the GoBGP session, and a collapsed row for the opt-in per-check details. Import it via *Dashboards → New → Import* and pick your datasource; the `instance` and `vip` variables filter every panel.

## OpenTelemetry export

Set `otel.endpoint` to an OTLP collector to export traces, metrics (bridged from the Prometheus registry) and logs. The URL scheme selects the transport: `grpc://` / `grpcs://` for OTLP/gRPC, `http://` / `https://` for OTLP/HTTP. Each signal can override the endpoint, headers, TLS setting or be disabled individually under `otel.traces`, `otel.metrics` and `otel.logs`; `logging.otel_level` sets a separate minimum level for exported logs.

```yaml
otel:
  endpoint: grpc://otel-collector.example.com:4317
  service_name: pathosd
  # headers: { x-honeycomb-team: "%{HONEYCOMB_KEY}" }
  # traces: { enabled: false }
```

Every health check runs as a trace span; the BGP announce, pessimize or withdraw it causes appears as a child span, so a route change can be followed back to the check result that triggered it.
