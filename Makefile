# k3c — local k3s clusters on Apple `container`

# local, gitignored overrides (e.g. CONTAINER_REF, FORKS_DIR, STAGING_DIR)
-include .env

BINARY  := k3c
PREFIX  ?= /usr/local
GOBIN   := $(shell go env GOPATH)/bin
LDFLAGS := -s -w \
	-X k3c/version.Version=dev \
	-X k3c/version.GitCommit=$(shell git rev-parse --short HEAD 2>/dev/null || echo unknown) \
	-X k3c/version.BuildDate=$(shell date -u +%Y-%m-%dT%H:%M:%SZ)

# Fork sources for the bundled runtime. Keep these in sync with
# .github/workflows/goreleaser.yaml — the release builds the same refs.
# Pinned to immutable commits (not the feat/gvnet-egress branch tip) so builds
# are reproducible; bump deliberately when the runtime needs a fork update.
FORKS_DIR             ?= tmp
CONTAINER_REPO        ?= https://github.com/philipparndt/container
CONTAINER_REF         ?= 7ed75e128f6151b7297f6133bcc131737965d1ad
CONTAINERIZATION_REPO ?= https://github.com/philipparndt/containerization
CONTAINERIZATION_REF  ?= ba71f683097dbff94cbf1d824427f99bff2a3557
CONTAINER_DIR         := $(FORKS_DIR)/container
CONTAINERIZATION_DIR  := $(FORKS_DIR)/containerization
RUNTIME_STAGE         := $(FORKS_DIR)/stage
RUNTIME_INIT_TAR      := $(FORKS_DIR)/init.tar
RUNTIME_MARKER        := $(FORKS_DIR)/.runtime-built

# embedded runtime payload (built by `make build`, consumed by -tags bundled)
PAYLOAD         := runtime/payload/container-runtime.tar.gz
PAYLOAD_VERSION := runtime/payload/container-version.txt

# `make bundle` can stage a pre-built install tree directly (STAGING_DIR=...);
# `make build` produces that tree from the forks above.
STAGING_DIR ?=
INIT_TAR    ?= $(STAGING_DIR)/init.tar
CONTAINER_VERSION ?=

.DEFAULT_GOAL := help

.PHONY: help all build build-unbundled runtime forks clone-fork fmt vet check test clean clean-forks install install-system uninstall bundle use-brew

# Homebrew tap formula for the released k3c (see brews: in .goreleaser.yaml)
BREW_FORMULA ?= philipparndt/k3c/k3c

help: ## show this help
	@echo "k3c — local k3s clusters on Apple container"
	@echo
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  make %-16s %s\n", $$1, $$2}'

all: check build ## vet + format check + build

# --- build ---

build: runtime ## build k3c bundled (clones+builds the fork runtime into ./tmp)
	@$(MAKE) bundle STAGING_DIR="$(RUNTIME_STAGE)" INIT_TAR="$(RUNTIME_INIT_TAR)"
	go build -tags bundled -ldflags "$(LDFLAGS)" -o $(BINARY) .
	@echo "built bundled $(BINARY)"

build-unbundled: ## build k3c without the runtime (fast dev build; drives a host container)
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) .
	@echo "built $(BINARY) (no bundled runtime — needs a host-installed container)"

# clone/update the container + containerization forks to their refs, as
# siblings under $(FORKS_DIR) (the container Package.swift references
# ../containerization)
forks: ## clone or update the fork repos in ./tmp at the configured refs
	@$(MAKE) --no-print-directory clone-fork DIR="$(CONTAINER_DIR)" REPO="$(CONTAINER_REPO)" REF="$(CONTAINER_REF)"
	@$(MAKE) --no-print-directory clone-fork DIR="$(CONTAINERIZATION_DIR)" REPO="$(CONTAINERIZATION_REPO)" REF="$(CONTAINERIZATION_REF)"

# REF may be a branch name or an immutable commit SHA, so fetch it explicitly
# and check out the fetched commit detached (works for both; `clone --branch`
# and `origin/$(REF)` would only work for a branch).
clone-fork:
	@if [ ! -d "$(DIR)/.git" ]; then \
		echo "cloning $(REPO) @ $(REF) -> $(DIR)"; \
		git clone -q "$(REPO)" "$(DIR)"; \
	else \
		echo "updating $(DIR) -> $(REF)"; \
	fi
	@git -C "$(DIR)" fetch -q origin "$(REF)"
	@git -C "$(DIR)" checkout -q --detach FETCH_HEAD

