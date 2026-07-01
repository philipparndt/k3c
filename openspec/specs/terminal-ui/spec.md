# terminal-ui Specification

## Purpose

Provide an interactive terminal UI for managing clusters, snapshots, and
lifecycle operations without remembering individual commands. This capability
owns `k3c ui`.
## Requirements
### Requirement: Interactive lifecycle management

`k3c ui` SHALL open an interactive terminal UI for viewing clusters and
snapshots and driving lifecycle operations (create, start, stop, delete, and
related transitions).

#### Scenario: Launch the UI

- **WHEN** the user runs `k3c ui`
- **THEN** an interactive terminal UI opens listing clusters and snapshots and
  offering lifecycle actions

### Requirement: Live traffic display

The UI SHALL display live traffic information, showing both the current rate and
the total traffic.

#### Scenario: Observe traffic

- **WHEN** traffic flows while the UI is open
- **THEN** the UI shows the current rate alongside the cumulative total

### Requirement: Responsive layout for small terminals

The UI SHALL adapt its presentation to the terminal size so that it remains
legible and fully navigable at any size. When the terminal is below the size
needed for the normal layout, the UI SHALL switch to a compact presentation
that (a) lays out header information vertically rather than side-by-side, (b)
truncates over-long lines with an ellipsis so each row occupies exactly one
screen line and never wraps, and (c) makes the cluster/snapshot list scrollable
so the selected row is always visible and every row is reachable. All
keybindings and actions available in the normal layout SHALL remain available
in the compact presentation.

#### Scenario: Compact view on a narrow terminal

- **WHEN** the terminal width is below the threshold required by the normal
  layout
- **THEN** the UI renders the header fields stacked vertically and truncates any
  line wider than the terminal with an ellipsis, so no row wraps onto a second
  line

#### Scenario: Scrolling a list taller than the terminal

- **WHEN** the cluster/snapshot list has more rows than fit in the available
  height and the user moves the selection toward a row that is off-screen
- **THEN** the list scrolls to keep the selected row visible, and every row
  remains reachable via selection movement

#### Scenario: Actions preserved in compact view

- **WHEN** the UI is in the compact presentation
- **THEN** the same keybindings and lifecycle actions available in the normal
  layout continue to work

#### Scenario: Dialogs fit a narrow terminal

- **WHEN** a dialog (e.g. a confirmation prompt or the keybinding help) would be
  wider than the terminal
- **THEN** the dialog box shrinks to fit the terminal width, wrapping long text
  lines, and the keybinding help packs its sections into as many columns as fit
  the width (down to one) rather than overflowing

#### Scenario: Scrolling a dialog taller than the terminal

- **WHEN** the keybinding help or the system diagram is open on a terminal too
  short to show all of it
- **THEN** the dialog drops its frame to reclaim space and scrolls with the
  cursor/scroll keys, and only its close keys (`?`/`esc` for help, `D`/`esc` for
  the diagram) close it — scroll keys do not

#### Scenario: Scrolling a diagram wider than the terminal

- **WHEN** the system diagram is wider than the terminal
- **THEN** it scrolls horizontally (←→) as well as vertically so the clipped
  edges remain reachable

### Requirement: Readable default color theme

The UI SHALL use a light-blue accent color as its default theme so it remains
legible across terminals. The accent color SHALL be used consistently for the
title, the selected-row highlight, and interface borders.

#### Scenario: Default accent is light blue

- **WHEN** the user launches `k3c ui` with no theme configured
- **THEN** the title, selected row, and borders are rendered in the light-blue
  accent color rather than the previous purple

### Requirement: Frameless top info panel

The UI SHALL render the top info panel (the k3c/machine/context/net/total/cache
fields) without a surrounding frame in the normal layout so the header occupies
less vertical and horizontal space, while remaining aligned with the key menu
beside it.

#### Scenario: Header without a frame

- **WHEN** the UI is shown in its normal (non-compact) layout
- **THEN** the info fields at the top are displayed with no border box around
  them, and the shortcut menu remains aligned beside them

### Requirement: Sort each machine's snapshots by name or date

The UI SHALL let the user order the snapshots listed under each machine either
by name (the default) or by creation date, and SHALL provide a keybinding to
switch between the two orderings. The machine order itself SHALL remain stable
(unaffected by the sort mode). The active sort mode SHALL be indicated in the
UI, and the current selection SHALL be preserved across a re-sort. When date
order is selected and a snapshot has no known creation date, that snapshot SHALL
be ordered after those that do.

#### Scenario: Default ordering is by name

- **WHEN** the UI opens
- **THEN** each machine's snapshots are listed sorted by name

#### Scenario: Switch to date ordering

- **WHEN** the user presses the sort-toggle key
- **THEN** each machine's snapshots reorder by creation date (newest first), the
  machine order is unchanged, the UI indicates the date sort mode is active, and
  the previously selected row remains selected

#### Scenario: Switch back to name ordering

- **WHEN** the user presses the sort-toggle key again
- **THEN** the snapshots return to name ordering and the UI indicates the name
  sort mode is active

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

