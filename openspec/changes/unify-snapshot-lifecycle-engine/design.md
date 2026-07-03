## Context

`cluster/snapshot.go` and `cluster/dockersnapshot.go` implement the same
snapshot lifecycle twice. The save side is the clearest: `writeSnapshot` and
`writeDockerSnapshot` both do clone-rootfs → clone-extra-artifacts → (warm) copy
`suspendStateFiles` with a name prefix → write a meta file, differing only in:

| Aspect | cluster | sidecar |
|---|---|---|
| container(s) | `ServerName` (+ `RegistryName`) | `dockerName` |
| primary rootfs artifact | `serverRootfs` | `dockerSnapRootfs` |
| extra artifacts | registry rootfs (optional), `k3s-etc` dir | image-store volume (required) |
| warm state prefix | `server-` | `sidecar-` |
| meta filename | `meta.yaml` | `meta` |
| meta fields | cluster, ip, clusterCidr, serviceCidr | (created, mode only) |
| post-write | `captureClusterConfig` | — |

The restore side diverges more: cluster restore stops server+registry, clones
rootfs+registry+k3s-etc back, does warm-restore **address reclaim** (#35) and
**CIDR checks**, and finishes with `Start`; sidecar restore stops the sidecar
(+gvnet), clones rootfs+volume back, and finishes with `DockerUp` plus a
**virtiofs-share repair** (`790ed41`). List/delete/rename are trivially parallel.

Constraint: this is a refactor. Restore is the most critical path and was only
just fixed (#35); behavior must be preserved exactly, and existing on-disk
snapshots (both meta filenames) must keep restoring.

## Goals / Non-Goals

**Goals:**
- One engine for save/restore/list/delete/rename, parameterized by a target
  descriptor; `snapshot.go`/`dockersnapshot.go` become thin adapters.
- Reconcile the one-sided fixes onto the shared engine via target hooks.
- Unit tests for the engine assembly and both adapters (the fork was untested).
- No behavior change; both meta filenames still restore.

**Non-Goals:**
- Changing snapshot on-disk format, tiers, or CLI surface.
- Frozen-snapshot internals (`frozen.go`) beyond routing through the engine.
- The typed container client (#38) — this change tolerates today's shell-out;
  it does not depend on #38, though it will benefit later.

## Decisions

### D1 — A `snapshotTarget` descriptor, not an interface hierarchy
A single struct of fields + function hooks, constructed by each adapter, rather
than a Go interface with two implementers. Rationale: the differences are almost
all *data* (names, filenames, prefix, meta lines), and the few behavioral
differences (extra-artifact copy, pre-stop, post-restore, bring-up) are naturally
closures over `*config.Config`. A struct-of-funcs keeps the engine linear and
readable and avoids scattering the flow across interface methods.

```go
type snapshotArtifact struct {
    name     string              // dest filename inside the snapshot dir
    src      func() (string, error)
    required bool                // hard error if missing (volume) vs skip (registry)
    dir      bool                // copyDir vs cloneFile
}

type snapshotTarget struct {
    machine     string                         // container holding machine state
    metaFile    string                         // "meta.yaml" | "meta"
    statePrefix string                         // "server-" | "sidecar-"
    rootfs      snapshotArtifact               // primary rootfs
    extras      []snapshotArtifact             // registry rootfs + k3s-etc | volume
    metaLines   func(warm bool) string         // cluster/ip/cidr | created/mode base
    postWrite   func(dir string)               // captureClusterConfig | nil
    stop        func()                         // stop server+registry | sidecar+gvnet
    preRestore  func(dir string) (cold bool)   // IP-reclaim decision (#35) | nil
    postRestore func() error                   // nil | virtiofs repair
    bringUp     func() error                   // Start | DockerUp
}
```

The engine functions become `saveSnapshot(cfg, t, dir, warm)`,
`restoreSnapshot(cfg, t, dir, cold)`, and shared `listSnapshots`/`delete`/`rename`
keyed on the target's snapshot root + meta filename. `SnapshotSave` /
`DockerSnapshotSave` etc. build the target and call the engine.

Alternative considered — a `SnapshottableVM` interface with `Rootfs()`,
`Restore()`, etc.: rejected because it would push the shared warm/cold state
handling either into both implementers (re-forking it) or into an awkward base,
and makes the linear save/restore flow harder to follow than a struct the engine
walks.

### D2 — Meta handling reads both filenames, writes the target's
The engine writes `t.metaFile`. Reads (list, `snapshotMetaValue`, restore) already
locate the file; keep them tolerant of both `meta.yaml` and `meta` so existing
snapshots of either target restore unchanged.

### D3 — Warm/cold state handling moves into the engine once
The `suspendStateFiles` copy loop (write) and the machine-state clone-back
(restore, including the "stale state must be removed first" step) live in the
engine, prefixed by `t.statePrefix`. This is the exact code that differs only by
prefix today.

### D4 — Reconcile divergent fixes as hooks, don't cross-apply blindly
- Cluster `preRestore` carries the #35 address-reclaim decision and CIDR check.
- Sidecar `postRestore` carries the virtiofs repair.
- Loopback health gating lives in the sidecar's `bringUp`/`DockerUp` path already
  and stays there.
These are wired so each target keeps exactly today's behavior; the change does
not, e.g., add CIDR checks to the sidecar (it has no k3s CIDRs).

### D5 — Phase the work; land phase 1 alone
Phase 1 (snapshot engine) is self-contained and the highest-value slice. Phase 2
(pause/resume/suspend) and phase 3 (CLI collapse) are separable, each sizeable,
and each merges independently. `reclaim.go` is already shared
(`reclaimViaPolicy`/`reclaimVM`) and is out of scope.

## Risks / Trade-offs

- **[Restore regression on the critical, recently-fixed path]** → Preserve both
  restore flows step-for-step behind the target; add unit tests asserting the
  engine calls the right hooks and reproduces the #35 reclaim decision and the
  virtiofs post-restore step. Drive a real cluster restore with the `verify`
  skill before merging.
- **[Existing snapshots fail to restore after the meta-filename unification]** →
  D2 reads both filenames; add a test with a fixture of each.
- **[Over-abstraction hiding the linear flow]** → D1's struct-of-funcs keeps the
  engine a readable top-to-bottom sequence; no interface indirection.
- **[Scope creep pulling in #38]** → Explicit non-goal; the engine keeps using
  the current `runContainer` helpers.

## Migration Plan

1. Phase 1: add `snapshotTarget` + engine; convert cluster then sidecar adapters;
   delete the duplicated bodies; add tests; verify a live cluster restore.
2. Phase 2 (separate PR): fold pause/resume/suspend into a lifecycle helper.
3. Phase 3 (separate PR): parameterize one CLI command set.

Rollback: phase 1 is a pure internal refactor on its own branch; revert the
commit if a restore regression appears. No data migration — snapshot format is
unchanged.

## Open Questions

- None blocking phase 1. Phase 2/3 sequencing can be revisited after phase 1
  lands and the target abstraction has proven itself.
