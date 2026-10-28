# Listening Socket Check

Reads the kernel socket table and passes when a socket is bound to the port: a TCP socket in `LISTEN`, or any bound UDP socket. Nothing is sent, so the service never sees a probe. Use it for collectors that treat every datagram as data, for services that log connections, or as a pre-flight before announcing.

## Options

| Field | Default | Meaning |
|-------|---------|---------|
| `proto` | required | `tcp` or `udp` |
| `port` | required | local port |
| `host` | VIP address | local bind address; wildcard binds (`0.0.0.0`, `::`) match any host |
| `netns_pid` | 0 | read `<procfs>/<pid>/net` instead of pathosd's own namespace |

Top-level `paths.procfs` (default `/proc`) is where the socket tables come from. Details: the matching sockets. With `detailed_metrics`: `pathosd_listen_sockets{vip,nlri,check}`.

Matching rules: a wildcard bind serves every host, an IPv4 wildcard does not serve an IPv6 host, and a socket bound to exactly the host matches.

## Examples

### 1. IPFIX collector, nothing sent

```yaml
vips:
  - name: ipfix
    prefix: 10.10.7.1/32
    check:
      type: listen
      interval: 2s
      timeout: 500ms
      listen:
        proto: udp
        port: 4739
    policy:
      fail_action: withdraw
```

### 2. TCP listener bound to the VIP specifically

The collector binds `10.10.7.2:6514` rather than the wildcard; a wildcard bind would also match.

```yaml
vips:
  - name: syslog-tls
    prefix: 10.10.7.2/32
    check:
      type: listen
      listen:
        proto: tcp
        port: 6514
```

### 3. Service bound to localhost behind a local proxy

The VIP is terminated by a proxy; the real listener is on `127.0.0.1`.

```yaml
vips:
  - name: proxied
    prefix: 10.10.7.3/32
    check:
      type: listen
      listen:
        proto: tcp
        port: 9000
        host: 127.0.0.1
```

### 4. From a container with its own network namespace

Mount the host's `/proc` read-only, point `paths.procfs` at it and read PID 1's namespace.

```yaml
paths:
  procfs: /host/proc
vips:
  - name: host-collector
    prefix: 10.10.7.4/32
    check:
      type: listen
      listen:
        proto: udp
        port: 2055
        netns_pid: 1
```

Run with `-v /proc:/host/proc:ro`. Without the mount the check reads the container's own, empty, socket table and stays unhealthy.

### 5. Listen as a pre-flight plus a real probe

The listener must exist and the HTTP endpoint must answer. Combined with `all`, a service that restarts drops the VIP within a second via the cheap listen check, while HTTP catches a wedged process.

```yaml
vips:
  - name: api
    prefix: 10.10.7.5/32
    checks_policy: all
    checks:
      - type: listen
        interval: 1s
        timeout: 200ms
        fall: 1
        listen:
          proto: tcp
          port: 8443
      - type: http
        interval: 5s
        timeout: 2s
        http:
          url: https://api.example.com/healthz
          port: 8443
    policy:
      fail_action: withdraw
```

### 6. Either transport of a dual-stack collector

```yaml
vips:
  - name: flows
    prefix: 10.10.7.6/32
    checks_policy: any
    checks:
      - name: netflow
        type: listen
        listen: { proto: udp, port: 2055 }
      - name: ipfix
        type: listen
        listen: { proto: udp, port: 4739 }
```

## Pitfalls

- A bound socket says nothing about the process behind it reading from it; a stuck collector keeps its socket. Pair with a protocol probe where one exists, or watch the collector's own metrics.
- A check that has never passed stays `unknown` rather than `unhealthy` in `/status`; with `any` that is harmless, with `all` the VIP waits.
- `netns_pid` needs the host's procfs mounted into the container (`-v /proc:/host/proc:ro` plus `paths.procfs: /host/proc`); without it the check reads the container's own namespace.
