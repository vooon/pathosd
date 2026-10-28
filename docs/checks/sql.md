# SQL Check

Connects to MySQL/MariaDB (`driver: mysql`) or PostgreSQL (`driver: postgres`), runs the configured queries on one connection and passes when every query succeeds and every `result_jq` condition holds. Nothing about any schema is built in: read-only state, replication lag, Galera state or an application table are all a query plus a condition, and the user only needs the privileges those queries require.

## Options

| Field | Default | Meaning |
|-------|---------|---------|
| `driver` | required | `mysql` (also MariaDB) or `postgres` |
| `host` | VIP address | server address (IP or DNS name) |
| `port` | 3306 / 5432 | server port |
| `user` | required | login user |
| `password` | | login password, use `%{VAR}` |
| `database` | | database to connect to (PostgreSQL defaults to the user name) |
| `queries` | `[{sql: SELECT 1}]` | statements run in order; each has `sql`, optional `name` (default `q1`, `q2`, …) and optional `result_jq` |
| `max_rows` | 100 | rows fetched per query |
| `keep_alive` | `false` | reuse the connection across probes |
| `tls`, `tls_ca_cert`, `tls_insecure`, `tls_server_name` | | as for the HTTP check |

Rows reach jq as an array of `{column: value}` objects: numeric columns as numbers, booleans as booleans (MySQL: 0/1), timestamps as RFC 3339 strings, `NULL` as `null`, everything else as strings (PostgreSQL arrays and `jsonb` too; use `fromjson` on the latter). `result_jq` must yield exactly `true`; `null` from an empty result set or a misspelled column fails with `result_jq is not true: null` in `last_detail`.

Details per query: rows, duration, passed; queries after a failing one are not run. With `detailed_metrics`: `pathosd_sql_query_passed`, `pathosd_sql_query_rows`, `pathosd_sql_query_duration_seconds`, all with a `query` label.

```json
{"queries":[{"name":"q1","rows":1,"duration":120300,"passed":true},
            {"name":"writable","rows":1,"duration":51288,"passed":true}]}
```

## Examples

### 1. The server answers

Connect, authenticate, run `SELECT 1`. This is the whole default.

```yaml
vips:
  - name: mariadb
    prefix: 10.10.8.1/32
    check:
      type: sql
      interval: 5s
      timeout: 2s
      sql:
        driver: mysql
        user: pathosd
        password: "%{DB_PASSWORD}"
    policy:
      fail_action: withdraw
```

### 2. PostgreSQL with an explicit database

```yaml
vips:
  - name: postgres
    prefix: 10.10.8.2/32
    check:
      type: sql
      interval: 5s
      timeout: 2s
      sql:
        driver: postgres
        user: pathosd
        password: "%{DB_PASSWORD}"
        database: app
```

### 3. Announce the read-write VIP only from the primary

The condition is the server's own notion of writability. The same config on every node makes exactly the primary announce; a failover tool that flips `read_only` or promotes a standby moves the VIP without touching pathosd.

```yaml
vips:
  - name: mysql-rw
    prefix: 10.10.8.3/32
    check:
      type: sql
      interval: 2s
      timeout: 1s
      rise: 2
      fall: 2
      sql:
        driver: mysql
        user: pathosd
        password: "%{DB_PASSWORD}"
        queries:
          - name: writable
            sql: SELECT @@read_only AS read_only
            result_jq: ".[0].read_only == 0"
    policy:
      fail_action: withdraw
  - name: pg-rw
    prefix: 10.10.8.4/32
    check:
      type: sql
      interval: 2s
      timeout: 1s
      rise: 2
      fall: 2
      sql:
        driver: postgres
        user: pathosd
        password: "%{DB_PASSWORD}"
        queries:
          - name: primary
            sql: SELECT pg_is_in_recovery() AS in_recovery
            result_jq: ".[0].in_recovery == false"
    policy:
      fail_action: withdraw
```

### 4. Read-only VIP from every node that serves reads

