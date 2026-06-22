## Context

Snapshots clone a cluster's VM rootfs (APFS copy-on-write) plus, for warm
snapshots, the Virtualization.framework RAM image. Measurement of the `vehub`
cluster:

- `server-rootfs.ext4`: 22.8 GB allocated — ~7 GB compressed OCI layers
  (content store), ~14 GB unpacked overlayfs, ~0.4 GB PVC data, the rest k3s.
- `server-vmstate.czs`: 13.5 GB raw RAM image, incompressible (0.4% zeros).
- The cluster is a single ext4 disk (`/dev/vdb`); macOS cannot mount ext4, so
  per-file surgery on the rootfs from the host is not possible.
- The 7 GB content store duplicates the host pull-cache, which is already the
  first registry mirror. `discard_unpacked_layers` is not set.
- PVC data (`/var/lib/rancher/k3s/storage`) holds live Postgres/Redis/Gitea —
  not reconstructable. The k3s datastore is `state.db` (~66 MB, kine/sqlite).

Constraints that shape the design: the RAM image is irreducible; the rootfs is
mostly already-compressed data so generic compression buys nothing; the only
data that can be *dropped and rebuilt* is the image store, and only because the
pull-cache can re-serve it.

## Goals / Non-Goals

**Goals:**
- A warm/cold/frozen tier dial trading snapshot size for restore time.
- A frozen tier that drops the reconstructable image store while keeping every
  byte of non-reconstructable data (datastore + PVCs).
- Keep snapshot *creation* fast: minimal freeze window, deferred shrink.
- Shrink cold snapshots and the live rootfs by ~7 GB via `discard_unpacked_layers`.
- Portable frozen export/import that works offline (fat) or tiny (thin).
- A pull-cache pin so retention never breaks a snapshot.

**Non-Goals:**
- Splitting the VM into multiple disks (images vs. data) — a cleaner long-term
  separation, but out of scope here.
- Reducing the warm RAM image (incompressible; needs workload/swap changes).
- Changing docker-sidecar snapshots.
- Strong cross-PVC application consistency (frozen is crash-consistent by
  default; a `--quiesce` option is deferred).

## Decisions

### Frozen is a logical extract, not a block clone

Because macOS cannot mount the guest ext4, images cannot be carved out of the
rootfs from the host. Frozen therefore captures data *guest-side* as files: a
sqlite online backup of `state.db`, a tar of `/var/lib/rancher/k3s/storage`, the
k3s server TLS/token, and an image-digest manifest. warm/cold remain block-level
(CoW clone + optional RAM). This split is coherent: warm/cold are physical disk
images; frozen is a logical, inherently portable bundle.

*Alternative considered:* a second ext4 disk for the image store so frozen could
CoW-clone only the data disk. Cleaner and instant, but a large runtime change
(multi-disk VM, separate containerd mount). Deferred to Non-Goals.

### The image store is the only thing frozen drops

Invariant: capture everything not reconstructable. Images are reconstructable
(pull-cache); PVCs and the datastore are not. Frozen size ≈ datastore + PVC data
(~0.5 GB for `vehub`), not ~70 MB as first assumed — the earlier estimate
ignored PVC data and would have destroyed stateful workloads.

### Thaw reuses the normal pull path

A frozen restore seeds `state.db` + PVC data into a fresh cluster and boots it.
The kubelet sees the pods, asks containerd for their images, and containerd
pulls them from the pull-cache mirror and unpacks — no new restore code, just
the existing pull flow pointed at a populated local cache. Measured cost on
`vehub`: ~2–4 min, dominated by gzip unpack (127 MB/s single-stream, ~1.9×
parallel scaling across 11 cores), not byte movement (pull-cache reads at
4.9 GB/s).

### Two-phase save: minimal freeze, background shrink

Phase 1 (freeze): the consistent capture + the instant CoW clone (warm/cold) or
the logical extract (frozen). Phase 2 (background, post-resume, detached): rootfs
re-sparsify and pull-cache pinning, operating on the immutable snapshot copy and
the host pull-cache. The snapshot is restorable the instant Phase 1 ends. Phase 2
is idempotent and crash-safe; **the pin commits before the cosmetic sparsify** —
a lost pin breaks a future thaw, a lost sparsify only leaves a larger snapshot.

### Pull-cache pin as the load-bearing safety mechanism

Each frozen snapshot records its image closure (manifest + config + layer
digests) as a durable pin under the pull-cache. Retention computes the union of
all snapshots' pins and evicts only the complement. The same pin guarantees the
blobs are present for local thaw, for fat export (read as loose files), and
across retention runs. Deleting a snapshot releases its pin.

### Frozen export: fat (default) vs thin

Fat bundles the pinned blob closure as loose files from the pull-cache — already
compressed, so it ships ~7 GB but *smaller* than a cold export's ~14 GB unpacked
overlayfs, and needs no ext4 mounting. Import seeds the target pull-cache
(content-addressed, dedups) then thaws — fully offline. Thin ships only datastore
+ PVC data (~0.5 GB) and re-pulls from the target's registries on import.

### `discard_unpacked_layers` + background re-sparsify

Set containerd `discard_unpacked_layers=true` via the k3s containerd config
template so the live rootfs stops retaining the 7 GB of already-unpacked
compressed layers; the pull-cache mirror re-serves any layer on demand. A
background re-sparsify pass (reusing `transfer.go`'s `punchHole` + SEEK
machinery) returns freed/zeroed blocks in a snapshot's rootfs clone to holes.
Orthogonal to frozen; benefits warm and cold and the live cluster.

## Risks / Trade-offs

- **[Frozen drops data if the invariant is violated]** → The invariant is
  explicit in the spec and tested: a frozen save MUST include all of
  `/var/lib/rancher/k3s/storage`. A scenario asserts stateful workloads restore
  with data intact.
- **[Crash-consistent PVC capture]** → Acceptable for crash-safe engines
  (Postgres/Kafka recover on start — the same guarantee a cold/power-loss
  restore already gives). A `--quiesce` option is deferred for stronger
  guarantees.
- **[Pin lost → broken thaw/export]** → Pin commits durably in Phase 1/early
  Phase 2, before any reduction; retention reads the union; delete releases.
- **[`discard_unpacked_layers` + unreachable mirror]** → The pull-cache is local
  and permanent and is already the first mirror, so a discarded layer is always
  re-servable; no internet dependency is introduced.
- **[Background sparsify races a concurrent restore/save]** → Phase 2 is scoped
  to one snapshot dir and is idempotent; guard against operating on a snapshot
  being restored.
- **[Frozen thaw latency surprises users]** → Documented as minutes vs. seconds;
  `snapshot list` shows the tier so the trade-off is visible. If unpack proves
  too slow, re-storing pull-cache layers as zstd is a future optimization.

## Migration Plan

- Additive: existing warm/cold snapshots and their `meta.yaml` keep working;
  `mode: frozen` and the manifest/pin files are new.
- `discard_unpacked_layers` applies to newly started clusters; existing rootfs
  shrink is realized on the next save's background sparsify or a manual prune.
- Rollback: the tier flags and frozen paths are independent of warm/cold; the
  containerd config change can be reverted in the template without affecting
  existing snapshots.

## Open Questions

- Exact location/format of the pin store under `~/.config/k3c/pull-cache`
  (per-snapshot pin file vs. a central index) — leaning per-snapshot file unioned
  at GC time for crash-safety.
- Whether frozen should also capture cluster-scoped non-PVC state that lives
  outside `state.db` (none known for default k3s, but verify CNI/host-local IPAM
  state is reconstructed on boot).
