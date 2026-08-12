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
- `gobgp.yaml`
- `squid.yaml`
- `httpbin.yaml`
- `nginx.yaml`
- `nginx-tls.yaml`
- `coredns.yaml`
- `syslog.yaml`
- `etcd.yaml`
- `ipv6-target.yaml`
- `pathosd.yaml`

Each file can include multiple Kubernetes resources (`ConfigMap`, `Deployment`/`Pod`, `Service`) for that component.

## Runtime Topology

- `pathosd` (ASN 65100): health checker + BGP speaker under test
- `frr` (ASN 65200): **IPv4** BGP peer used for IPv4 route assertions
- `bird` (bird3, ASN 65300): **IPv6** BGP peer used for IPv6 route assertions
- `gobgp` (GoBGP, ASN 65400): third BGP peer carrying both IPv4 and IPv6; queried via gRPC to confirm received routes
- `nginx`: HTTP/HTTPS health target for `web-vip`, `tcp-vip`, `https-vip`
- `coredns`: DNS health target for `dns-vip`
- `syslog`: UDP health target for `udp-vip`
- `etcd`: gRPC health target for `grpc-vip`
- `ipv6-target`: TCP health target for `ipv6-vip`
- `squid`: forward HTTP proxy used by `squid-vip`
- `httpbin`: local HTTP target (go-httpbin) reached through the Squid proxy

All components run in namespace `pathosd-e2e` on an IPv4 k3d/k3s cluster. IPv6 VIP routes are carried over IPv4-transport MP-BGP (no host IPv6 required).

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
- **Active IPv6-carrying peer**: bird3 dials pathosd over IPv4 transport and negotiates IPv6-unicast (MP-BGP); pathosd is `passive` for this session
- A wrapper (`run.sh`) resolves pathosd's pod IPv4 address from the headless `pathosd-bgp` service and templates it into `bird.conf` as `neighbor`, then runs `bird -f`
- Exposes TCP/179 via a headless service (`clusterIP: None`)
- Only IPv6 unicast is carried (`ipv6 { import all; export none; }`) — FRR covers the IPv4 side
- Assertions use `birdc -s /run/bird/bird.ctl` text output
- Note: GitHub Actions runner pods have IPv6 disabled, so BIRD cannot hold IPv6 routes in its table. The e2e validates the IPv6 origin path end-to-end instead: the IPv6 VIP is announced by pathosd and the bird IPv6-capable peer session is `Established`.

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

- `alpine` + `socat` running a persistent TCP listener (`TCP-LISTEN:8080,fork`)
- Headless service; scaling to 0 makes the check fail and withdraws `ipv6-vip`. The IPv6-specific behavior (IPv6 unicast announcement) is validated by bird receiving `ipv6-vip`

### gobgp (`gobgp.yaml`)

- Image: `gobgp:e2e` (built from `Dockerfile.gobgp`, the standalone `gobgpd` from the GoBGP v4 module)
- Runs as a `Pod` named `gobgp`; a wrapper resolves pathosd's pod IPv4 address from the headless `pathosd-bgp` service and templates it into `gobgpd.toml`, then runs `gobgpd`
- **Active peer** that dials pathosd (pathosd is `passive`) and carries both IPv4 and IPv6 unicast
- Unlike BIRD, GoBGP stores routes with unreachable next-hops, so it holds the IPv6 VIP route in its RIB
- Exposes TCP/179 and the GoBGP gRPC API (50051); the e2e queries the RIB via gRPC to assert the IPv4 and IPv6 VIP routes were actually received

### squid (`squid.yaml`)

- Image: `squid:e2e` (built from `Dockerfile.squid`, Debian trixie + `squid`)
- Runs a permissive forward proxy on TCP/3128 with a minimal `squid.conf`
- Exposed via a ClusterIP service; `squid-vip` routes its HTTP check through it

### httpbin (`httpbin.yaml`)

