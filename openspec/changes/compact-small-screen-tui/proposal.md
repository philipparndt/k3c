## Why

On small terminals the TUI's fixed-width, side-by-side layout overflows: the
header panel and key menu collide, snapshot rows wrap mid-value (the timestamp
column spills onto the next line), and long machine lists run off the bottom
with no way to scroll. The frame becomes unreadable and some rows are
unreachable. The UI should stay legible and fully navigable at any terminal
size.

## What Changes

- Detect when the terminal is too small for the normal layout (below width
  and/or height thresholds) and switch to a **compact view**.
- In compact view, lay out the header information vertically (stacked) instead
  of side-by-side so it never overflows horizontally.
- Truncate over-long lines with an ellipsis instead of letting them wrap, so
  every row occupies exactly one screen line.
- Make the machine/snapshot list **scrollable** so the selected row is always
  brought into view and every row is reachable regardless of height.
- Keep the same keybindings and actions available in compact view; only the
  presentation changes.

## Capabilities

### New Capabilities
<!-- none -->

### Modified Capabilities
- `terminal-ui`: Add a responsive requirement — the UI adapts to small terminal
  sizes with a compact, non-wrapping, scrollable presentation while preserving
  all actions.

## Impact

- Code: `tui/tui.go` (the `View`/`headerView`/`treeView` render path, the
  `tea.WindowSizeMsg` handler, and row truncation/scroll bookkeeping in the
  model). Likely a new vertical-offset field on the model and a viewport-style
  clamp for the list.
- No CLI surface, config, or networking changes. No new dependencies
  (lipgloss/bubbles already vendored).
