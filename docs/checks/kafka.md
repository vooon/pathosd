# Kafka Check

Opens a client connection to one broker, negotiates API versions (and TLS and SASL when configured) and requests cluster metadata. It passes when the metadata lists at least `min_brokers` brokers and, with `topic` set, that topic exists with a leader for every partition. Without `topic` only broker metadata is requested, which stays cheap on clusters with thousands of partitions.

## Options

| Field | Default | Meaning |
|-------|---------|---------|
| `host` | VIP address | broker address (IP or DNS name) |
| `port` | 9092 | broker port |
| `client_id` | `pathosd` | client id shown in broker logs and used for quotas |
| `sasl.mechanism` | | `plain`, `scram-sha-256` or `scram-sha-512` |
| `sasl.user`, `sasl.password` | | credentials, password via `%{VAR}` |
| `topic` | | topic that must exist with a leader per partition |
| `min_brokers` | 1 | brokers the metadata must list |
| `tls`, `tls_ca_cert`, `tls_insecure`, `tls_server_name` | | as for the HTTP check |

A fresh client is created for every probe, so connect, TLS and authentication are exercised each time. Details: cluster id, controller, broker count, partitions and partitions without leader. With `detailed_metrics`: `pathosd_kafka_brokers` and `pathosd_kafka_partitions_without_leader`.

```json
{"cluster_id":"4L6g3nShT-eMCtK--X86sw","controller":1,"brokers":3,"topic":"events","partitions":12}
```

## Examples

### 1. Broker answers metadata

```yaml
vips:
  - name: kafka
    prefix: 10.10.9.1/32
    check:
      type: kafka
      interval: 5s
      timeout: 3s
      kafka: {}
    policy:
      fail_action: withdraw
```

### 2. Per-broker VIP: this broker must be part of the cluster

Every broker runs pathosd with the same config and announces its own VIP. A broker that is up but has lost the controller and its peers reports fewer brokers and drops out.

```yaml
vips:
  - name: broker
    prefix: 10.10.9.2/32
    check:
      type: kafka
      interval: 5s
      timeout: 3s
      rise: 2
      fall: 2
      kafka:
        host: 127.0.0.1
        min_brokers: 3
    policy:
      fail_action: withdraw
```

### 3. The topic the clients need must be writable

Leaderless partitions mean producers block; withdraw until leadership is back. With `fail_action: lower_priority` the broker stays reachable for other topics.

```yaml
vips:
  - name: kafka-events
    prefix: 10.10.9.3/32
    check:
      type: kafka
      interval: 5s
      timeout: 3s
      kafka:
        topic: events
    policy:
      fail_action: lower_priority
```

### 4. SASL/SCRAM over TLS with a private CA

```yaml
vips:
  - name: kafka-secure
    prefix: 10.10.9.4/32
    check:
      type: kafka
      interval: 5s
      timeout: 3s
      kafka:
        port: 9093
        tls: true
        tls_ca_cert: /etc/pathosd/kafka-ca.crt
        tls_server_name: kafka.example
        sasl:
          mechanism: scram-sha-512
          user: pathosd
          password: "%{KAFKA_PASSWORD}"
    policy:
      fail_action: withdraw
```

### 5. SASL PLAIN on a plaintext listener

Only on trusted networks; the password crosses the wire unencrypted.

```yaml
vips:
  - name: kafka-plain-auth
    prefix: 10.10.9.5/32
    check:
      type: kafka
      interval: 5s
      timeout: 3s
      kafka:
        port: 9094
        sasl:
          mechanism: plain
          user: pathosd
          password: "%{KAFKA_PASSWORD}"
```

### 6. Bootstrap VIP for a whole cluster, announced from several nodes

Each node announces the same prefix; clients bootstrap through whichever is healthy. `min_brokers` keeps a partitioned node from offering itself.

```yaml
vips:
  - name: kafka-bootstrap
    prefix: 10.10.9.6/32
    check:
      type: kafka
      interval: 5s
      timeout: 3s
      kafka:
        host: 127.0.0.1
        min_brokers: 2
    policy:
      fail_action: withdraw
      announce:
        as_path_prepend: 0
```

### 7. Several listeners of one broker

A broker with an internal plaintext listener and an external TLS listener; both must work before the VIP is announced.

```yaml
vips:
  - name: kafka-dual
    prefix: 10.10.9.7/32
    checks_policy: all
    checks:
      - name: internal
        type: kafka
        interval: 5s
        timeout: 3s
        kafka:
          port: 9092
      - name: external
        type: kafka
        interval: 5s
        timeout: 3s
        kafka:
          port: 9093
          tls: true
          tls_ca_cert: /etc/pathosd/kafka-ca.crt
          sasl:
            mechanism: scram-sha-256
            user: pathosd
            password: "%{KAFKA_PASSWORD}"
    policy:
      fail_action: withdraw
```

## Pitfalls

- A plaintext client on a SASL or TLS listener fails with `broker closed the connection immediately ... is SASL missing?` or a TLS handshake error; match `tls` and `sasl` to the listener's security protocol.
- The check never creates topics (`allow auto topic creation` is off); `topic "x" not in metadata` means it does not exist or the user lacks Describe.
- `timeout` must cover API version negotiation plus SASL round trips; 1 s is tight over WAN, 3 s is a safe default.
- Mutual TLS (client certificates) is not supported; use SASL for client authentication.
