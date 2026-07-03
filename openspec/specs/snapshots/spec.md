# snapshots Specification

## Purpose

Save and restore cluster and sidecar state as named, restorable points using
instant APFS copy-on-write, and move snapshots between machines via portable
archives. This capability owns `k3c snapshot` (clusters) and `k3c docker
snapshot` (the sidecar's whole image store).
## Requirements
### Requirement: Save and restore cluster snapshots

`k3c snapshot save [CLUSTER] [NAME]` SHALL save a snapshot of cluster state;
NAME SHALL default to a timestamp. The save SHALL support three tiers selected
by flag — **warm** (default, when suspend is supported), **cold** (`--cold`),
and **frozen** (`--frozen`) — and SHALL record the chosen tier as `mode:` in the
snapshot's `meta.yaml`. `k3c snapshot restore [CLUSTER] NAME` SHALL restore a
snapshot and start the cluster, auto-detecting the tier from `meta.yaml`.
`list`, `rename`, and `delete` SHALL manage saved snapshots; `list` SHALL show
each snapshot's tier, and `rename [CLUSTER] OLD NEW` SHALL rename a stored
snapshot, carrying its pull-cache image pin to the new name.

#### Scenario: Save a warm snapshot

- **WHEN** the user runs `k3c snapshot save` on a running suspend-capable cluster
- **THEN** a timestamp-named warm snapshot is saved without stopping the cluster
  and `meta.yaml` records `mode: warm`

#### Scenario: Save a cold snapshot

- **WHEN** the user runs `k3c snapshot save --cold mysnap`
- **THEN** the cluster is quiesced for a clean-shutdown disk image, the snapshot
  records `mode: cold`, and it boots fresh on restore

#### Scenario: Save a frozen snapshot

- **WHEN** the user runs `k3c snapshot save --frozen mysnap`
- **THEN** a logical snapshot is saved containing the cluster datastore, all
  persistent-volume data, and an image-digest manifest, the container image
  store is omitted, and `meta.yaml` records `mode: frozen`

#### Scenario: Restore auto-detects the tier

- **WHEN** the user runs `k3c snapshot restore mysnap`
- **THEN** the tier is read from the snapshot's `meta.yaml` and the cluster is
  restored using that tier's restore path

#### Scenario: Warm restore reclaims the snapshot address

- **WHEN** the user restores a warm snapshot after the cluster was deleted and
  recreated with swapped addresses (e.g. the registry took the server's former
  IP)
- **THEN** the restore stops the cluster's containers (releasing their
  addresses), the server reclaims the snapshot-time address, and the snapshot
  resumes warm
- **AND** only when a running container outside the cluster holds that address
  does the restore fall back to a cold boot, warning which container blocks it

#### Scenario: Rename a snapshot

- **WHEN** the user runs `k3c snapshot rename mysnap golden`
- **THEN** the stored snapshot `mysnap` is renamed to `golden` and its pinned
  image closure remains pinned under the new name

### Requirement: Export and import cluster snapshots

`k3c snapshot export [CLUSTER] NAME` SHALL export a snapshot to a portable
archive. A warm or cold snapshot SHALL export its disk image (always restoring
cold). A frozen snapshot SHALL export as a logical bundle in one of three modes:

- **fat** (default): self-contained — the datastore, persistent-volume data,
  the local-only images, AND the recoverable image-blob closure (loose files
  from the host pull-cache). Imports and restores fully offline.
- **slim** (`--slim`): the datastore, persistent-volume data, and the
  **local-only** images (those not recoverable from a remote registry —
  pushed to the local registry or `image import`ed); recoverable images
  re-pull from the target's registries on restore.
- **thin** (`--thin`): the datastore and persistent-volume data only, no
  images; only safe when the cluster has no local-only images.

`--slim` and `--thin` SHALL be mutually exclusive. A frozen snapshot SHALL
capture its local-only images (as an OCI archive) at save time, so slim and fat
exports — and local thaws — can restore them without a re-pull. `k3c snapshot
import FILE [NAME]` SHALL import an exported archive into an already-created
cluster; a fat bundle SHALL seed the target pull-cache with its blob closure,
and any bundled local-only images SHALL be imported into containerd on restore.

#### Scenario: Move a snapshot between machines

