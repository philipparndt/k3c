## 1. Foundations & shared contract

- [ ] 1.1 Add a `SnapshotMode` type (`warm`/`cold`/`frozen`) and parse/serialize it via `meta.yaml`'s `mode:` field in `cluster/snapshot.go`
- [ ] 1.2 Define the frozen on-disk layout constants and the image-manifest schema (referenced image refs + closure digests) as a shared struct/file
- [ ] 1.3 Define the pull-cache pin record format and helper signatures (pin closure, release, list-union) as stubs in a new `cluster/pullcachepin.go`
- [ ] 1.4 Define the two-phase save seam: a `reduceSnapshot(dir)` background-phase entry point (stub) invoked after resume
- [ ] 1.5 Add guest-side helper signatures for the logical extract (sqlite backup, storage tar, cert copy, image enumeration) as stubs

## 2. Layer discard & rootfs re-sparsify (independent)

- [ ] 2.1 Add a containerd config template enabling `discard_unpacked_layers=true` and wire it into cluster start in `cluster/cluster.go`
- [ ] 2.2 Implement a re-sparsify pass over a snapshot rootfs clone (hole-punch zeroed blocks), reusing `transfer.go` `punchHole`/SEEK machinery, in a new file
- [ ] 2.3 Make re-sparsify idempotent and safe to skip when unsupported; log bytes reclaimed
- [ ] 2.4 Unit-test the re-sparsify against a fixture file with known zero ranges

## 3. Pull-cache pin & retention (independent)

- [ ] 3.1 Implement durable pin records under the pull-cache (per-snapshot pin file keyed by snapshot id)
- [ ] 3.2 Compute the live set as the union of all pins; make `prune`/retention evict only unpinned, expired blobs (`cluster/pullcache.go`)
- [ ] 3.3 Make `clear` warn/skip pinned blobs unless forced
- [ ] 3.4 Release a snapshot's pin on `snapshot delete`
- [ ] 3.5 Unit-test: pinned digests survive prune; deleting a snapshot frees its pins

## 4. Frozen save (logical extract)

- [ ] 4.1 Implement the guest-side extract: sqlite online backup of `state.db`, tar of `/var/lib/rancher/k3s/storage`, k3s TLS/token copy
- [ ] 4.2 Enumerate referenced images and build the image-digest closure manifest
- [ ] 4.3 Assemble the frozen snapshot dir (datastore + PVC data + certs + manifest) and write `meta.yaml` with `mode: frozen`
- [ ] 4.4 Ensure the freeze window stays minimal (crash-consistent extract; no long pause) per the two-phase contract
- [ ] 4.5 Verify the invariant in code: refuse to write a frozen snapshot missing the storage tar

## 5. Frozen restore (thaw)

- [ ] 5.1 Implement thaw: provision a fresh cluster, restore `state.db` + PVC data + certs, boot cold-equivalent
- [ ] 5.2 Trigger image rehydration from the pull-cache mirror and wait for readiness
- [ ] 5.3 Apply the existing CIDR compatibility check and kubeconfig re-merge
- [ ] 5.4 Fail clearly when a referenced digest is absent from the pull-cache (no silent partial start)

## 6. Frozen export / import

- [ ] 6.1 Export fat: bundle datastore + PVC data + certs + manifest + pinned blob closure (loose files from the pull-cache), zstd archive
- [ ] 6.2 Export thin (`--thin`): bundle datastore + PVC data + manifest only
- [ ] 6.3 Import fat: seed the target pull-cache with missing blobs (content-addressed), then thaw
- [ ] 6.4 Import thin: thaw, re-pulling referenced images from the target's registries
- [ ] 6.5 Round-trip test: export on one cache state, import into a cache missing the blobs, confirm seed + thaw

## 7. CLI wiring

- [ ] 7.1 Add `--cold` / `--frozen` to `k3c snapshot save`; default warm where suspend is supported (`cmd/snapshot.go`)
- [ ] 7.2 Make `restore` auto-detect the tier from `meta.yaml`
- [ ] 7.3 Show the tier in `snapshot list`; add `--thin` to `snapshot export`
- [ ] 7.4 Help text and error messages reflect the tier trade-offs (frozen = small, minutes to thaw)

## 8. Two-phase orchestration

- [ ] 8.1 Wire `SnapshotSave` to run capture/clone in the freeze window and dispatch `reduceSnapshot` detached after resume
- [ ] 8.2 In `reduceSnapshot`, commit the pin durably first, then run re-sparsify
- [ ] 8.3 Make the background phase idempotent and re-runnable; guard against a snapshot being restored concurrently

## 9. Tests, docs, spec sync

- [ ] 9.1 Integration test: frozen save + thaw round-trip preserves a stateful workload's data
- [ ] 9.2 Update README / ARCHITECTURE.md snapshot section with the tier model and trade-offs
- [ ] 9.3 `go build ./...` and `go test ./...` green; `openspec validate snapshot-tiers-frozen` passes
- [ ] 9.4 Conventional-commit the change on `feat/snapshot-tiers-frozen`
