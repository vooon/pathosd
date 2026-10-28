# gRPC Check

Calls the standard [gRPC Health Checking Protocol](https://github.com/grpc/grpc/blob/master/doc/health-checking.md) (`grpc.health.v1.Health/Check`) and passes on `SERVING`. With `method` set it calls that unary method with an empty request instead and passes when the status code is in `ok_codes`, which covers services without the health API.

## Options

| Field | Default | Meaning |
|-------|---------|---------|
| `host` | VIP address | address to connect to |
| `port` | required | port |
| `tls` | `false` | TLS transport |
| `tls_ca_cert` | system roots | CA file (requires `tls`, exclusive with `tls_insecure`) |
| `tls_insecure` | `false` | skip verification (requires `tls`) |
| `tls_server_name` | `host` | SNI and verification name |
| `service` | `""` | service name in the health request; empty asks for server-wide health |
| `method` | | full method path, e.g. `myapp.v1.Greeter/Hello`; switches off the health protocol |
| `ok_codes` | `[OK]` | passing status codes when `method` is set |
| `metadata` | | gRPC metadata (headers) sent with the call |

Details: status code and message. With `detailed_metrics`: `pathosd_grpc_status_code{vip,nlri,check}`.

## Examples

### 1. Health protocol, server-wide

```yaml
vips:
  - name: grpc-svc
    prefix: 10.10.6.1/32
    check:
      type: grpc
      grpc:
        port: 50051
    policy:
      fail_action: withdraw
```

### 2. One service of many

Health servers report per registered service; an empty name is the aggregate. Ask for the one the VIP is for.

```yaml
vips:
  - name: orders
    prefix: 10.10.6.2/32
    check:
      type: grpc
      grpc:
        port: 50051
        service: shop.v1.Orders
```

### 3. etcd over TLS with a private CA

etcd implements the health protocol. `timeout` allows for the handshake.

```yaml
vips:
  - name: etcd
    prefix: 10.10.6.3/32
    check:
      type: grpc
      interval: 2s
      timeout: 1s
      grpc:
        port: 2379
        tls: true
        tls_ca_cert: /etc/pathosd/etcd-ca.crt
        tls_server_name: etcd.example
    policy:
      fail_action: withdraw
```

### 4. Service without the health API: call a cheap method

Any unary method works. The request is an empty proto3 message; most "get version" or "ping" methods accept that. The default `ok_codes` is `[OK]`.

```yaml
vips:
  - name: legacy-grpc
    prefix: 10.10.6.4/32
    check:
      type: grpc
      grpc:
        port: 9000
        method: legacy.v2.Info/Version
```

### 5. Accept more than OK

A method that rejects the empty request with `InvalidArgument` still proves the service is up and decoding requests. `Unimplemented` would mean the wrong backend answered, so it is not listed.

```yaml
vips:
  - name: strict-grpc
    prefix: 10.10.6.5/32
    check:
      type: grpc
      grpc:
        port: 9000
        method: shop.v1.Orders/Get
        ok_codes: [OK, InvalidArgument, NotFound]
```

### 6. Authenticated call with metadata

```yaml
vips:
  - name: auth-grpc
    prefix: 10.10.6.6/32
    check:
      type: grpc
      interval: 5s
      timeout: 2s
      grpc:
        port: 443
        tls: true
        method: shop.v1.Admin/Ping
        metadata:
          authorization: "Bearer %{API_TOKEN}"
          x-probe: pathosd
```

### 7. Health protocol plus a hard TCP timeout

The health call has a generous timeout for slow starts; the TCP probe withdraws within a second when the listener dies.

```yaml
vips:
  - name: grpc-combined
    prefix: 10.10.6.7/32
    checks_policy: all
    checks:
      - type: grpc
        interval: 5s
        timeout: 3s
        grpc:
          port: 50051
      - type: tcp
        interval: 500ms
        timeout: 200ms
        fall: 2
        tcp:
          port: 50051
    policy:
      fail_action: withdraw
```

## Pitfalls

- `NOT_SERVING` and `SERVICE_UNKNOWN` both fail; a missing `service` registration shows as the latter in `last_detail`.
- HTTP/2 over TLS needs ALPN `h2`; a TLS terminator that only speaks HTTP/1.1 fails the check even though `curl` works.
- Without `tls` the check uses h2c (plaintext HTTP/2); a server that requires TLS closes the connection, visible as `Unavailable`.
