# snapshots Specification

## Purpose

Save and restore cluster and sidecar state as named, restorable points using
instant APFS copy-on-write, and move snapshots between machines via portable
archives. This capability owns `k3c snapshot` (clusters) and `k3c docker
snapshot` (the sidecar's whole image store).
## Requirements
### Requirement: Save and restore cluster snapshots

`k3c snapshot save [CLUSTER] [NAME]` SHALL save a snapshot of cluster state
using APFS copy-on-write; NAME SHALL default to a timestamp, and the save SHALL
be warm by default (taken against a running cluster). `k3c snapshot restore
[CLUSTER] NAME` SHALL restore a snapshot and start the cluster. `list`,
`rename`, and `delete` SHALL manage saved snapshots; `rename [CLUSTER] OLD NEW`
SHALL rename a stored snapshot, carrying its pull-cache image pin to the new
name.

#### Scenario: Save a warm snapshot

- **WHEN** the user runs `k3c snapshot save` on a running cluster
- **THEN** a timestamp-named snapshot of the cluster state is saved without
  stopping it

#### Scenario: Restore a snapshot

- **WHEN** the user runs `k3c snapshot restore mysnap`
- **THEN** the cluster state is restored from `mysnap` and the cluster is
  started

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
archive (always restoring cold). `k3c snapshot import FILE [NAME]` SHALL import
an exported archive into an already-created cluster.

#### Scenario: Move a snapshot between machines

- **WHEN** the user runs `k3c snapshot export mysnap` on one machine and `k3c
  snapshot import mysnap.tar` on another (after creating the cluster)
- **THEN** the snapshot is packaged as a portable archive and imported on the
  second machine

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

