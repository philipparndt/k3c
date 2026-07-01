## ADDED Requirements

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
