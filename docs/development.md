# Development

Contributor conventions (layout, code rules, review checklist) are in [AGENTS.md](https://github.com/vooon/pathosd/blob/main/AGENTS.md). Test design notes: [unit-test-guidelines.md](https://github.com/vooon/pathosd/blob/main/docs/unit-test-guidelines.md) and [e2e-test-design.md](https://github.com/vooon/pathosd/blob/main/docs/e2e-test-design.md).

## Building

```bash
make build                             # static binary via go build
goreleaser build --snapshot --clean    # all release targets
docker build -t pathosd .              # container image
```

## Checks before a PR

```bash
go build ./...
go vet -tags e2e ./...
golangci-lint run
go test -race ./...
go generate ./internal/config/... && git diff --exit-code schema/
```

CI runs the same plus `go mod tidy -diff` and `govulncheck`.

## E2E testing

End-to-end tests run pathosd with FRR, BIRD, nginx, CoreDNS, etcd, squid and syslog inside k3d/k3s and validate announce, pessimization, and withdraw behavior across all check types, including an MD5-signed session to FRR.

- Test code: `tests/e2e/e2e_test.go` (`//go:build e2e`)
- Manifests: `tests/e2e/manifests/`
- Design notes: `docs/e2e-test-design.md`

Run locally:

```bash
make e2e
```

Or step-by-step:

```bash
make e2e-cluster
make e2e-build
make e2e-deploy
make e2e-test
```
