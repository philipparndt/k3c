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
`pullCache.retentionDays`). The cache SHALL be shared across clusters. Pruning
and clearing SHALL NOT remove any blob digest pinned by a snapshot: `prune`
SHALL retain pinned digests regardless of their age, and `clear` SHALL warn and
skip pinned digests unless forced.

#### Scenario: Prune by retention window

- **WHEN** the user runs `k3c image pull-cache prune`
- **THEN** images not pulled within the configured retention window are removed,
  except blob digests pinned by a snapshot, which are retained

#### Scenario: Inspect cache size

- **WHEN** the user runs `k3c image pull-cache info`
- **THEN** the cache object count and total size are printed

#### Scenario: Pinned blobs survive retention

- **WHEN** a frozen snapshot has pinned an image's blob closure and that image
  has not been pulled within the retention window
- **THEN** `prune` retains the pinned blobs so the snapshot remains restorable

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

### Requirement: Snapshot pin and retention

A snapshot SHALL be able to pin the full image closure it depends on
(manifest, config, and layer digests) in the host pull-cache. Pull-cache
retention SHALL treat the union of all snapshots' pinned digests as live and
evict only unpinned, expired blobs. A pin SHALL be recorded durably and SHALL be
released when its snapshot is deleted.

#### Scenario: Pin records an image closure

- **WHEN** a frozen snapshot is saved
- **THEN** the manifest, config, and layer digests of every referenced image are
  recorded as pinned in the pull-cache

#### Scenario: Deleting a snapshot releases its pins

- **WHEN** a snapshot that pinned digests is deleted
- **THEN** its pins are released, and digests no longer pinned by any snapshot
  become eligible for retention again

### Requirement: Seed the pull-cache from a frozen import

Importing a self-contained (fat) frozen bundle SHALL seed the target host
pull-cache with the bundle's image-blob closure before the cluster is started,
adding only digests not already present (content-addressed), so the subsequent
thaw rehydrates from the local pull-cache without internet access.

#### Scenario: Fat import seeds missing blobs

- **WHEN** the user imports a fat frozen bundle on a machine whose pull-cache
  lacks some of the bundle's blobs
- **THEN** the missing blobs are added to the pull-cache, already-present blobs
  are skipped, and the cluster thaws from the local pull-cache

