## MODIFIED Requirements

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

## ADDED Requirements

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
