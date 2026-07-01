## Context

The TUI's `c`/`n` key (`tui/tui.go`, snapshot key path around line 1062) always
opens the create wizard (`nameInput`), regardless of whether the cursor is on a
machine or a snapshot row. The wizard submits `snapshot save <cluster> <name>
[--cold|--frozen]` (or `docker snapshot save …`) via `startOp`.

`cluster.SnapshotSave` refuses to overwrite: it errors `snapshot '<name>' already
exists` when the target directory is present (`cluster/snapshot.go:164`). So
"recreate" cannot be a plain re-save — the existing snapshot must be removed
first (`SnapshotDelete`, `cluster/snapshot.go:643`).

The TUI already has a reusable `confirm` dialog (`tui/tui.go:65`) that renders up
to three buttons — `Cancel`, an optional middle decline button (`noCmd`), and a
right affirmative button (`cmd`) that `destructive` paints red. Buttons run
`tea.Cmd`s; a cmd may emit a message (the delete flow already does
`func() tea.Msg { return askMsg{c: followUp} }`). The current snapshot row
carries its tier in `r.snapMode` ("warm"/"cold"/"frozen").

## Goals / Non-Goals

**Goals:**
- Offer New vs Recreate when `c` is pressed on a snapshot row, with New as the
  safe default and Recreate clearly marked destructive.
- Recreate rebuilds the snapshot in place: same name, same tier.
- Expose the same recreate as a `--replace` flag on the save commands so the CLI
  and TUI share one mechanism.

**Non-Goals:**
- Changing `c` behavior on machine rows (still opens the wizard directly).
- A crash-safe, zero-data-loss atomic replace (see the risk below).
- Recreating with a *different* tier or name (that is just "New snapshot").

## Decisions

### 1. Reuse the `confirm` dialog for the New/Recreate choice
On `c` over a snapshot row, open a `confirm` with three buttons:
`[Cancel] [New snapshot] [Recreate]`, where `noCmd` = "New snapshot" and
`cmd` = "Recreate" (`destructive: true`, so only Recreate is red). Default the
focus to the "New snapshot" button (the requested default) rather than the usual
Cancel. "New snapshot" must open the wizard, which is not a plain op, so its cmd
emits a new `openSnapshotWizardMsg{cluster, docker}` that the update loop turns
into the existing `nameInput` (identical to today's wizard).
_Alternative — a bespoke dialog type:_ rejected; the three-button `confirm`
already models "cancel + two actions" and keeps rendering/navigation consistent.

### 2. Recreate via a `--replace` flag on `save` (delete-then-save)
Add `--replace` to `k3c snapshot save` and `k3c docker snapshot save`. With it,
`SnapshotSave`/`DockerSnapshotSave` delete an existing same-named snapshot before
saving instead of erroring; without it, behavior is unchanged (still errors on a
name clash). The TUI recreate runs `snapshot save --replace <cluster> <name>`
plus the tier flag derived from the row's `snapMode` (warm→none, cold→`--cold`,
frozen→`--frozen`).
_Alternative — sequence two ops in the TUI (delete op, then save op on
completion):_ rejected; op-chaining in the model is more fragile, gives no CLI
benefit, and splits the status/streaming into two operations.

### 3. Tier comes from the existing snapshot
Recreate preserves the snapshot's current tier, read from `r.snapMode` on the
selected row, so "same settings" holds without asking again. Docker sidecar
snapshots only support warm/cold, matching the existing wizard constraint.

## Risks / Trade-offs

- [Delete-then-save loses the old snapshot if the new save fails] → Documented
  behavior of Recreate; the destructive button and red styling set expectations,
  and snapshots are local dev checkpoints. A safer save-to-temp-then-swap is
  possible but adds real complexity (warm/cold/frozen each stage differently);
  deferred unless data-loss reports arise.
- [Recreating a snapshot that is the active/pinned restore point] → Behaves like
  today's manual delete+save; the pull-cache pin is re-established by the new save
  as usual. No special handling.
- [User picks Recreate by mistake] → Cancel is present and focus defaults to the
  non-destructive "New snapshot"; Recreate requires deliberately moving to the
  red button.

## Migration Plan

Additive and backward compatible: `--replace` defaults off, so existing CLI and
scripted saves are unchanged; the TUI machine-row flow is unchanged. No data or
config migration.

## Open Questions

- None outstanding.
