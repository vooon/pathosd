# UDP Check

Sends one datagram and passes unless the kernel reports ICMP *port unreachable* (`ECONNREFUSED` on the connected socket). A service that answers also passes; a service that silently accepts passes too, because UDP gives no acknowledgement. No raw sockets or privileges are needed.

## Options

| Field | Default | Meaning |
|-------|---------|---------|
| `host` | VIP address | destination |
| `port` | required | destination port |
| `payload` | one zero byte | datagram contents |

Decision logic: `ECONNREFUSED` → nothing listens → fail; data received → pass; nothing received before `timeout` → datagram accepted → pass. `timeout` therefore sets how long a probe takes when the service does not reply, so keep it short (200 ms to 1 s).

## Examples

### 1. Syslog collector

The default zero byte is discarded by syslog daemons.

```yaml
vips:
  - name: syslog
    prefix: 10.10.5.1/32
    check:
      type: udp
      interval: 5s
      timeout: 1s
      udp:
        port: 514
    policy:
      fail_action: withdraw
```

### 2. Collector that must not see probes at all

NetFlow/IPFIX/sFlow collectors parse every datagram and may log a malformed one. Use the [listen](https://github.com/vooon/pathosd/blob/main/docs/checks/listen.md) check instead, which inspects the socket table and sends nothing.

### 3. A harmless, protocol-valid payload

Some daemons log junk; send something they understand. A syslog line with facility local7 and severity debug is usually filtered:

```yaml
vips:
  - name: syslog-quiet
    prefix: 10.10.5.3/32
    check:
      type: udp
      interval: 5s
      timeout: 1s
      udp:
        port: 514
        payload: "<191>pathosd probe"
```

### 4. Backend on another host

```yaml
vips:
  - name: radius
    prefix: 10.10.5.4/32
    check:
      type: udp
      udp:
        host: 10.10.5.44
        port: 1812
```

### 5. UDP presence plus a real DNS answer

For a DNS server, `udp` only proves a listener; the `dns` check proves answers. Both must pass.

```yaml
vips:
  - name: dns-udp
    prefix: 10.10.5.5/32
    checks_policy: all
    checks:
      - type: udp
        interval: 1s
        timeout: 300ms
        udp:
          port: 53
      - type: dns
        interval: 5s
        timeout: 1s
        dns:
          names: ["example.com."]
    policy:
      fail_action: withdraw
```

## Pitfalls

- A firewall that drops ICMP errors hides a closed port: every probe "passes". Verify once with `pathosd` against a port you know is closed (the trigger endpoint shows `connection refused`).
- Rate-limited ICMP (`net.ipv4.icmp_ratelimit`) can make a closed port look open on some probes; use `fall` ≥ 3.
- IPv6 works the same way (`ICMPv6 port unreachable`).
