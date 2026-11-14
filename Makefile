VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

.PHONY: build test test-integration test-eval lint vuln sec fmt

build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/memorable ./cmd/memorable

test:
	go test -race ./...

test-integration:
	go test -race -tags=integration ./...

test-eval:
	go test -tags=eval -run TestEval -v ./internal/memory/

lint:
	golangci-lint run ./...

vuln:
	go tool govulncheck ./...

sec:
	golangci-lint run --enable-only gosec ./...

fmt:
	gofmt -s -w .