- **WHEN** the user runs `k3c snapshot export mysnap` on one machine and `k3c
  snapshot import mysnap.tar` on another (after creating the cluster)
- **THEN** the snapshot is packaged as a portable archive and imported on the
  second machine

#### Scenario: Export a frozen snapshot self-contained (fat)

- **WHEN** the user runs `k3c snapshot export mysnap` on a frozen snapshot
  (default fat mode)
- **THEN** the archive bundles the datastore, persistent-volume data, the
  local-only images, and the recoverable image-blob closure, and imports
  without internet access by seeding the target pull-cache and rehydrating

#### Scenario: Export a frozen snapshot slim

- **WHEN** the user runs `k3c snapshot export --slim mysnap` on a frozen snapshot
  that references both remote-registry images and locally pushed images
- **THEN** the archive bundles only the local-only images (omitting the
  remote-registry blob closure), and on import the local images are restored
  from the archive while the remote images re-pull from the target's registries

#### Scenario: Export a frozen snapshot thin

- **WHEN** the user runs `k3c snapshot export --thin mysnap` on a frozen snapshot
- **THEN** the archive omits all images and import re-pulls every referenced
  image from the target's configured registries (which fails to recover any
  local-only image)

### Requirement: Snapshot the Docker sidecar

`k3c docker snapshot save NAME` SHALL snapshot the sidecar's rootfs and entire
image store (every nested k3d cluster) to a named, restorable state; `restore
NAME` SHALL replace the current image store with the snapshot. `--cold` SHALL
quiesce with a stop (save) or boot fresh (restore) instead of using warm
suspend/resume. `list`, `rename`, and `delete` SHALL manage sidecar snapshots.

#### Scenario: Snapshot the whole image store

- **WHEN** the user runs `k3c docker snapshot save before-upgrade`
- **THEN** the sidecar rootfs and full image store are saved as
  `before-upgrade`

#### Scenario: Cold snapshot

- **WHEN** the user runs `k3c docker snapshot save clean --cold`
- **THEN** the sidecar is stopped to quiesce it and a snapshot is taken

### Requirement: Recreate a snapshot in place

`k3c snapshot save` and `k3c docker snapshot save` SHALL accept a `--replace`
flag. With `--replace`, if a snapshot with the given name already exists it SHALL
be deleted and a new snapshot saved in its place using the requested tier;
without `--replace`, saving over an existing name SHALL continue to fail with an
"already exists" error. `--replace` SHALL have no effect when no snapshot of that
name exists (it saves normally).

#### Scenario: Replace an existing snapshot

- **WHEN** the user runs `k3c snapshot save mysnap --replace` and a snapshot
  `mysnap` already exists
- **THEN** the existing `mysnap` is deleted and a new `mysnap` is saved in its
  place with the requested tier

#### Scenario: Save without replace still refuses to overwrite

- **WHEN** the user runs `k3c snapshot save mysnap` (no `--replace`) and a
  snapshot `mysnap` already exists
- **THEN** the save fails with an "already exists" error and the existing
  snapshot is left untouched

#### Scenario: Replace the Docker sidecar snapshot

- **WHEN** the user runs `k3c docker snapshot save mysnap --replace` and a
  sidecar snapshot `mysnap` already exists
- **THEN** the existing sidecar snapshot is deleted and a new one is saved in its
  place

### Requirement: Create and restore a cluster from an archive in one step

`k3c cluster import-run FILE [NAME]` SHALL create a cluster from an exported
snapshot archive and restore it in a single step. It SHALL read the archive's
recorded cluster name (used as the default when NAME is omitted) and its CIDRs,
SHALL create the cluster using those CIDRs (so the restore is not refused on a
CIDR mismatch), then SHALL import and restore the archive. It SHALL refuse to
run when a cluster of the target name already exists. A snapshot SHALL embed the
cluster's project config (`k3c.yaml`), and `import-run` SHALL use that embedded
config by default and SHALL override it when `--config` is given. The
host-specific CA bundle is regenerated at create time and is not embedded.

#### Scenario: Bring up a cluster from an archive on a fresh machine

- **WHEN** the user runs `k3c cluster import-run vehub-clean.k3csnap` on a
  machine that does not yet have the cluster
