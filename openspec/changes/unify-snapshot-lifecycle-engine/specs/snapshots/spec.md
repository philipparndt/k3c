## ADDED Requirements

### Requirement: Cluster and sidecar snapshots share one engine

Cluster snapshots and docker-sidecar snapshots SHALL be produced and restored by
a single shared engine parameterized by a per-target descriptor, so that common
behavior — snapshot tier handling (warm/cold/frozen), warm machine-state capture
and restore, slot preparation for `--replace`, and list/delete/rename — stays in
parity across both targets rather than being maintained as two implementations.
Target-specific behavior (the cluster's registry rootfs, k3s config, warm-restore
address reclaim and CIDR checks; the sidecar's image-store volume and
post-restore virtiofs-share repair) SHALL be expressed as descriptor fields and
restore hooks on the shared engine, not as a forked copy of it.

#### Scenario: A snapshot-engine fix reaches both targets

- **WHEN** a fix is made to shared snapshot behavior (e.g. how warm machine-state
  files are captured or restored)
- **THEN** it takes effect for both cluster and docker-sidecar snapshots without
  a second copy, because both run through the same engine

#### Scenario: Target-specific restore steps still run

- **WHEN** a cluster warm snapshot is restored whose address is now free
- **THEN** the cluster target's reclaim hook reassigns the snapshot-time address
  and the machine resumes warm

- **WHEN** a docker-sidecar snapshot is restored
- **THEN** the sidecar target's post-restore hook repairs the virtiofs share

#### Scenario: Existing snapshot behavior is preserved

- **WHEN** an existing snapshot (warm, cold, or frozen) taken before this change
  is restored
- **THEN** it restores with the same result as before, with no change to
  on-disk snapshot format, CLI surface, or tier semantics
