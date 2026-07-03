## 1. Snapshot engine — save core (phase 1a, this PR)

- [x] 1.1 Add `snapshotArtifact` and `snapshotTarget` types (design D1) in a new `cluster/snapshotengine.go`
- [x] 1.2 Add `clusterSnapshotTarget(cfg)` and `sidecarSnapshotTarget(cfg)` constructors building the descriptors from today's constants (rootfs names, extras, prefixes, state source)
- [x] 1.3 Implement `writeSnapshotArtifacts(dir, t, warm)`: mkdir, clone rootfs, clone/copy extras (required vs skip, file vs dir), warm state loop with `t.statePrefix`
- [x] 1.4 Rewrite `writeSnapshot` and `writeDockerSnapshot` to build a target and call the shared core; keep target-specific meta writing + `captureClusterConfig` in the adapters (they differ in fields)
- [x] 1.5 `go build ./...` and `go test ./cluster/` green

## 2. Tests (phase 1a, this PR)

- [x] 2.1 Unit-test target construction: cluster vs sidecar descriptors have the expected names, prefixes, extras (file vs dir, required vs optional)
- [x] 2.2 Unit-test `writeSnapshotArtifacts` against a temp dir (injected src funcs): correct artifact filenames, required-missing errors, optional-missing skips, dir vs file copy
- [x] 2.3 Unit-test `writeWarmState`: files copied with the target prefix; missing vmstate → error

## 3. Snapshot engine — restore core (phase 1b, in this PR)

- [x] 3.1 Reconcile the restore machine-state divergence: both targets now preserve `machine-identifier.bin` on cold restore and propagate clone errors (previously only the cluster did; the sidecar removed the identifier and ignored errors). Unit-tested in `restoreMachineState` tests.
- [x] 3.2 Extract `restoreSnapshotArtifacts(dir, t, cold)`: restore rootfs + extras + unified stale/warm state handling with `t.statePrefix`
- [x] 3.3 Route `SnapshotRestore`/`DockerSnapshotRestore` through it, keeping the target-specific orchestration hooks: cluster IP reclaim/CIDR (#35), sidecar virtiofs repair (790ed41), frozen-thaw routing
- [ ] 3.4 Meta-filename unification (both `meta.yaml` and `meta`) — deferred: current reads already resolve each target's own file; a full unification is a small separate cleanup
- [ ] 3.5 Route list/delete/rename through one shared implementation — deferred to keep this PR focused on the save+restore core
- [x] 3.6 Verify: real cluster warm + cold restore driven against a scratch cluster (`snaptest`); sidecar reconciliation unit-tested (engine is shared with the verified cluster path)

## 4. Land (phase 1a + 1b)

- [x] 4.1 `openspec validate --all` green
- [x] 4.2 PR opened (#47)

## 5. Phase 2 — lifecycle unification (follow-up PR)

- [ ] 5.1 Extract a shared pause/resume/suspend helper parameterized by machine name + paused-marker path; `Pause/Resume/Suspend` and `DockerPause/Resume/Suspend` become adapters
- [ ] 5.2 Tests + verify; separate PR

## 6. Phase 3 — CLI collapse (follow-up PR)

- [ ] 6.1 Parameterize one snapshot command set over {cluster, sidecar}; replace the `docker snapshot` subtree in `cmd/docker.go` and `cmd/snapshot.go` duplication
- [ ] 6.2 Tests (cobra tree smoke) + verify; separate PR
