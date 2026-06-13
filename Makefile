# k3c — local k3s clusters on Apple `container`

# local, gitignored overrides (e.g. STAGING_DIR, INIT_TAR, CONTAINER_VERSION)
# so plain `make build-bundled` works without arguments
-include .env

BINARY  := k3c
PREFIX  ?= /usr/local
GOBIN   := $(shell go env GOPATH)/bin
LDFLAGS := -s -w \
	-X k3c/version.Version=dev \
	-X k3c/version.GitCommit=$(shell git rev-parse --short HEAD 2>/dev/null || echo unknown) \
	-X k3c/version.BuildDate=$(shell date -u +%Y-%m-%dT%H:%M:%SZ)

# bundled runtime: tree of the Apple `container` install root staged here
# (bin/container, bin/container-apiserver, libexec/...); no default — pass
# STAGING_DIR=... (see runtime/payload/README.md)
STAGING_DIR ?=
# guest init image (vminit:latest); defaults to init.tar inside the staging
# dir if present (see runtime/payload/README.md)
INIT_TAR ?= $(STAGING_DIR)/init.tar
PAYLOAD  := runtime/payload/container-runtime.tar.gz
# version of the bundled container runtime, shown by `k3c version`; derived
# from the staged binary unless passed explicitly
CONTAINER_VERSION ?=
PAYLOAD_VERSION := runtime/payload/container-version.txt

.DEFAULT_GOAL := help

.PHONY: help all build fmt vet check test clean install install-user uninstall bundle build-bundled

help: ## show this help
	@echo "k3c — local k3s clusters on Apple container"
	@echo
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  make %-14s %s\n", $$1, $$2}'
	@echo
	@echo "variables: PREFIX=$(PREFIX) (install prefix)"

all: check build ## vet + format check + build

build: ## build the k3c binary
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) .

fmt: ## format the Go sources
	gofmt -w .

vet: ## run go vet
	go vet ./...

test: ## run tests
	go test ./...

check: vet ## vet + fail on unformatted files
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt needed for: $$unformatted"; exit 1; \
	fi

bundle: ## stage the container install tree into runtime/payload (STAGING_DIR=...)
	@test -n "$(STAGING_DIR)" || { echo "set STAGING_DIR to a container install tree (see runtime/payload/README.md)"; exit 1; }
	@test -d "$(STAGING_DIR)" || { echo "STAGING_DIR not found: $(STAGING_DIR)"; exit 1; }
	@test -x "$(STAGING_DIR)/bin/container" || { echo "no bin/container under $(STAGING_DIR)"; exit 1; }
	@mkdir -p runtime/payload
	@echo "bundling container runtime from $(STAGING_DIR)"
	@rm -f $(PAYLOAD) $(PAYLOAD_VERSION)
	@if [ -n "$(CONTAINER_VERSION)" ]; then \
		printf '%s\n' "$(CONTAINER_VERSION)" > $(PAYLOAD_VERSION); \
	else \
		"$(STAGING_DIR)/bin/container" --version 2>/dev/null | tail -1 > $(PAYLOAD_VERSION) \
			|| printf 'unknown\n' > $(PAYLOAD_VERSION); \
	fi
	@echo "container version: $$(cat $(PAYLOAD_VERSION))"
	@if [ -f "$(INIT_TAR)" ]; then \
		echo "including init image: $(INIT_TAR)"; \
		if [ "$(INIT_TAR)" != "$(STAGING_DIR)/init.tar" ]; then \
			cp "$(INIT_TAR)" "$(STAGING_DIR)/init.tar"; \
			tar -C "$(STAGING_DIR)" -czf $(PAYLOAD) . ; \
			rm -f "$(STAGING_DIR)/init.tar"; \
		else \
			tar -C "$(STAGING_DIR)" -czf $(PAYLOAD) . ; \
		fi; \
	else \
		echo "WARNING: init image not found at $(INIT_TAR); bundling without init.tar"; \
		echo "         first-use system bootstrap will need the init image another way"; \
		echo "         (see runtime/payload/README.md)"; \
		tar -C "$(STAGING_DIR)" -czf $(PAYLOAD) . ; \
	fi
	@echo "wrote $(PAYLOAD) ($$(du -h $(PAYLOAD) | cut -f1))"

build-bundled: bundle ## bundle the runtime then build k3c with it embedded
	go build -tags bundled -ldflags "$(LDFLAGS)" -o $(BINARY) .
	@echo "built bundled $(BINARY)"

clean: ## remove the built binary and bundled payload
	rm -f $(BINARY) $(PAYLOAD) $(PAYLOAD_VERSION)

# the install targets deliberately do NOT rebuild: they install the binary
# as previously built (make build OR make build-bundled), so installing a
# bundled build does not silently degrade to an unbundled one
install: ## install system-wide to $(PREFIX)/bin (sudo if needed)
	@test -f $(BINARY) || { echo "no ./$(BINARY) — run 'make build' or 'make build-bundled' first"; exit 1; }
	@if [ -w $(PREFIX)/bin ]; then \
		install -m 0755 $(BINARY) $(PREFIX)/bin/$(BINARY); \
	else \
		echo "$(PREFIX)/bin is not writable, using sudo..."; \
		sudo install -m 0755 $(BINARY) $(PREFIX)/bin/$(BINARY); \
	fi
	@echo "installed: $(PREFIX)/bin/$(BINARY)"

install-user: ## install to GOPATH/bin (no sudo; ensure it is on PATH)
	@test -f $(BINARY) || { echo "no ./$(BINARY) — run 'make build' or 'make build-bundled' first"; exit 1; }
	install -m 0755 $(BINARY) $(GOBIN)/$(BINARY)
	@echo "installed: $(GOBIN)/$(BINARY)"

install-bundled: build-bundled install-user ## build the bundled binary and install it to GOPATH/bin

uninstall: ## remove installed binaries
	rm -f $(GOBIN)/$(BINARY) 2>/dev/null || true
	@if [ -e $(PREFIX)/bin/$(BINARY) ]; then \
		rm -f $(PREFIX)/bin/$(BINARY) 2>/dev/null || sudo rm -f $(PREFIX)/bin/$(BINARY); \
	fi
