GO ?= go
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GOARCH := $(shell $(GO) env GOARCH)
PLATFORM_BINARY := dist/larky-darwin-$(GOARCH)

.PHONY: build test race vet validate verify package test-install clean

build:
	mkdir -p dist
	CGO_ENABLED=1 $(GO) build -trimpath -ldflags "-s -w -X github.com/jtsang4/larky/internal/cli.Version=$(VERSION)" -o $(PLATFORM_BINARY).tmp ./cmd/larky
	mv -f $(PLATFORM_BINARY).tmp $(PLATFORM_BINARY)
	cp $(PLATFORM_BINARY) dist/larky.tmp
	mv -f dist/larky.tmp dist/larky

test:
	$(GO) test ./...

race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

validate: build test vet
	$(GO) test ./internal/plugincheck
	claude plugin validate plugins/claude --strict

verify: build
	./dist/larky verify run --through 3

package: build
	./scripts/package.sh "$(VERSION)"

test-install: package
	./scripts/test-install.sh

clean:
	$(GO) clean -cache -testcache
