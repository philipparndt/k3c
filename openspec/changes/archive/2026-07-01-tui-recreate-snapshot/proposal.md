## Why

In the TUI, pressing `c` on a snapshot row opens the "new snapshot" wizard just
like pressing it on a machine row — there is no quick way to refresh an existing
snapshot in place. Re-capturing a named checkpoint (e.g. a "golden" state)
currently means deleting the snapshot and recreating it by hand, retyping the
name and re-picking the tier. A one-step "recreate" makes that common flow safe
and fast.

## What Changes

- In the TUI, when the cursor is on a **snapshot** row and the user presses `c`,
  show a dialog offering two actions instead of jumping straight into the wizard:
  - **New snapshot** (default, safe) — opens the existing create wizard, as today.
  - **Recreate** (destructive, red) — deletes the selected snapshot and saves a
    new one in its place with the **same name and same tier** (warm/cold/frozen).
- On a **machine** row, `c` keeps its current behavior (opens the wizard directly).
- Add a `--replace` flag to `k3c snapshot save` and `k3c docker snapshot save`
  that recreates a same-named snapshot (delete the existing one, then save) so
  the recreate is a single operation and the capability is available from the CLI
  too. Without `--replace`, saving over an existing name still errors as it does
  today.

## Capabilities

### New Capabilities
<!-- None: both affected specs already exist. -->

### Modified Capabilities
- `terminal-ui`: pressing `c` on a snapshot row opens a New/Recreate choice
  dialog; the recreate action rebuilds the snapshot in place with its existing
  name and tier.
- `snapshots`: `snapshot save` / `docker snapshot save` gain `--replace` to
  recreate a same-named snapshot (delete-then-save), preserving the requested
  tier.

## Impact

- `tui/tui.go`: the `c`/`n` snapshot key path (open a `confirm`-style choice
  dialog on a snapshot row), a message to open the create wizard from a dialog
  button, and the recreate op (`snapshot save --replace` with the row's tier).
- `cmd/snapshot.go`, `cmd/docker.go`: new `--replace` flag on the save commands.
- `cluster/snapshot.go`, `cluster/dockersnapshot.go`: save honors `replace`
  (delete an existing same-named snapshot before saving) instead of erroring.
- No changes to restore, export/import, rename, or cluster lifecycle.
