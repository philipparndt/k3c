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

## 3. Snapshot engine — restore core (phase 1b, follow-up PR — needs live verify)

- [ ] 3.1 Reconcile the restore machine-state divergence: cluster cold-restore preserves `machine-identifier.bin` and error-checks clones; sidecar removes all state files and ignores clone errors. Decide the unified behavior (cluster's looks more correct) and confirm it against a live sidecar restore before landing.
- [ ] 3.2 Extract `restoreSnapshotArtifacts(dir, t)`: restore rootfs + extras + unified stale/warm state handling with `t.statePrefix`
- [ ] 3.3 Route `SnapshotRestore`/`DockerSnapshotRestore` through it, keeping the target-specific orchestration hooks: cluster IP reclaim/CIDR (#35), sidecar virtiofs repair (790ed41), frozen-thaw routing
- [ ] 3.4 Make meta reads tolerant of both `meta.yaml` and `meta` (design D2); fixture test for each
- [ ] 3.5 Route list/delete/rename through one shared implementation; keep adapters
- [ ] 3.6 Verify: real cluster warm+cold restore AND a sidecar snapshot restore against scratch targets (verify skill)

## 4. Land (phase 1a)

- [x] 4.1 `openspec validate --all` green
- [ ] 4.2 PR opened

## 5. Phase 2 — lifecycle unification (follow-up PR)

- [ ] 5.1 Extract a shared pause/resume/suspend helper parameterized by machine name + paused-marker path; `Pause/Resume/Suspend` and `DockerPause/Resume/Suspend` become adapters
- [ ] 5.2 Tests + verify; separate PR

## 6. Phase 3 — CLI collapse (follow-up PR)

- [ ] 6.1 Parameterize one snapshot command set over {cluster, sidecar}; replace the `docker snapshot` subtree in `cmd/docker.go` and `cmd/snapshot.go` duplication
- [ ] 6.2 Tests (cobra tree smoke) + verify; separate PR
