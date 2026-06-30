## ADDED Requirements

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
  lines and stacking multi-column content vertically rather than overflowing
