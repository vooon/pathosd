.PHONY: build test generate lint validate-schema vet clean e2e-cluster e2e-build e2e-deploy e2e-test e2e-clean e2e e2e-redeploy

BINARY  := pathosd
VERSION ?= dev
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
DATE    := $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
VERPKG  := github.com/prometheus/common/version
LDFLAGS := -s -w \
	-X $(VERPKG).Version=$(VERSION) \
	-X $(VERPKG).Revision=$(COMMIT) \
	-X $(VERPKG).BuildDate=$(DATE) \
	-X $(VERPKG).Branch=local

build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/pathosd

test:
	go test ./...

generate:
	go generate ./...

lint: vet
	@echo "Lint passed"

vet:
	go vet ./...

validate-schema: generate
	@git diff --exit-code schema/ || (echo "Schema out of date — run 'make generate' and commit" && exit 1)

clean:
	rm -f $(BINARY)

# --- E2E Test Targets ---

E2E_CLUSTER   ?= pathosd-e2e
E2E_IMAGE     := pathosd:e2e
E2E_NAMESPACE := pathosd-e2e

e2e-cluster:
	k3d cluster create $(E2E_CLUSTER) --wait

e2e-build:
	docker build -f Dockerfile.e2e -t $(E2E_IMAGE) .
	k3d image import $(E2E_IMAGE) -c $(E2E_CLUSTER)

e2e-deploy:
	kubectl apply -f tests/e2e/manifests/
	kubectl -n $(E2E_NAMESPACE) wait --for=condition=ready pod -l app=pathosd --timeout=120s

e2e-test:
	go test -tags=e2e -v -timeout=5m -count=1 ./tests/e2e/...

e2e-clean:
	k3d cluster delete $(E2E_CLUSTER)

e2e: e2e-cluster e2e-build e2e-deploy e2e-test

e2e-redeploy: e2e-build
	kubectl -n $(E2E_NAMESPACE) delete pod -l app=pathosd --force --grace-period=0 || true
	kubectl apply -f tests/e2e/manifests/
	kubectl -n $(E2E_NAMESPACE) wait --for=condition=ready pod -l app=pathosd --timeout=120s
