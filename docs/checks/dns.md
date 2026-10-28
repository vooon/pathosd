# DNS Check

Sends one query per configured name to a resolver and passes when every reply has `NOERROR` and at least one answer record. Names are queried as given (a trailing dot is added), no search list, no recursion through the system resolver.

## Options

| Field | Default | Meaning |
|-------|---------|---------|
| `names` | required | names to query; all must succeed |
| `resolver` | VIP address | `ip` or `ip:port` of the server to ask |
| `port` | 53 | used when `resolver` has no port |
| `query_type` | `A` | `A`, `AAAA`, `CNAME`, `PTR`, `NS`, `MX`, `SOA`, `TXT`, `SRV` |

Details: rcode and answer count per name. With `detailed_metrics`: `pathosd_dns_rcode{vip,nlri,check,name}` and `pathosd_dns_answers{vip,nlri,check,name}`.

## Examples

### 1. Recursive resolver on the VIP

```yaml
vips:
  - name: resolver
    prefix: 10.10.2.1/32
    check:
      type: dns
      dns:
        names: ["example.com."]
    policy:
      fail_action: withdraw
```

### 2. Authoritative server: ask for its own zone

A recursive query to an authoritative server depends on upstreams; asking for the zone's SOA only tests what this server owns. `timeout` of 100 ms is fine on a LAN.

```yaml
vips:
  - name: ns1
    prefix: 10.10.2.2/32
    check:
      type: dns
      interval: 2s
      timeout: 500ms
      dns:
        names: ["example.com."]
        query_type: SOA
    policy:
      fail_action: withdraw
```

### 3. Several names, failure of any withdraws

Checks that the common zones are all served. Use one check with several names when they share a cadence; use separate checks under `checks:` when they do not.

```yaml
vips:
  - name: ns-all-zones
    prefix: 10.10.2.3/32
    check:
      type: dns
      interval: 5s
      timeout: 1s
      dns:
        names:
          - example.com.
          - example.net.
          - 10.in-addr.arpa.
        query_type: SOA
```

### 4. IPv6 resolver VIP and AAAA records

```yaml
vips:
  - name: resolver-v6
    prefix: 2001:db8:100::53/128
    check:
      type: dns
      dns:
        names: ["www.example.com."]
        query_type: AAAA
```

### 5. Anycast resolver on a wider prefix

For a prefix that is not a single host, `resolver` is required because there is no single VIP address to default to.

```yaml
vips:
  - name: anycast-dns
    prefix: 10.10.2.0/24
    check:
      type: dns
      dns:
        resolver: 10.10.2.53
        names: ["example.com."]
```

### 6. Non-standard port (e.g. a local forwarder)

```yaml
vips:
  - name: forwarder
    prefix: 10.10.2.6/32
    check:
      type: dns
      dns:
        resolver: 127.0.0.1:5353
        names: ["example.com."]
```

### 7. Recursion plus upstream reachability

A recursive resolver can answer cached names while its upstream is gone. The expression keeps the VIP announced only while the resolver answers an authoritative-side name (`ns`) and also resolves an external name (`external`), or while at least the local zone works during an internet outage.

```yaml
vips:
  - name: recursor
    prefix: 10.10.2.7/32
    checks_policy: "ns || external"
    checks:
      - name: ns
        type: dns
        dns:
          names: ["example.com."]
          query_type: SOA
      - name: external
        type: dns
        interval: 10s
        timeout: 3s
        fall: 2
        dns:
          names: ["www.example.org."]
    policy:
      fail_action: lower_priority
```

## Pitfalls

- `NXDOMAIN` is a failure. Query a name that exists; for a resolver, the zone apex of a public domain is stable.
- A reply with `NOERROR` and no answer (for example asking for `AAAA` on an IPv4-only name) fails, by design.
- Queries use UDP; truncated answers are not retried over TCP, so query small records (`SOA`, `A`) rather than large `TXT` sets.
