## ADDED Requirements

### Requirement: Recreate a snapshot from the list

When the cursor is on a snapshot row, pressing the create key (`c`) SHALL open a
dialog offering two actions — create a new snapshot (the safe default) or
recreate the selected snapshot — plus a cancel option. Choosing to create a new
snapshot SHALL open the normal snapshot-create wizard. Choosing to recreate SHALL
be presented as a destructive action and, when confirmed, SHALL delete the
selected snapshot and save a new one in its place using the same name and the
same tier (warm/cold/frozen) as the deleted one. When the cursor is on a machine
row, pressing the create key SHALL continue to open the create wizard directly
without the dialog.

#### Scenario: Choose to create a new snapshot from a snapshot row

- **WHEN** the cursor is on a snapshot row and the user presses `c` and chooses
  the new-snapshot action
- **THEN** the normal snapshot-create wizard opens

#### Scenario: Recreate the selected snapshot

- **WHEN** the cursor is on a snapshot row and the user presses `c` and confirms
  the recreate action
- **THEN** the selected snapshot is deleted and a new snapshot is saved in its
  place with the same name and the same tier

#### Scenario: Recreate is a guarded, non-default action

- **WHEN** the New/Recreate dialog is shown
- **THEN** the recreate action is presented as destructive and the new-snapshot
  action is the default, and cancelling makes no change

#### Scenario: Create key on a machine row is unchanged

- **WHEN** the cursor is on a machine row and the user presses `c`
- **THEN** the snapshot-create wizard opens directly, without the New/Recreate
  dialog
