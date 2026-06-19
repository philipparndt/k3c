# registry-and-pull-cache Specification

## Purpose

Let cluster and sidecar VMs pull images they otherwise could not: the guest
does not trust the corporate CA and (even with transparent egress) docker.io is
corp-blocked. A host pull-through cache terminates corporate-CA TLS and does
DNS and egress on the host. This capability owns the pull-cache, registry
mirror configuration, the optional local registry, and image import. It is
managed via `k3c image pull-cache` and the `pullCache`/`localRegistry`/
`mirrors`/`registries` configuration.

## Requirements

### Requirement: Pull-through cache with corporate-CA termination

A host pull-cache SHALL listen on the configured port (default 5011) and serve
guest pulls over plain HTTP, performing DNS, corporate-CA TLS, and egress on the
host. Every registry the cluster pulls from SHALL have a mirror entry pointing
at the host pull-cache; without it the guest pulls the registry directly and
fails with `x509: certificate signed by unknown authority`.

#### Scenario: Pull a corporate-registry image

- **WHEN** a node pulls an image whose registry has a pull-cache mirror entry
- **THEN** the pull is served via the host pull-cache, which terminates
  corporate-CA TLS and egresses on the host, and the pull succeeds

#### Scenario: Missing mirror entry fails CA verification

- **WHEN** a registry mirror loses its pull-cache endpoint
- **THEN** the guest pulls the registry directly without the corporate CA and
  fails with `x509: certificate signed by unknown authority`

### Requirement: Inspect and maintain the pull cache

`k3c image pull-cache` SHALL provide `list` (cached images), `info` (object
count and size), `stats` (hit/miss counters of the running daemons), `clear`
(empty the cache), and `prune` (remove images not pulled within
`pullCache.retentionDays`). The cache SHALL be shared across clusters.

#### Scenario: Prune by retention window

- **WHEN** the user runs `k3c image pull-cache prune`
- **THEN** images not pulled within the configured retention window are removed

#### Scenario: Inspect cache size

- **WHEN** the user runs `k3c image pull-cache info`
- **THEN** the cache object count and total size are printed

### Requirement: Optional local registry

When `localRegistry.enabled` is set, k3c SHALL run a local registry (on a small
dedicated VM) reachable from the cluster, published to the host on
`localRegistry.hostPort`.

#### Scenario: Enable the local registry

- **WHEN** `localRegistry.enabled` is true and the cluster is created
- **THEN** a local registry is available to the cluster and published on the
  configured host port

### Requirement: Import a host image into a cluster

`k3c image import IMAGE [CLUSTER]` SHALL import an image from the host image
store directly into the cluster, bypassing any registry.

#### Scenario: Import a locally built image

- **WHEN** the user runs `k3c image import myapp:dev`
- **THEN** the image is loaded from the host image store into the cluster
