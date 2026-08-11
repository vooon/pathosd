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
- `bird.yaml`
- `nginx.yaml`
- `nginx-tls.yaml`
- `coredns.yaml`
- `syslog.yaml`
- `etcd.yaml`
- `ipv6-target.yaml`
- `pathosd.yaml`

Each file can include multiple Kubernetes resources (`ConfigMap`, `Deployment`/`Pod`, `Service`) for that component.

## Runtime Topology

- `pathosd` (ASN 65100): health checker + BGP speaker under test; dual-stack (IPv4 + IPv6)
- `frr` (ASN 65200): **IPv4** BGP peer used for IPv4 route assertions
- `bird` (bird3, ASN 65300): **IPv6** BGP peer used for IPv6 route assertions
- `nginx`: HTTP/HTTPS health target for `web-vip`, `tcp-vip`, `https-vip`
- `coredns`: DNS health target for `dns-vip`
- `syslog`: UDP health target for `udp-vip`
- `etcd`: gRPC health target for `grpc-vip`
- `ipv6-target`: TCP health target for `ipv6-vip` (exposed via a headless service returning its pod IPv6 address for the AAAA record)

All components run in namespace `pathosd-e2e` on a dual-stack k3d/k3s cluster (IPv4 + IPv6 pod/service CIDRs).

## Component Details

### FRR (`frr.yaml`)

- Image: `quay.io/frrouting/frr:10.3.1`
- Runs as a single `Pod` named `frr` (stable `kubectl exec` target)
- Exposes TCP/179 via a headless service (`clusterIP: None`)
- `bgpd.conf` uses a dynamic peer-group with `remote-as external`
- `no bgp ebgp-requires-policy` is set so received routes are accepted in E2E
- Debug logging is enabled in `bgpd.conf` (`debug bgp ...`)

### BIRD 3 (`bird.yaml`)

- Image: `bird3:e2e` (built from `Dockerfile.bird3`, Debian trixie + `bird3` package)
- Runs as a single `Pod` named `bird` (stable `kubectl exec` target)
- **Active IPv6 peer**: bird3 dials pathosd's pod IPv6 address; pathosd is `passive` for this session
- A wrapper (`run.sh`) resolves pathosd's pod IPv6 address from the headless `pathosd-bgp` service and templates it into `bird.conf` as `neighbor`, then runs `bird -f`
- Exposes TCP/179 via a headless service (`clusterIP: None`)
- Only IPv6 unicast is carried (`ipv6 { import all; export all; }`) — FRR covers the IPv4 side
- Assertions use `birdc -s /run/bird/bird.ctl` text output

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

### ipv6-target (`ipv6-target.yaml`)

- `alpine` + `socat` running a persistent TCP listener (`TCP-LISTEN:8080,fork`) on both address families
- Headless service: DNS returns the pod's IPv6 address (AAAA); scaling to 0 makes the check fail and withdraws `ipv6-vip`. The IPv6-specific behavior (IPv6 unicast announcement) is validated by bird receiving `ipv6-vip`

### pathosd (`pathosd.yaml`)

- Image: `pathosd:e2e`
- Runs with `--config /etc/pathosd/pathosd.yaml`
- Uses env placeholders in config:
  - `%{POD_IPV4}` for `router_id` (always IPv4)
  - `%{POD_IPV6}` for `router.local_address` (IPv6 next-hop for IPv6 VIPs)
  - `%{FRR_PEER_IP}` for the IPv4 FRR neighbor (with explicit `%{POD_IPV4}` local address)
  - `%{BIRD_PEER_IP}` for the IPv6 bird neighbor
- `wait-frr` init container resolves the FRR pod IPv4 IP; `wait-bird` resolves the bird pod IPv6 IP
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
- `ipv6-vip` (`2001:db8:100::1/128`): TCP check against `ipv6-target:8080` over IPv6, `fail_action: withdraw`; announced via IPv6 unicast to the `bird` peer

## Test Implementation

Main test file: `tests/e2e/e2e_test.go` (`//go:build e2e`).

### High-level flow

1. Wait for all pods ready (frr, bird, nginx, nginx-tls, coredns, syslog, etcd, ipv6-target, pathosd).
2. Port-forward `svc/pathosd` and validate `/healthz`.
3. Wait until `/readyz` reports required peers established (FRR + bird).
4. Assert all VIPs become `announced` (including `ipv6-vip`).
5. Assert FRR receives all six IPv4 routes, and bird receives the IPv6 route.
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
17. ipv6-target-down case:
    - scale ipv6-target to 0
    - assert `ipv6-vip` withdrawn and bird route removed
18. ipv6-target-up recovery:
    - assert `ipv6-vip` announced and bird route present
19. Assert `/metrics` and ad-hoc trigger API behavior.

### FRR vs BIRD assertions

- FRR (`vtysh`) asserts the **IPv4** unicast routes: `show bgp ipv4 unicast json` and prefix-specific JSON for community/AS-path details.
- BIRD (`birdc`) asserts the **IPv6** unicast route: `show route <prefix>` for presence and `show route all <prefix>` to extract `BGP.as_path`. BIRD emits text output (no JSON).

## CI Flow

Workflow: `.github/workflows/e2e.yaml`

1. Build `pathosd:e2e` (`Dockerfile.e2e`) and `bird3:e2e` (`Dockerfile.bird3`); import both into k3d.
2. Create a dual-stack cluster (`--cluster-cidr`/`--service-cidr` with IPv6, `--flannel-ipv6-masq`).
3. Apply namespace first and wait for it to become `Active`.
4. Apply all manifests.
5. Wait for `frr`, `bird`, `nginx`, `coredns`, `etcd`, `pathosd` pods.
6. Run `go test -tags=e2e -v -timeout=5m -count=1 ./tests/e2e/...`.
7. On failure, dump pod status, pathosd logs, FRR logs + summary, and bird logs + `birdc show protocols all`.

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
