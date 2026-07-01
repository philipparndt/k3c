## Why

The default TUI accent is a purple (`#5A56E0`/`#7D79F6`) that reads poorly for
some users, the bordered info panel at the top wastes vertical and horizontal
space, and each machine's snapshots can only be ordered by name. Users also
can't adapt the palette to their own terminal. A lighter, higher-contrast
default plus a compacter header, a snapshot date sort, and configurable colors
make `k3c ui` easier to read and personalize.

## What Changes

- Change the default TUI accent color from purple to light blue (`#89D7FB`).
- Remove the frame (rounded border) around the info panel at the top of the UI
  so the header occupies less space; the info fields render frameless.
- Add a sort toggle for each machine's snapshots: by name (current behavior, the
  default) or by date (creation time, newest first). The machine order stays
  stable. The active sort mode is indicated in the UI and cycled with a
  keybinding.
- Add a `ui`/theme section to the k3c config so the color theme can be
  overridden: the main/accent color plus the individual label colors
  (dim, good, warn, cool, bad). Unset colors fall back to the built-in defaults.

## Capabilities

### New Capabilities
<!-- None: both affected specs already exist. -->

### Modified Capabilities
- `terminal-ui`: new default accent color, frameless top info panel, and a
  name/date sort toggle for each machine's snapshots (machine order stays
  stable).
- `configuration`: a new `ui.theme` config section that overrides the TUI color
  palette (accent plus per-role label colors).

## Impact

- `tui/tui.go`: theme color definitions (accent → light blue), `headerView`
  (drop `panelBox` border), snapshot ordering within each machine + a sort-mode
  toggle keybinding and indicator, and reading theme colors from resolved config
  instead of hard-coded package vars.
- `config/config.go` (`FileConfig`, `Config`, defaults, `config view`): new
  `ui.theme` fields resolved into the effective config.
- No changes to cluster lifecycle, networking, or other commands.
