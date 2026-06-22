## Why

Cluster snapshots are large: a warm snapshot of `vehub` is ~36 GB (a 22.8 GB
rootfs clone + a 13.5 GB VM RAM image), and a cold one ~22.8 GB. Measurement
shows ~7 GB of the rootfs is compressed OCI layers that are *also* in the host
pull-cache, and the bulk of a stateful cluster's irreplaceable data (PVCs:
Postgres, Redis, Gitea) is only ~0.4 GB. Today the only size knob is
warm-vs-cold, and the RAM image is incompressible, so there is no way to keep a
cheap, portable checkpoint of a cluster without paying for its entire image
store. We want a size/restore-speed dial that drops only the data that can be
*reconstructed* from the pull-cache, while never dropping data that cannot.

## What Changes

- Add a third snapshot tier **frozen** alongside warm and cold, forming a
  temperature dial that trades snapshot size for restore ("thaw") time:
  - **warm** — CoW-clone the full rootfs + the VM RAM image; resumes in place. *(unchanged)*
  - **cold** — CoW-clone the full rootfs (captures all on-disk data incl. PVCs); boots fresh. *(unchanged behavior; smaller per below)*
  - **frozen** *(new)* — a *logical* extract (k3s datastore + all PVC data + certs/token + an image-digest manifest); drops the reconstructable image store and rehydrates it from the pull-cache on thaw.
- **Correctness invariant:** a snapshot MUST capture everything that is not
  reconstructable. Container images are reconstructable (pull-cache); PVC data
  is not. Frozen therefore keeps **all** PVC data and only drops images.
- `k3c snapshot save` gains tier selection: `--cold` and `--frozen` (default
  stays warm where suspend is supported). Restore auto-detects the tier from
  `meta.yaml`.
- **Pull-cache pin/retention:** a frozen snapshot pins the full image closure
  (manifest + config + layer digests) it depends on, so pull-cache eviction can
  never remove a digest any snapshot still references.
- **Two-phase save:** the freeze window does only the consistent capture + the
  instant clone/extract; all size-reduction (rootfs re-sparsify, digest pinning)
  is deferred to a detached background phase after the cluster resumes.
- **Smaller cold + smaller live cluster:** enable containerd
  `discard_unpacked_layers` (stops the guest hoarding ~7 GB of already-unpacked
  compressed layers) plus a background re-sparsify pass that returns freed
  blocks to holes (cold 22.8 → ~15 GB; live rootfs −7 GB).
- **Frozen export/import:** `--frozen` snapshots export as a portable bundle —
  *fat* (default; self-contained, ships the pinned blob closure as loose files,
  smaller than a cold export) or *thin* (`--thin`; state + PVC data only, relies
  on the target's registries on import).

## Capabilities

### New Capabilities
<!-- none — extends existing snapshots + pull-cache capabilities -->

### Modified Capabilities
- `snapshots`: add the warm/cold/frozen tier model and its correctness
  invariant; add frozen save/restore (logical extract + thaw); add two-phase
  save (minimal freeze + background shrink); add frozen export/import
  (fat/thin); add the rootfs re-sparsify reduction.
- `registry-and-pull-cache`: add snapshot pin/retention — pinned digests are
  never evicted; pull-cache may be seeded by a frozen import.

## Impact

- **Code:** `cluster/snapshot.go` (tier model, frozen save/restore, two-phase
  save), `cluster/transfer.go` (frozen export/import, fat/thin), `cmd/snapshot.go`
  (CLI flags), `cluster/pullcache.go` (pin store, GC union, seed-on-import),
  `cluster/cluster.go` (containerd `discard_unpacked_layers` config), new
  helpers for the logical extract and the background re-sparsify.
- **On-disk formats:** `meta.yaml` gains `mode: frozen`; new per-snapshot image
  manifest; new pull-cache pin records; frozen snapshots are a file bundle
  rather than an ext4 image.
- **Runtime:** containerd config now sets `discard_unpacked_layers=true`; the
  live rootfs shrinks ~7 GB and relies on the pull-cache mirror to re-serve
  discarded layers (already the first registry mirror — no internet dependency).
- **Behavior:** frozen restore takes minutes (image unpack) vs. seconds for
  cold; documented and crash-consistent by default.
- **Non-goals:** no multi-disk VM split; no change to docker-sidecar snapshots;
  warm RAM-image size is unchanged (it is incompressible and irreducible without
  workload changes).
