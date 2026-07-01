## ADDED Requirements

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