- Local `mccutchen/go-httpbin:2.25.0` target so the Squid proxy test has no external dependency
- `squid-vip` fetches `http://httpbin.../status/201` through Squid and expects HTTP 201

### pathosd (`pathosd.yaml`)

- Image: `pathosd:e2e`
- Runs with `--config /etc/pathosd/pathosd.yaml`
- Uses env placeholders in config:
  - `%{POD_IPV4}` for `router_id` and `router.local_address`
  - `router.local_address_ipv6` is a fixed IPv6 address used as the IPv6 next-hop
  - `%{FRR_PEER_IP}` for the IPv4 FRR neighbor
  - `%{BIRD_PEER_IP}` for the (IPv4-transport) bird neighbor
- `wait-frr` and `wait-bird` init containers resolve the FRR/bird pod IPv4 IPs
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
- `ipv6-vip` (`2001:db8:100::1/128`): TCP check against `ipv6-target:8080`, `fail_action: withdraw`; announced via IPv6 unicast (over IPv4 transport) to the `bird` peer
- `squid-vip` (`10.100.7.1/32`): HTTP check routed through the Squid proxy to the local httpbin `/status/201` expecting `201`, `fail_action: withdraw`

## Test Implementation

Main test file: `tests/e2e/e2e_test.go` (`//go:build e2e`).

### High-level flow

1. Wait for all pods ready (frr, bird, nginx, nginx-tls, coredns, syslog, etcd, ipv6-target, pathosd).
2. Port-forward `svc/pathosd` and validate `/healthz`.
3. Wait until `/readyz` reports required peers established (FRR + bird).
4. Assert all VIPs become `announced` (including `ipv6-vip`).
5. Assert FRR receives all six IPv4 routes, and the bird IPv6 peer is established with `ipv6-vip` announced.
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
    - assert `ipv6-vip` withdrawn
18. ipv6-target-up recovery:
    - assert `ipv6-vip` announced and bird peer established
19. gobgp route check:
    - query gobgp's gRPC RIB and assert it received both the IPv4 and IPv6 VIP routes
20. squid-vip case:
    - assert `squid-vip` announced through the working proxy
    - scale squid to 0; assert `squid-vip` withdrawn and FRR route removed
    - scale squid to 1; assert recovery to `announced`
21. Assert `/metrics` and ad-hoc trigger API behavior.

### FRR vs BIRD vs GoBGP assertions

- FRR (`vtysh`) asserts the **IPv4** unicast routes: `show bgp ipv4 unicast json` and prefix-specific JSON for community/AS-path details.
- BIRD (`birdc`) asserts the **IPv6** peer session is `Established` (`show protocols all`) and the IPv6 VIP is announced by pathosd. BIRD emits text output (no JSON). Because runner pods have no IPv6, BIRD's routing table cannot hold the IPv6 route, so only the peer session (which carries IPv6 unicast via MP-BGP) is asserted.
- GoBGP is queried via its gRPC API (`ListPath`) to assert it actually received the **IPv4 and IPv6** VIP routes in its RIB — the strongest proof a peer received the routes.

## CI Flow

Workflow: `.github/workflows/e2e.yaml`

1. Build `pathosd:e2e` (`Dockerfile.e2e`), `bird3:e2e` (`Dockerfile.bird3`), `squid:e2e` (`Dockerfile.squid`), and `gobgp:e2e` (`Dockerfile.gobgp`); import all into k3d.
2. Create an IPv4 k3d/k3s cluster.
3. Apply namespace first and wait for it to become `Active`.
4. Apply all manifests.
5. Wait for `frr`, `bird`, `gobgp`, `squid`, `nginx`, `coredns`, `etcd`, `pathosd` pods.
6. Run `go test -tags=e2e -v -timeout=5m -count=1 ./tests/e2e/...`.
7. On failure, dump pod status, pathosd logs, FRR logs + summary, bird logs + `birdc show protocols all`, squid logs, and gobgp logs.

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
