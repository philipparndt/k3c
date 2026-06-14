# runtime/payload

This directory holds the embedded Apple `container` runtime payload for
*bundled* release builds of k3c. The artifact itself is large (~300 MB,
arm64 macOS only) and is **not** committed to git — see `.gitignore`.

## What gets embedded

`container-runtime.tar.gz` — a gzip-compressed tar of the `container`
install tree (the `CONTAINER_INSTALL_ROOT`), laid out as:

```
bin/container
bin/container-apiserver
libexec/container/plugins/container-runtime-linux/{bin/...,config.toml}
libexec/container/plugins/container-network-vmnet/bin/...
libexec/container/plugins/container-core-images/bin/...
libexec/container/plugins/machine-apiserver/{bin/...,resources/...}
init.tar            # guest init image (vminit:latest), loaded on first use
```

At runtime k3c extracts this tree to `~/.cache/k3c/runtime/<version>/` and
invokes `bin/container` with `CONTAINER_INSTALL_ROOT` pointing at the
extraction directory. The init image is loaded via
`container images load -i <root>/init.tar` if `vminit:latest` is missing.

## How it is produced

```
make build           # clone+build the forks into ./tmp, then bundle + go build -tags bundled
```

`make build` is self-contained: it clones the container + containerization
forks (at the refs in the Makefile, kept in sync with the goreleaser workflow)
into `./tmp`, builds the container app + init image there (skipping the slow
Swift builds when the forks are unchanged), assembles the install tree, and
embeds it. `make install` then installs the bundled binary to `GOPATH/bin`.

To bundle a **pre-built** install tree directly (instead of cloning), use
`make bundle STAGING_DIR=/path/to/staging` — the tree is what a
[container](https://github.com/apple/container) build installs (`bin/container`,
`bin/container-apiserver`, `libexec/`). Put `STAGING_DIR`/`INIT_TAR` in a
gitignored `.env` to avoid repeating them.

`make bundle` also writes `container-version.txt` (from
`$(STAGING_DIR)/bin/container --version`, overridable via
`CONTAINER_VERSION=...`); it is embedded alongside the payload and shown by
`k3c version` as `bundled container: ...`.

The init image (`init.tar`) is included automatically if found at
`INIT_TAR` (default: `init.tar` inside the staging dir). Produce it with the
containerization repo:

```
make -C <containerization> init
<containerization>/bin/cctl images save -o init.tar vminit:latest
```

then point `make bundle INIT_TAR=/path/to/init.tar`.

## Build tags

Ordinary `go build ./...` (no `bundled` tag) does **not** embed anything and
needs no artifact here — k3c then drives a host-installed `container`.
Only `go build -tags bundled` embeds `container-runtime.tar.gz`, which must
exist at that point.
