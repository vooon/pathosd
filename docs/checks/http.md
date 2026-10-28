# HTTP Check

Sends an HTTP or HTTPS request and passes when the status code is acceptable and, if configured, the body matches. Every probe opens a new connection (and TLS handshake) unless `keep_alive` is set, so certificate problems surface on the next probe.

## Options

| Field | Default | Meaning |
|-------|---------|---------|
| `url` | `/` | path (`/healthz`) or full URL (`https://web.example.com/healthz`). A full URL sets `proto`, the `Host` header and the TLS server name |
| `proto` | from URL, else `http` | `http` or `https` |
| `host` | VIP address | where the TCP connection goes; the `Host` header still comes from `url` |
| `port` | 80 / 443 | connect port |
| `method` | `GET` | `GET` or `HEAD`; `HEAD` cannot be combined with body matchers (rejected at validation) |
| `response_codes` | `[200]` | acceptable status codes |
| `response_text` | | substring that must appear in the body |
| `response_regex` | | Go regular expression the body must match |
| `response_jq` | | jq expression over the JSON body that must yield `true` |
| `tls_ca_cert` | system roots | PEM file with the CA to trust (exclusive with `tls_insecure`) |
| `tls_insecure` | `false` | skip certificate verification |
| `tls_server_name` | URL hostname | SNI and verification name; set it when connecting by IP to a name-based certificate |
| `proxy` | | forward proxy URL (`http://user:pass@proxy:3128`); requires a full `url` |
| `headers` | | extra request headers; `User-Agent` defaults to `pathosd/<version>` and can be overridden here |
| `keep_alive` | `false` | reuse the connection across probes |

Details in `/status` and the trigger response: status code and TLS certificate expiry. With `detailed_metrics`: `pathosd_http_response_status{vip,nlri,check}` and `pathosd_http_tls_cert_expiry_timestamp_seconds{vip,nlri,check}` (alert on the latter well before it reaches `time()`).

## Examples

### 1. Plain health endpoint on the VIP

Connects to `10.10.1.1:80`, sends `GET /healthz` with `Host: 10.10.1.1`, expects 200.

```yaml
vips:
  - name: web
    prefix: 10.10.1.1/32
    check:
      type: http
      http:
        url: /healthz
    policy:
      fail_action: withdraw
```

### 2. Name-based virtual host over HTTPS

The connection still goes to the VIP, but `Host`, SNI and certificate verification use `web.example.com`. Defaults for port (443) and server name come from the URL. `timeout` is raised because a TLS handshake plus the request rarely fits in 100 ms.

```yaml
vips:
  - name: web-tls
    prefix: 10.10.1.1/32
    check:
      type: http
      interval: 2s
      timeout: 1s
      http:
        url: https://web.example.com/healthz
    policy:
      fail_action: withdraw
```

### 3. Alternate port, HEAD, several good codes

A backend behind the VIP listens on 8080 and answers 204 to `HEAD /ready`; a 503 means draining and must withdraw.

```yaml
vips:
  - name: api
    prefix: 10.10.1.2/32
    check:
      type: http
      http:
        url: /ready
        port: 8080
        method: HEAD
        response_codes: [200, 204]
    policy:
      fail_action: withdraw
```

### 4. Body must contain a marker

Status codes lie when an error page returns 200. `response_text` needs a substring anywhere in the body, `response_regex` a pattern; for an exact whole-body match anchor the regex and allow surrounding whitespace.

```yaml
vips:
  - name: legacy-app
    prefix: 10.10.1.3/32
    check:
      type: http
      http:
        url: /status.txt
        response_text: "STATUS=OK"
  - name: legacy-app-regex
    prefix: 10.10.1.4/32
    check:
      type: http
      http:
        url: /status
        response_regex: "^pool (primary|secondary): up$"
  - name: legacy-app-exact
    prefix: 10.10.1.11/32
    check:
      type: http
      http:
        url: /status.txt
        response_regex: '^\s*OK\s*$'    # the whole body is "OK", nothing else
```

### 5. JSON body with a jq condition

