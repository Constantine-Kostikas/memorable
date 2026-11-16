VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

.PHONY: build test test-integration test-eval lint vuln sec fmt css css-check htmx-check fonts-check

TAILWIND_VERSION          := v4.3.3
TAILWIND_SHA256_linux_x64   := dc61b3ac6b8c9ca874c0cc4c57b2409791a64c5540404ca5f5367360babc313a
TAILWIND_SHA256_linux_arm64 := 55fd0b241214eff3de1e8ee4f22796662f2d2e7a49bcfca7477cfd0bac398195
TAILWIND_SHA256_macos_x64   := 7922e0953f2110c05976e3bf58f14e643d90427575e766b7d433f5f80cbee7e1
TAILWIND_SHA256_macos_arm64 := cdf646702987a743464dff4d9c60fd4480d1c1e73dd819a9a67f1078815dce9d
HTMX_VERSION              := v2.0.10
HTMX_SHA256               := 71ea67185bfa8c98c39d31717c6fce5d852370fcdfd129db4543774d3145c0de
FONT_SHA256_REGULAR       := b09653e3ba9d95e26da5c408979f40451990a4573ce5f96abe6982e2fcb09e6c
FONT_SHA256_BOLD          := d8e8b1e0e929651439e25e23ade4b9d6cac073f2444aadb8e8b85431726c2036

TAILWIND_OS   := $(if $(filter Darwin,$(shell uname -s)),macos,linux)
TAILWIND_ARCH := $(if $(filter x86_64 amd64,$(shell uname -m)),x64,arm64)
TAILWIND_PLATFORM := $(TAILWIND_OS)-$(TAILWIND_ARCH)
TAILWIND_SHA256   := $(TAILWIND_SHA256_$(TAILWIND_OS)_$(TAILWIND_ARCH))
TAILWIND_URL      := https://github.com/tailwindlabs/tailwindcss/releases/download/$(TAILWIND_VERSION)/tailwindcss-$(TAILWIND_PLATFORM)

WEBUI_CSS := internal/webui/static/app.css
WEBUI_HTMX := internal/webui/static/htmx.min.js
WEBUI_FONT_REGULAR := internal/webui/static/atkinson-hyperlegible-regular.woff2
WEBUI_FONT_BOLD := internal/webui/static/atkinson-hyperlegible-bold.woff2

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

bin/tailwindcss:
	@mkdir -p bin
	curl -fsSL -o bin/tailwindcss.download $(TAILWIND_URL)
	@echo "$(TAILWIND_SHA256)  bin/tailwindcss.download" | sha256sum -c - 2>/dev/null \
		|| echo "$(TAILWIND_SHA256)  bin/tailwindcss.download" | shasum -a 256 -c -
	chmod +x bin/tailwindcss.download
	mv bin/tailwindcss.download bin/tailwindcss

css: bin/tailwindcss
	bin/tailwindcss -i internal/webui/input.css -o $(WEBUI_CSS) --minify

css-check: css
	git diff --exit-code $(WEBUI_CSS)

htmx-check:
	@echo "$(HTMX_SHA256)  $(WEBUI_HTMX)" | sha256sum -c - 2>/dev/null \
		|| echo "$(HTMX_SHA256)  $(WEBUI_HTMX)" | shasum -a 256 -c -

fonts-check:
	@echo "$(FONT_SHA256_REGULAR)  $(WEBUI_FONT_REGULAR)" | sha256sum -c - 2>/dev/null \
		|| echo "$(FONT_SHA256_REGULAR)  $(WEBUI_FONT_REGULAR)" | shasum -a 256 -c -
	@echo "$(FONT_SHA256_BOLD)  $(WEBUI_FONT_BOLD)" | sha256sum -c - 2>/dev/null \
		|| echo "$(FONT_SHA256_BOLD)  $(WEBUI_FONT_BOLD)" | shasum -a 256 -c -