# build the container app + init image from the forks and assemble the runtime
# stage; the (slow) Swift builds are skipped when the forks are unchanged
runtime: forks ## build the fork runtime (container app + init image) into ./tmp/stage
	@CSHA=$$(git -C "$(CONTAINER_DIR)" rev-parse HEAD); \
	ZSHA=$$(git -C "$(CONTAINERIZATION_DIR)" rev-parse HEAD); \
	KEY="$$CSHA $$ZSHA"; \
	if [ -x "$(RUNTIME_STAGE)/bin/container" ] && [ -f "$(RUNTIME_INIT_TAR)" ] && [ "$$(cat $(RUNTIME_MARKER) 2>/dev/null)" = "$$KEY" ]; then \
		echo "fork runtime up to date ($$KEY); skipping rebuild"; \
	else \
		echo "building container app ($(CONTAINER_REF)) — this is slow on first build"; \
		$(MAKE) -C "$(CONTAINER_DIR)" container BUILD_CONFIGURATION=release; \
		echo "staging the full install tree (all plugins) from the fork"; \
		$(MAKE) -C "$(CONTAINER_DIR)" stage BUILD_CONFIGURATION=release; \
		echo "preparing the Linux cross-compile toolchain for the init image"; \
		$(MAKE) -C "$(CONTAINERIZATION_DIR)/vminitd" cross-prep; \
		echo "building init image ($(CONTAINERIZATION_REF))"; \
		$(MAKE) -C "$(CONTAINERIZATION_DIR)" init BUILD_CONFIGURATION=release; \
		"$(CONTAINERIZATION_DIR)/bin/cctl" images save -o "$(RUNTIME_INIT_TAR)" vminit:latest; \
		echo "assembling runtime stage -> $(RUNTIME_STAGE)"; \
		STG="$(CONTAINER_DIR)/bin/release/staging"; \
		rm -rf "$(RUNTIME_STAGE)"; \
		mkdir -p "$(RUNTIME_STAGE)/bin"; \
		cp "$$STG/bin/container" "$(RUNTIME_STAGE)/bin/"; \
		cp "$$STG/bin/container-apiserver" "$(RUNTIME_STAGE)/bin/"; \
		cp -R "$$STG/libexec" "$(RUNTIME_STAGE)/libexec"; \
		test -x "$(RUNTIME_STAGE)/libexec/container/plugins/container-network-gvnet/bin/container-network-gvnet" \
			|| { echo "ERROR: container-network-gvnet plugin missing from staged runtime — transparent egress would break"; exit 1; }; \
		echo "$$KEY" > "$(RUNTIME_MARKER)"; \
		echo "fork runtime built ($$KEY)"; \
	fi

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

bundle: ## tar a container install tree into runtime/payload (STAGING_DIR=...)
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
	@echo "building gvnet transparent-egress netstack helper into the payload"
	@go build -ldflags "-s -w" -o "$(STAGING_DIR)/bin/gvnet" ./cmd/gvnet
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

clean: ## remove the built binary and bundled payload
	rm -f $(BINARY) $(PAYLOAD) $(PAYLOAD_VERSION)

clean-forks: ## remove the cloned fork repos + runtime stage (./tmp)
	rm -rf $(FORKS_DIR)

# install does NOT rebuild: it installs the binary as previously built, so a
# bundled `make build` is not silently degraded to an unbundled one
install: ## install k3c to GOPATH/bin (no sudo; ensure it is on PATH)
	@test -f $(BINARY) || { echo "no ./$(BINARY) — run 'make build' first"; exit 1; }
	install -m 0755 $(BINARY) $(GOBIN)/$(BINARY)
	@echo "installed: $(GOBIN)/$(BINARY)"

install-system: ## install system-wide to $(PREFIX)/bin (sudo if needed)
	@test -f $(BINARY) || { echo "no ./$(BINARY) — run 'make build' first"; exit 1; }
	@if [ -w $(PREFIX)/bin ]; then \
		install -m 0755 $(BINARY) $(PREFIX)/bin/$(BINARY); \
	else \
		echo "$(PREFIX)/bin is not writable, using sudo..."; \
		sudo install -m 0755 $(BINARY) $(PREFIX)/bin/$(BINARY); \
	fi
	@echo "installed: $(PREFIX)/bin/$(BINARY)"

uninstall: ## remove installed binaries
	rm -f $(GOBIN)/$(BINARY) 2>/dev/null || true
	@if [ -e $(PREFIX)/bin/$(BINARY) ]; then \
		rm -f $(PREFIX)/bin/$(BINARY) 2>/dev/null || sudo rm -f $(PREFIX)/bin/$(BINARY); \
	fi

# switch the active k3c back to the released Homebrew build by removing the
# local go build that shadows it on PATH (the inverse of `make install`)
use-brew: ## activate the Homebrew k3c instead of the local go build
	@command -v brew >/dev/null 2>&1 || { echo "Homebrew not found (https://brew.sh)"; exit 1; }
	@if ! brew list k3c >/dev/null 2>&1; then \
		echo "installing $(BREW_FORMULA) via Homebrew..."; \
		brew install $(BREW_FORMULA); \
	fi
	@if [ -e "$(GOBIN)/$(BINARY)" ]; then \
		echo "removing local go build that shadows brew: $(GOBIN)/$(BINARY)"; \
		rm -f "$(GOBIN)/$(BINARY)"; \
	fi
	@active="$$(command -v $(BINARY) || true)"; \
	echo "active k3c: $${active:-<none on PATH>}"; \
	[ -n "$$active" ] && $$active --version 2>/dev/null | head -1 || true; \
	echo "note: run 'hash -r' (bash) or 'rehash' (zsh) if your shell still resolves the old path"; \
	echo "      'brew upgrade k3c' to get the latest released version"
