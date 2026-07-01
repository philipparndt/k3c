## 1. Snapshot save --replace (cluster + docker)

- [x] 1.1 Add a `replace bool` parameter to `cluster.SnapshotSave` (`cluster/snapshot.go`): when set and the target snapshot dir exists, `SnapshotDelete` it before saving instead of returning the "already exists" error
- [x] 1.2 Add the same `replace` handling to `cluster.DockerSnapshotSave` (`cluster/dockersnapshot.go`)
- [x] 1.3 Add a `--replace` flag to `k3c snapshot save` (`cmd/snapshot.go`) and thread it into `SnapshotSave`
- [x] 1.4 Add a `--replace` flag to `k3c docker snapshot save` (`cmd/docker.go`) and thread it into `DockerSnapshotSave`
- [x] 1.5 Update the save commands' help text to mention `--replace` (recreate a same-named snapshot)

## 2. TUI New/Recreate dialog

- [x] 2.1 Add an `openSnapshotWizardMsg{cluster string; docker bool}` message and handle it in the update loop by constructing the existing `nameInput` create wizard (extract the wizard-open logic so the key path and the message share it)
- [x] 2.2 In the `c`/`n` snapshot key path, when the cursor is on a snapshot row, open a `confirm` dialog with buttons `[Cancel] [New snapshot] [Recreate]`: `noCmd` emits `openSnapshotWizardMsg`, `cmd` runs the recreate op, `destructive: true`
- [x] 2.3 Default the dialog focus to the "New snapshot" button (non-destructive default) rather than Cancel
- [x] 2.4 Build the recreate op: `snapshot save --replace <cluster> <name>` (or `docker snapshot save --replace <name>`) with the tier flag from the row's `snapMode` (warm→none, cold→`--cold`, frozen→`--frozen`)
- [x] 2.5 Keep `c`/`n` on a machine row opening the wizard directly (no dialog)

## 3. Tests and verification

- [x] 3.1 Add a `cluster` test covering `SnapshotSave` with `replace` (overwrites an existing snapshot) and without (still errors on a name clash)
- [x] 3.2 Add/adjust a TUI test: `c` on a snapshot row opens the New/Recreate dialog (default focus on New, Recreate marked destructive); `c` on a machine row opens the wizard directly
- [x] 3.3 Run `go build ./...` and `go test ./...`; manually confirm the dialog, the recreate (same name + tier), and that machine-row `c` is unchanged
