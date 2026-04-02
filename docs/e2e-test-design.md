# E2E Test Design

## Goal
End-to-end test: bring up a BGP peer, health check targets, and pathosd in Kubernetes.
Verify the full flow: check → policy → BGP announce/withdraw/pessimize.

## Chosen Approach: k3d/k3s + Go e2e test

k3d wraps k3s (lightweight K8s) in Docker/podman containers. Pods communicate
via flannel — no special networking tricks needed for BGP between pods.

### Why k3d/k3s
- Cluster ready in ~20s
- Works locally with podman (`K3D_RUNTIME=podman`)
- Works in GitHub Actions via `nolar/setup-k3d-k3s@v1` (no curl|bash)
- Manifests double as deployment documentation
- Hands-on K8s experience

### Alternatives considered
- **docker-compose**: simpler but no K8s experience, less CI-portable
- **testcontainers-go**: Go-native but podman quirks, no K8s
- **minikube**: heavier, slower startup (~2min), no clear advantage over k3d
- **kind**: ~30-40s startup, flaky with podman (`KIND_EXPERIMENTAL_PROVIDER=podman`)

---

## Stack Components

### BGP Peer — FRR
- Image: `quay.io/frrouting/frr:10.3.1`
- Config: accept BGP session from pathosd ASN (65100), no routes originated
- Deployed as a Pod + Service (ClusterIP), port 179

### HTTP Target — nginx
- Image: `nginx:1.27-alpine`
- ConfigMap with `/healthz` location returning 200
- Deployed as Deployment + Service

### DNS Target — CoreDNS
- Image: `coredns/coredns:1.12`
- ConfigMap with Corefile serving `example.test` zone
- Deployed as Deployment + Service, port 53

### pathosd
- Image: built from Dockerfile, loaded via `k3d image import`
- ConfigMap with e2e config (references K8s Service names)
- Deployed as Deployment + Service (exposes API port)

---

## Kubernetes Manifests

All manifests live in `tests/e2e/manifests/`. Applied with `kubectl apply -f tests/e2e/manifests/`.

```
tests/e2e/manifests/
  namespace.yaml          # pathosd-e2e namespace
  frr-configmap.yaml      # FRR bgpd.conf
  frr-pod.yaml            # FRR pod + service
  nginx-configmap.yaml    # nginx.conf with /healthz
  nginx-deployment.yaml   # nginx deployment + service
  coredns-configmap.yaml  # Corefile + zone file
  coredns-deployment.yaml # coredns deployment + service
  pathosd-configmap.yaml  # pathosd.yaml config
  pathosd-deployment.yaml # pathosd deployment + service
```

### pathosd Config (ConfigMap)
```yaml
schema: v1
router:
  asn: 65100
  router_id: 10.100.0.1
api:
  listen: ":8080" # FIX
bgp:
  neighbors:
    - name: frr
      address: frr.pathosd-e2e.svc.cluster.local
      peer_asn: 65200
      port: 179
vips:
  - name: web-vip
    prefix: 10.100.1.1/32
    check:
      type: http
      http:
        host: nginx.pathosd-e2e.svc.cluster.local
        url: /healthz
    policy:
      fail_action: lower_priority
  - name: dns-vip
    prefix: 10.100.2.1/32
    check:
      type: dns
      dns:
        names: [example.test.]
        resolver: coredns.pathosd-e2e.svc.cluster.local
    policy:
      fail_action: withdraw
```

---

## Test File

`tests/e2e/e2e_test.go` with `//go:build e2e`

Uses `kubectl port-forward` or K8s Service NodePort to reach pathosd API.

### Test Flow
1. Wait for pathosd pod ready
2. Wait for `/readyz` → 200 (BGP peer established)
3. Assert VIPs transition to `announced` via `/status`
4. Scale nginx to 0 → assert web-vip pessimizes (lower_priority)
5. Scale nginx back to 1 → assert web-vip recovers to `announced`
6. Delete coredns pod → assert dns-vip withdraws
7. Recreate coredns → assert dns-vip recovers
8. Verify `/metrics` contains expected metric names
9. Verify FRR received routes via `vtysh -c "show bgp summary"` (kubectl exec)

### Helpers
- `waitForPodReady(namespace, labelSelector, timeout)`
- `portForward(namespace, svcName, localPort, remotePort)`
- `kubectlExec(namespace, podName, cmd)`
- `scaleDeploy(namespace, name, replicas)`

---

## GitHub Actions CI

```yaml
name: E2E Tests
on:
  pull_request:
  push:
    branches: [main, first-steps]

jobs:
  e2e:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod

      - uses: nolar/setup-k3d-k3s@v1
        with:
          version: v1.31
          github-token: ${{ secrets.GITHUB_TOKEN }}

      - name: Build and load pathosd image
        run: |
          docker build -t pathosd:e2e .
          k3d image import pathosd:e2e

      - name: Deploy e2e stack
        run: |
          kubectl apply -f tests/e2e/manifests/
          kubectl -n pathosd-e2e wait --for=condition=ready pod -l app=pathosd --timeout=90s

      - name: Run e2e tests
        run: go test -tags=e2e -v -timeout=5m ./tests/e2e/...
```

## Local Development (podman)

```bash
# Install k3d (one-time)
# k3d supports podman via K3D_RUNTIME=podman (experimental)
curl -s https://raw.githubusercontent.com/k3d-io/k3d/main/install.sh | bash

# Or use k3d binary from distro package manager

# Create cluster
k3d cluster create pathosd-e2e

# Build and load image
podman build -t pathosd:e2e .
k3d image import pathosd:e2e -c pathosd-e2e

# Deploy and test
kubectl apply -f tests/e2e/manifests/
go test -tags=e2e -v ./tests/e2e/...

# Cleanup
k3d cluster delete pathosd-e2e
```

## Makefile Targets
```makefile
e2e-cluster:
	k3d cluster create pathosd-e2e --wait

e2e-build:
	docker build -t pathosd:e2e .
	k3d image import pathosd:e2e -c pathosd-e2e

e2e-deploy:
	kubectl apply -f tests/e2e/manifests/
	kubectl -n pathosd-e2e wait --for=condition=ready pod -l app=pathosd --timeout=90s

e2e-test:
	go test -tags=e2e -v -timeout=5m ./tests/e2e/...

e2e-clean:
	k3d cluster delete pathosd-e2e

e2e: e2e-cluster e2e-build e2e-deploy e2e-test
```