- **THEN** a cluster is created with the archive's CIDRs, the archive is
  imported, and the cluster is restored — without a separate `cluster create`,
  `snapshot import`, and `snapshot restore`

#### Scenario: Refuse to overwrite an existing cluster

- **WHEN** the user runs `k3c cluster import-run` for a cluster name that
  already exists
- **THEN** the command refuses and directs the user to import + restore into it
  or delete it first

#### Scenario: Recreate uses the embedded config

- **WHEN** the user runs `k3c cluster import-run vehub.k3csnap` with no
  `--config`, and the archive embeds the cluster's `k3c.yaml`
- **THEN** the cluster is created with the embedded settings (sizing, egress,
  mirrors), not generic defaults; passing `--config` overrides them

### Requirement: Snapshot tiers preserve all non-reconstructable data

Every snapshot tier SHALL capture all cluster data that cannot be reconstructed
from another source. Container images are reconstructable from the host
pull-cache and MAY be omitted by a tier; persistent-volume data and the cluster
datastore are not reconstructable and SHALL NOT be omitted by any tier. A frozen
snapshot SHALL include all persistent-volume data.

#### Scenario: Frozen snapshot retains persistent-volume data

- **WHEN** a frozen snapshot is saved of a cluster with stateful workloads
  (e.g. PostgreSQL, Redis) backed by persistent volumes
- **THEN** the snapshot includes every persistent volume's data, and a restore
  brings the workloads back with their data intact

#### Scenario: Frozen snapshot omits the reconstructable image store

- **WHEN** a frozen snapshot is saved
- **THEN** the container image store is omitted from the snapshot and is
  reconstructed on restore from the pull-cache

### Requirement: Frozen snapshot thaw rehydrates images from the pull-cache

Restoring a frozen snapshot ("thaw") SHALL re-create the cluster datastore and
persistent-volume data, boot the cluster fresh, and rehydrate the referenced
container images from the host pull-cache (the configured registry mirror)
without requiring internet access. A frozen restore SHALL be cold-equivalent and
SHALL apply the existing CIDR compatibility check and kubeconfig re-merge.

#### Scenario: Thaw rehydrates images locally

- **WHEN** the user restores a frozen snapshot whose images are present in the
  pull-cache
- **THEN** the cluster boots, containerd pulls the referenced images from the
  pull-cache mirror and unpacks them, and the workloads start without internet
  access

#### Scenario: Thaw fails clearly when a required image is missing

- **WHEN** a frozen snapshot references an image digest no longer present in the
  pull-cache
- **THEN** the restore reports the missing digest rather than silently starting
  an incomplete cluster

### Requirement: Two-phase save with minimal freeze

`k3c snapshot save` SHALL keep the freeze/quiesce window limited to the
consistent capture plus the instant copy-on-write clone (warm/cold) or logical
extract (frozen). All size-reduction work — rootfs re-sparsify and pull-cache
digest pinning — SHALL be performed in a background phase after the cluster
resumes, operating on the immutable snapshot copy and the host pull-cache. The
snapshot SHALL be valid and restorable as soon as the freeze phase completes;
the background phase SHALL be idempotent and crash-safe, and SHALL commit any
pin durably before performing cosmetic shrink steps.

#### Scenario: Cluster resumes before size reduction runs

- **WHEN** a snapshot is saved
- **THEN** the cluster resumes as soon as the capture completes, and the
  re-sparsify and pinning run afterward without re-pausing the cluster

#### Scenario: Snapshot remains valid if the background phase is interrupted

- **WHEN** the background reduction phase is interrupted before completion
- **THEN** the snapshot is still restorable, and re-running the reduction
  completes it without duplicating work

### Requirement: Reduce snapshot and live rootfs image footprint

The cluster's containerd SHALL be configured to discard unpacked image layers so
the live rootfs does not retain compressed layers already unpacked, relying on
the pull-cache mirror to re-serve them on demand. The background save phase SHALL
re-sparsify a snapshot's rootfs clone, returning freed (zeroed) blocks to holes.

#### Scenario: Cold snapshot is smaller after layer discard and sparsify

- **WHEN** a cold snapshot is saved on a cluster configured to discard unpacked
  layers and the background re-sparsify completes
- **THEN** the snapshot's rootfs is materially smaller than the unreduced rootfs,
  with no loss of restorable state

