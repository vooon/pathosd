# TCP Check

Opens a TCP connection and closes it. It passes when the handshake completes within `timeout`. Cheap, protocol-agnostic and often the first check to add; replace it with a protocol probe once the service has one.

## Options

| Field | Default | Meaning |
|-------|---------|---------|
| `host` | VIP address | address to connect to |
| `port` | required | port to connect to |

`last_detail` names the dialled address and the error when the connect fails.

## Examples

### 1. Listener on the VIP

```yaml
vips:
  - name: smtp
    prefix: 10.10.4.1/32
    check:
      type: tcp
      tcp:
        port: 25
    policy:
      fail_action: withdraw
```

### 2. Backend on another address

The VIP is served by a process bound to the host's own address.

```yaml
vips:
  - name: redis
    prefix: 10.10.4.2/32
    check:
      type: tcp
      tcp:
        host: 127.0.0.1
        port: 6379
```

### 3. Wider prefix

For a prefix that is not a single host, `host` is required.

```yaml
vips:
  - name: pool
    prefix: 10.10.4.0/28
    check:
      type: tcp
      tcp:
        host: 10.10.4.1
        port: 443
```

### 4. Several ports must all be open

One check per port, combined with `all`. Names are required because the type repeats.

```yaml
vips:
  - name: mail
    prefix: 10.10.4.4/32
    checks_policy: all
    checks:
      - name: smtp
        type: tcp
        tcp: { port: 25 }
      - name: submission
        type: tcp
        tcp: { port: 587 }
      - name: imaps
        type: tcp
        tcp: { port: 993 }
    policy:
      fail_action: withdraw
```

### 5. Either of two listeners is enough

A service that migrates between ports during upgrades.

```yaml
vips:
  - name: rolling
    prefix: 10.10.4.5/32
    checks_policy: any
    checks:
      - name: old
        type: tcp
        tcp: { port: 8080 }
      - name: new
        type: tcp
        tcp: { port: 8081 }
```

### 6. Fast detection

A connect takes well under a millisecond on a LAN, so a 250 ms interval with `fall: 4` withdraws within about a second of the listener going away.

```yaml
vips:
  - name: lb
    prefix: 10.10.4.6/32
    check:
      type: tcp
      interval: 250ms
      timeout: 100ms
      fall: 4
      rise: 2
      tcp:
        port: 443
    policy:
      fail_action: withdraw
```

## Pitfalls

- A listening socket proves the kernel accepted the connection; the application may be wedged behind it. Prefer `http`, `grpc` or `sql` when available.
- Connecting to a port protected by TCP MD5 or a firewall that silently drops SYNs ends in a timeout, which counts as failure.
- Every probe leaves a short-lived connection in the server's accept log; use `listen` when even that is unwanted.
