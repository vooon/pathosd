# E2E Test Design

## Goal
Validate the full `pathosd` control loop in Kubernetes:

1. health checks change VIP health
2. policy maps health to VIP state
3. embedded GoBGP advertises, withdraws, or pessimizes routes
4. an external BGP peer (FRR) receives the expected route updates

This document describes the current implementation in this repository.

## Current Layout

All E2E manifests are in `tests/e2e/manifests/`, with one file per component:

- `namespace.yaml`
- `frr.yaml`
- `nginx.yaml`
- `nginx-tls.yaml`
- `coredns.yaml`
- `syslog.yaml`
- `etcd.yaml`
- `pathosd.yaml`

Each file can include multiple Kubernetes resources (`ConfigMap`, `Deployment`/`Pod`, `Service`) for that component.

## Runtime Topology

- `pathosd` (ASN 65100): health checker + BGP speaker under test
- `frr` (ASN 65200): BGP peer used for route assertions
- `nginx`: HTTP/HTTPS health target for `web-vip`, `tcp-vip`, `https-vip`
- `coredns`: DNS health target for `dns-vip`
- `syslog`: UDP health target for `udp-vip`
- `etcd`: gRPC health target for `grpc-vip`

All components run in namespace `pathosd-e2e` on k3d/k3s.

## Component Details

### FRR (`frr.yaml`)

- Image: `quay.io/frrouting/frr:10.3.1`
- Runs as a single `Pod` named `frr` (stable `kubectl exec` target)
- Exposes TCP/179 via a headless service (`clusterIP: None`)
- `bgpd.conf` uses a dynamic peer-group with `remote-as external`
- `no bgp ebgp-requires-policy` is set so received routes are accepted in E2E
- Debug logging is enabled in `bgpd.conf` (`debug bgp ...`)

### nginx (`nginx.yaml`)

- Image: `nginx:1.27-alpine`
- `/healthz` returns `200` JSON
- `Deployment` + `Service` on TCP/80

### CoreDNS (`coredns.yaml`)

- Image: `coredns/coredns:1.12.1`
- Serves `example.test` zone from ConfigMap files
- `Deployment` + `Service` on TCP/UDP 53

### etcd (`etcd.yaml`)

- Image: `gcr.io/etcd-development/etcd:v3.5.21`
- Single-node etcd serving gRPC on port 2379 (plaintext)
- Implements `grpc.health.v1.Health/Check` natively
- Headless service: DNS returns pod IP directly; DNS NXDOMAIN on scale-to-0 fails the gRPC dial immediately
- Kubernetes native gRPC readiness probe on port 2379

### pathosd (`pathosd.yaml`)

- Image: `pathosd:e2e`
- Runs with `--config /etc/pathosd/pathosd.yaml`
- Uses env placeholders in config:
  - `%{POD_IP}` for `router_id` and `local_address`
  - `%{FRR_PEER_IP}` for BGP neighbor address
- `wait-frr` init container resolves FRR pod IP from DNS and waits for TCP/179 before start
- Readiness probes `/readyz`; liveness probes `/healthz`

Configured VIPs:

- `web-vip` (`10.100.1.1/32`): HTTP check, `fail_action: lower_priority`
  - includes `lower_priority_file: /tmp/pathosd-web-vip-drain.lock`
  - pessimization uses prepend + community (`65100:666`)
- `dns-vip` (`10.100.2.1/32`): DNS check, `fail_action: withdraw`
- `tcp-vip` (`10.100.3.1/32`): TCP check against nginx:80, `fail_action: withdraw`
- `udp-vip` (`10.100.4.1/32`): UDP check against syslog:514, `fail_action: withdraw`
- `https-vip` (`10.100.5.1/32`): HTTPS check with custom CA cert, `fail_action: withdraw`
- `grpc-vip` (`10.100.6.1/32`): gRPC standard health protocol against etcd:2379, `fail_action: withdraw`

## Test Implementation

Main test file: `tests/e2e/e2e_test.go` (`//go:build e2e`).

### High-level flow

1. Wait for all pods ready (frr, nginx, nginx-tls, coredns, syslog, etcd, pathosd).
2. Port-forward `svc/pathosd` and validate `/healthz`.
3. Wait until `/readyz` reports required peer established.
4. Assert all VIPs become `announced`.
5. Assert FRR receives all six routes.
6. Dedicated lock-file case:
   - create `/tmp/pathosd-web-vip-drain.lock` inside pathosd container
   - assert `web-vip` becomes `pessimized` while still `healthy`
   - assert FRR sees prepended AS path and community `65100:666`
   - remove file and assert recovery to `announced`
7. nginx-down case:
   - scale nginx to 0
   - assert `web-vip` pessimization and `tcp-vip` withdrawal
   - assert FRR route remains with prepended AS path and community
8. nginx-up recovery.
9. coredns-down case:
   - scale coredns to 0
   - assert `dns-vip` withdrawn
   - assert FRR route is removed
10. coredns-up recovery.
11. syslog-down case:
    - scale syslog to 0
    - assert `udp-vip` withdrawn and FRR route removed
12. syslog-up recovery.
13. nginx-tls-down case:
    - scale nginx-tls to 0
    - assert `https-vip` withdrawn and FRR route removed
14. nginx-tls-up recovery.
15. etcd-down case:
    - scale etcd to 0
    - assert `grpc-vip` withdrawn and FRR route removed
16. etcd-up recovery.
17. Assert `/metrics` and ad-hoc trigger API behavior.

### FRR JSON parsing note

FRR output differs between commands:

- `show bgp ipv4 unicast json` can omit detailed community fields in route entries.
- `show bgp ipv4 unicast <prefix> json` includes richer per-path fields (including `community`).

The test uses prefix-specific JSON when asserting pessimization community values.

## CI Flow

Workflow: `.github/workflows/e2e.yaml`

1. Build `pathosd:e2e` using `Dockerfile.e2e`.
2. Import image into k3d.
3. Apply namespace first and wait for it to become `Active`.
4. Apply all manifests.
5. Wait for `frr`, `nginx`, `coredns`, `etcd`, `pathosd` pods.
6. Run `go test -tags=e2e -v -timeout=5m -count=1 ./tests/e2e/...`.
7. On failure, dump pod status, pathosd logs, FRR logs, and FRR summary.

## Local Commands

Use Make targets:

```bash
make e2e-cluster
make e2e-build
make e2e-deploy
make e2e-test
```

Or run full flow:

```bash
make e2e
```

Cleanup:

```bash
make e2e-clean
```