Announced by the primary and by every replica whose replication is applying. On the primary `SHOW REPLICA STATUS` returns no row, which the `// {}` default turns into a pass; on a replica the SQL thread must run. Only the SQL thread is required so a replica keeps serving reads while the primary is unreachable. The user needs `REPLICA MONITOR` (MariaDB) or `REPLICATION CLIENT` (MySQL).

```yaml
vips:
  - name: mysql-ro
    prefix: 10.10.8.5/32
    check:
      type: sql
      interval: 2s
      timeout: 1s
      sql:
        driver: mysql
        user: pathosd
        password: "%{DB_PASSWORD}"
        queries:
          - sql: SELECT 1
          - name: replication
            sql: SHOW REPLICA STATUS
            result_jq: '(.[0] // {}) | (.Slave_SQL_Running // "Yes") == "Yes"'
    policy:
      fail_action: withdraw
  - name: pg-ro
    prefix: 10.10.8.6/32
    check:
      type: sql
      interval: 2s
      timeout: 1s
      sql:
        driver: postgres
        user: pathosd
        password: "%{DB_PASSWORD}"
        queries:
          - sql: SELECT 1
          - name: caught_up
            sql: SELECT pg_last_wal_receive_lsn() IS NOT DISTINCT FROM pg_last_wal_replay_lsn() AS caught_up
            result_jq: ".[0].caught_up"
    policy:
      fail_action: withdraw
```

Together with example 3 this is the primary/replica routing the e2e suite exercises: the rw prefix has one path in the fabric, the ro prefix one per healthy node, and demoting the primary moves only the rw VIP.

### 5. Replica with a lag budget

A replica that is more than 60 s behind should stop taking reads. On MariaDB the column is `Seconds_Behind_Master`; the `// 0` keeps the primary passing.

```yaml
vips:
  - name: mysql-ro-fresh
    prefix: 10.10.8.7/32
    check:
      type: sql
      interval: 5s
      timeout: 2s
      sql:
        driver: mysql
        user: pathosd
        password: "%{DB_PASSWORD}"
        queries:
          - name: lag
            sql: SHOW REPLICA STATUS
            result_jq: '(.[0] // {}) | (.Slave_SQL_Running // "Yes") == "Yes" and (.Seconds_Behind_Master // 0) < 60'
    policy:
      fail_action: lower_priority
```

PostgreSQL: on a standby `pg_last_xact_replay_timestamp()` stops advancing when the primary is idle, so compare LSNs or use `pg_stat_wal_receiver` rather than wall-clock lag alone.

```yaml
vips:
  - name: pg-ro-fresh
    prefix: 10.10.8.8/32
    check:
      type: sql
      interval: 5s
      timeout: 2s
      sql:
        driver: postgres
        user: pathosd
        password: "%{DB_PASSWORD}"
        queries:
          - name: lag
            sql: >-
              SELECT pg_is_in_recovery() AS standby,
                     COALESCE(EXTRACT(EPOCH FROM now() - pg_last_xact_replay_timestamp()), 0) AS lag,
                     pg_last_wal_receive_lsn() IS NOT DISTINCT FROM pg_last_wal_replay_lsn() AS caught_up
            result_jq: ".[0].standby == false or .[0].caught_up or .[0].lag < 60"
```

### 6. Galera / cluster membership

A node must be `Synced` and part of the primary component.

```yaml
vips:
  - name: galera
    prefix: 10.10.8.9/32
    check:
      type: sql
      interval: 2s
      timeout: 1s
      sql:
        driver: mysql
        user: pathosd
        password: "%{DB_PASSWORD}"
        queries:
          - name: synced
            sql: SHOW STATUS LIKE 'wsrep_local_state_comment'
            result_jq: '.[0].Value == "Synced"'
          - name: primary_component
            sql: SHOW STATUS LIKE 'wsrep_cluster_status'
            result_jq: '.[0].Value == "Primary"'
    policy:
      fail_action: withdraw
```

### 7. Application-level freshness

The database is up but the pipeline feeding it is not: withdraw when the newest row is older than five minutes, or when a queue backs up.

