## Why

The cluster snapshot path (`cluster/snapshot.go`) and the docker-sidecar snapshot
path (`cluster/dockersnapshot.go`) are a copy-paste fork: `writeSnapshot` vs
`writeDockerSnapshot` are near-identical (clone rootfs → clone store → copy warm
machine-state files → write meta), and `SnapshotSave/Restore/List/Delete/Rename`
are mirrored by `DockerSnapshot*` with drifted details. Every fix must land twice
and already misses one side — the warm-restore IP reclaim (#35) shipped
cluster-only, the virtiofs-share repair (`790ed41`) shipped docker-only. This is
the single biggest driver of future snapshot bugs, and neither fork has tests.

## What Changes

- Introduce a `snapshotTarget` abstraction describing everything a snapshottable
  VM differs by: its container name(s), rootfs path(s), the extra per-snapshot
  artifacts to clone (image store volume, registry rootfs, k3s-etc), the
  machine-state file prefix, the meta filename, and pre/post-restore hooks.
- Drive one snapshot engine (save, restore, list, delete, rename) for both
  cluster and sidecar; `snapshot.go` and `dockersnapshot.go` shrink to two thin
  adapters that construct a target.
- Reconcile the one-sided fixes so both targets get the correct behavior: the
  cluster target keeps warm-restore IP reclaim and CIDR checks; the sidecar
  target keeps virtiofs-share repair and loopback health gating; the shared
  engine owns the common warm/cold machine-state handling.
- Add unit tests for the unified engine and both target adapters — the fork was
  untested; the merged engine is born testable.
- **Phase 2 (follow-up, tracked as tasks):** unify the lifecycle verbs
  (pause/resume/suspend) that `pause.go` and `dockerlifecycle.go` duplicate.
- **Phase 3 (follow-up, tracked as tasks):** collapse the two CLI command trees
  (`cmd/snapshot.go` and the `docker snapshot` subtree in `cmd/docker.go`) into
  one parameterized set.

This is a refactor: **no user-visible behavior changes.** The reconciliation only
removes accidental divergence, it does not add or remove features.

## Capabilities

### New Capabilities
<!-- none: this is an internal refactor of existing capabilities -->

### Modified Capabilities
- `snapshots`: add a requirement that cluster and docker-sidecar snapshots share
  a single save/restore engine with consistent semantics, so tier handling
  (warm/cold/frozen), warm machine-state restore, and post-restore repair stay in
  parity across both. No change to existing save/restore/list/rename/export
  behavior.

## Impact

- `cluster/snapshot.go`, `cluster/dockersnapshot.go` — unified engine + adapters
- Reconciled behaviors: warm-restore IP reclaim (#35), virtiofs repair
  (`790ed41`), loopback health gate — all preserved, routed through target hooks
- New tests: `cluster/snapshotengine_test.go` (or similar)
- Phase 2/3 touch `cluster/pause.go`, `cluster/dockerlifecycle.go`,
  `cmd/snapshot.go`, `cmd/docker.go`
- No config, CLI-surface, or on-disk-format changes; existing snapshots restore
  unchanged
