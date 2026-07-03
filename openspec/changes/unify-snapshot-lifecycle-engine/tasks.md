## 1. Snapshot engine — save path (phase 1)

- [ ] 1.1 Add `snapshotArtifact` and `snapshotTarget` types (design D1) in a new `cluster/snapshotengine.go`
- [ ] 1.2 Add `clusterSnapshotTarget(cfg)` and `sidecarSnapshotTarget(cfg)` constructors building the descriptors from today's constants (rootfs names, extras, prefixes, meta filenames, meta lines)
- [ ] 1.3 Implement `saveSnapshot(cfg, t, dir, warm)`: mkdir, clone rootfs, clone/copy extras (required vs skip, file vs dir), warm state loop with `t.statePrefix`, write `t.metaFile`, run `t.postWrite`
- [ ] 1.4 Rewrite `writeSnapshot` and `writeDockerSnapshot` to build a target and call `saveSnapshot`; delete the duplicated bodies
- [ ] 1.5 `go build ./...` and `go test ./cluster/` green

## 2. Snapshot engine — restore + list/delete/rename (phase 1)

- [ ] 2.1 Make meta reads tolerant of both `meta.yaml` and `meta` (design D2); confirm `snapshotMetaValue`/list/restore locate either
- [ ] 2.2 Implement `restoreSnapshot(cfg, t, dir, cold)`: `t.stop`, restore rootfs + extras, remove stale machine state, apply warm state via `t.statePrefix`, run `t.preRestore` (cluster IP reclaim/CIDR, #35) and `t.postRestore` (sidecar virtiofs repair, 790ed41), then `t.bringUp`
- [ ] 2.3 Convert `SnapshotRestore` and `DockerSnapshotRestore` to build a target and call `restoreSnapshot`, preserving frozen-thaw routing
- [ ] 2.4 Route list/delete/rename through one shared implementation keyed on the target's snapshot root + meta filename; keep `SnapshotList/Delete/Rename` and `DockerSnapshot*` as adapters
- [ ] 2.5 `go build ./...` and `go test ./cluster/` green

## 3. Tests (phase 1)

- [ ] 3.1 Unit-test target construction: cluster vs sidecar descriptors have the expected names, prefixes, meta filenames, extras
- [ ] 3.2 Unit-test the save assembly against a temp dir (fake rootfs/volume files): correct artifact filenames, warm state files prefixed, meta contents per target
- [ ] 3.3 Unit-test restore hook wiring: cluster `preRestore` reproduces the #35 blocked-IP → cold decision (reuse `containerHolding`); sidecar `postRestore` invokes the virtiofs repair
- [ ] 3.4 Fixture test: a snapshot dir with `meta.yaml` and one with `meta` both list/restore-resolve correctly (design D2)

## 4. Verify & land (phase 1)

- [ ] 4.1 Drive a real cluster warm + cold restore end-to-end (verify skill) against a scratch cluster; confirm no behavior change
- [ ] 4.2 `openspec validate --all` green; PR opened

## 5. Phase 2 — lifecycle unification (follow-up PR)

- [ ] 5.1 Extract a shared pause/resume/suspend helper parameterized by machine name + paused-marker path; `Pause/Resume/Suspend` and `DockerPause/Resume/Suspend` become adapters
- [ ] 5.2 Tests + verify; separate PR

## 6. Phase 3 — CLI collapse (follow-up PR)

- [ ] 6.1 Parameterize one snapshot command set over {cluster, sidecar}; replace the `docker snapshot` subtree in `cmd/docker.go` and `cmd/snapshot.go` duplication
- [ ] 6.2 Tests (cobra tree smoke) + verify; separate PR