```yaml
vips:
  - name: ingest
    prefix: 10.10.8.10/32
    check:
      type: sql
      interval: 30s
      timeout: 5s
      fall: 2
      sql:
        driver: postgres
        user: pathosd
        password: "%{DB_PASSWORD}"
        database: ingest
        queries:
          - name: fresh
            sql: SELECT EXTRACT(EPOCH FROM now() - max(received_at)) AS age FROM events
            result_jq: ".[0].age < 300"
          - name: backlog
            sql: SELECT count(*) AS pending FROM jobs WHERE state = 'queued'
            result_jq: ".[0].pending < 10000"
    policy:
      fail_action: lower_priority
```

### 8. Connection headroom

Stop sending new clients to a server close to `max_connections`.

```yaml
vips:
  - name: pg-headroom
    prefix: 10.10.8.11/32
    check:
      type: sql
      interval: 5s
      timeout: 2s
      sql:
        driver: postgres
        user: pathosd
        password: "%{DB_PASSWORD}"
        queries:
          - name: headroom
            sql: >-
              SELECT (SELECT count(*) FROM pg_stat_activity) AS used,
                     current_setting('max_connections')::int AS max
            result_jq: ".[0].used < .[0].max * 0.9"
```

### 9. TLS, keep-alive and a proxy host

A server that requires TLS with a private CA; the connection is kept across probes because the handshake is expensive. `host` is a DNS name here, which is fine as long as it resolves quickly.

```yaml
vips:
  - name: mysql-tls
    prefix: 10.10.8.12/32
    check:
      type: sql
      interval: 2s
      timeout: 1s
      sql:
        driver: mysql
        host: db-primary.internal.example
        user: pathosd
        password: "%{DB_PASSWORD}"
        tls: true
        tls_ca_cert: /etc/pathosd/db-ca.crt
        keep_alive: true
```

### 10. Separate cadences: cheap liveness, expensive condition

The liveness query runs every second; the replication check every 10 s. The VIP follows the fast check for outages and the slow one for lag.

```yaml
vips:
  - name: mysql-ro-tiered
    prefix: 10.10.8.13/32
    checks_policy: all
    checks:
      - name: alive
        type: sql
        interval: 1s
        timeout: 500ms
        sql:
          driver: mysql
          user: pathosd
          password: "%{DB_PASSWORD}"
      - name: replication
        type: sql
        interval: 10s
        timeout: 3s
        fall: 2
        sql:
          driver: mysql
          user: pathosd
          password: "%{DB_PASSWORD}"
          queries:
            - sql: SHOW REPLICA STATUS
              result_jq: '(.[0] // {}) | (.Slave_SQL_Running // "Yes") == "Yes" and (.Seconds_Behind_Master // 0) < 60'
    policy:
      fail_action: withdraw
```

## Grants

The check user needs only what the queries touch:

```sql
-- MariaDB / MySQL
CREATE USER 'pathosd'@'10.10.8.%' IDENTIFIED BY '...';
GRANT REPLICA MONITOR ON *.* TO 'pathosd'@'10.10.8.%';   -- MySQL: REPLICATION CLIENT
-- PostgreSQL: pg_is_in_recovery() and the LSN functions need no extra privilege
CREATE ROLE pathosd LOGIN PASSWORD '...';
GRANT pg_monitor TO pathosd;   -- only for pg_stat_* views
```

## Pitfalls

- Alias every expression (`SELECT @@read_only AS read_only`); MySQL otherwise names the column `@@read_only`, which jq cannot address with `.read_only`.
- `SHOW STATUS LIKE` returns `Variable_name`/`Value` columns with string values; compare strings or use `tonumber`.
- `timeout` must cover connect plus authentication plus the queries; a `rise: 2` with a 2 s interval means a fresh primary is announced about 4 s after it starts answering.
- Do not point a check at a pooler (PgBouncer, ProxySQL) when the VIP is meant to follow the server behind it.
- With `fail_action: lower_priority` a lagging replica stays reachable at lower preference; use `withdraw` when stale reads are worse than no reads.
