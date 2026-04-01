.PHONY: build test generate lint validate-schema vet clean

BINARY := pathosd
VERSION ?= dev
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
DATE    := $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

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
