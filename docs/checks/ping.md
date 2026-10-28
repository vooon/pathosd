# Ping Check

Sends ICMP echo requests and passes while the loss ratio stays at or below `max_loss_ratio`. It proves reachability of an address, nothing about the service, so it is best combined with a protocol probe or used for anycast nodes where "the host is up" is the whole decision.

## Options

| Field | Default | Meaning |
|-------|---------|---------|
| `dst_ip` | VIP address | address to ping |
| `src_ip` | | source address of the echo requests |
| `privileged` | `false` | raw ICMP socket (needs `CAP_NET_RAW`) instead of an unprivileged ICMP datagram socket |
| `count` | 1 | echo requests per probe (1–60) |
| `interval` | | time between the echo requests of one probe |
| `timeout` | | per-packet reply timeout |
| `max_loss_ratio` | 0 | acceptable loss, 0.0–1.0 |

The check-level `timeout` must cover the whole probe: roughly `count × interval + timeout`. Details: sent, received, loss and RTTs. With `detailed_metrics`: `pathosd_ping_rtt_seconds` (histogram) and `pathosd_ping_packet_loss_ratio`.

Unprivileged mode needs `net.ipv4.ping_group_range` to include pathosd's group; the packaged unit grants `CAP_NET_RAW` so `privileged: true` works out of the box. See [troubleshooting](https://github.com/vooon/pathosd/blob/main/docs/troubleshooting.md) for the container flags.

## Examples

### 1. Reachability of the VIP

```yaml
vips:
  - name: node
    prefix: 10.10.3.1/32
    check:
      type: ping
      ping: {}
    policy:
      fail_action: withdraw
```

### 2. Tolerate some loss on a lossy link

Five packets 100 ms apart, up to 40 % loss. The probe takes about 0.5 s plus one reply timeout, so `timeout` is 1 s and `interval` 2 s.

```yaml
vips:
  - name: remote-site
    prefix: 10.10.3.2/32
    check:
      type: ping
      interval: 2s
      timeout: 1s
      ping:
        count: 5
        interval: 100ms
        timeout: 300ms
        max_loss_ratio: 0.4
    policy:
      fail_action: lower_priority
```

### 3. Raw sockets and a pinned source address

Useful on hosts with several addresses where the reply path matters.

```yaml
vips:
  - name: gateway
    prefix: 10.10.3.3/32
    check:
      type: ping
      ping:
        dst_ip: 10.10.3.254
        src_ip: 10.10.3.3
        privileged: true
```

### 4. Ping as the fast signal next to a slow application check

The HTTP check runs every 10 s with a generous timeout; ping every second catches a dead host long before it. `all` means either failure withdraws.

```yaml
vips:
  - name: app
    prefix: 10.10.3.4/32
    checks_policy: all
    checks:
      - type: ping
        interval: 1s
        timeout: 500ms
        fall: 3
      - type: http
        interval: 10s
        timeout: 5s
        http:
          url: /health
    policy:
      fail_action: withdraw
```

### 5. Anycast node: local reachability of the next hop

For an anycast VIP announced from many sites, the node should drop out when its own upstream disappears. Pinging the default gateway does that without involving the application. (The gateway is given explicitly; pathosd never infers it.)

```yaml
vips:
  - name: anycast
    prefix: 192.0.2.0/24
    checks_policy: all
    checks:
      - name: upstream
        type: ping
        ping:
          dst_ip: 10.10.3.1
          count: 3
          interval: 50ms
          timeout: 200ms
          max_loss_ratio: 0.34
        timeout: 800ms
      - name: service
        type: tcp
        tcp:
          host: 192.0.2.10
          port: 443
    policy:
      fail_action: withdraw
```

## Pitfalls

- Firewalls that drop ICMP make every probe fail; use `tcp` instead.
- A single packet with `max_loss_ratio: 0` turns one lost packet into a failed probe. Use `fall` to absorb it, or `count: 3` with `max_loss_ratio: 0.34`.
- Pinging the VIP from the host that owns it answers locally and proves nothing; ping a neighbour or use a service probe.