`response_jq` runs [jq](https://jqlang.github.io/jq/manual/) over the parsed body and must produce `true`. Anything else, including `null` from a missing key, fails with the value shown in `last_detail`.

```yaml
vips:
  - name: orders
    prefix: 10.10.1.5/32
    check:
      type: http
      interval: 5s
      timeout: 2s
      http:
        url: /health
        # {"status":"ok","checks":[{"name":"db","healthy":true},{"name":"cache","healthy":false}],"queue_depth":12}
        response_jq: '.status == "ok" and (.checks | all(.healthy)) and .queue_depth < 1000'
    policy:
      fail_action: lower_priority
```

Useful shapes:

- `.status == "UP"` — Spring Boot actuator
- `.components.db.status == "UP"` — one component of a nested report
- `(.checks | map(select(.critical)) | all(.ok))` — only critical sub-checks count
- `.replication.lag_seconds < 30` — number comparison, missing key yields `null < 30` which is `true` in jq; guard with `(.replication.lag_seconds // 999) < 30`

### 6. Private CA, custom header, bearer token

The CA file is read at startup. The token comes from the environment so it never sits in the config file.

```yaml
vips:
  - name: internal-api
    prefix: 10.10.1.6/32
    check:
      type: http
      interval: 5s
      timeout: 2s
      http:
        url: https://api.internal.example/healthz
        tls_ca_cert: /etc/pathosd/internal-ca.crt
        headers:
          Authorization: "Bearer %{API_TOKEN}"
          X-Health-Probe: pathosd
    policy:
      fail_action: withdraw
```

### 7. Certificate with a different name than the URL host

Connect to the VIP, send `Host: svc.example`, but the certificate is issued for `edge.example`.

```yaml
vips:
  - name: edge
    prefix: 10.10.1.7/32
    check:
      type: http
      interval: 2s
      timeout: 1s
      http:
        url: https://svc.example/healthz
        tls_server_name: edge.example
```

`tls_insecure: true` is the escape hatch for self-signed certificates on test systems; prefer `tls_ca_cert`.

### 8. Egress proxy health

The VIP is a forward proxy. The probe asks the proxy to fetch an external URL, so the VIP is only announced while the proxy can actually reach the internet. `proxy` requires a full `url`; the origin host comes from the URL, not from the VIP.

```yaml
vips:
  - name: egress-proxy
    prefix: 10.10.1.8/32
    check:
      type: http
      interval: 10s
      timeout: 5s
      rise: 2
      fall: 2
      http:
        url: https://www.example.com/
        proxy: "http://probe:%{PROXY_PASSWORD}@127.0.0.1:3128"
        method: HEAD
        response_codes: [200, 301, 302]
    policy:
      fail_action: withdraw
```

### 9. Fast interval with keep-alive

With `keep_alive: true` the connection is reused, so a 250 ms interval does not cost a TLS handshake each time. The certificate is only re-verified when the idle connection is dropped (after 90 s idle or when the server closes it).

```yaml
vips:
  - name: hot-path
    prefix: 10.10.1.9/32
    check:
      type: http
      interval: 250ms
      timeout: 100ms
      fall: 8        # about 2 s of failures
      rise: 4
      http:
        url: https://hot.example.com/ping
        keep_alive: true
```

### 10. HTTP plus a cheap TCP probe at a different cadence

The HTTP probe decides, the TCP probe catches a dead listener within a second. The VIP withdraws when either fails.

```yaml
vips:
  - name: web-combined
    prefix: 10.10.1.10/32
    checks_policy: all
    checks:
      - type: http
        interval: 5s
        timeout: 2s
        http:
          url: https://web.example.com/healthz
      - type: tcp
        interval: 1s
        timeout: 200ms
        fall: 2
        tcp:
          port: 443
    policy:
      fail_action: withdraw
```

## Pitfalls

- A 200 from a load balancer or CDN in front of the backend proves nothing about the backend. Point `host` at the backend and keep the `Host` header via `url`.
- Health endpoints that touch the database turn a database hiccup into a withdrawn web VIP. Put the database behind its own [sql](https://github.com/vooon/pathosd/blob/main/docs/checks/sql.md) VIP and keep `/healthz` cheap.
- Redirects are not followed; a 301 fails unless listed in `response_codes`. A plain-HTTP probe of a site that redirects to HTTPS therefore needs `response_codes: [200, 301]`, or better, probe the HTTPS URL.
- Backends and WAFs that reject empty or unknown user agents see `pathosd/<version>`; allow-list it, or set `headers: {User-Agent: ...}` to whatever they expect.
- `timeout` includes DNS resolution of `host` when it is a name. Prefer IPs for the connect host.
