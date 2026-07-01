## Context

The TUI (`k3c ui`, in `tui/tui.go`) styles everything from a block of
package-level `lipgloss` vars evaluated once at init (`tui/tui.go:1228-1244`).
The accent is a purple `AdaptiveColor{Light: "#5A56E0", Dark: "#7D79F6"}` used
for the title, selection bar, and all three rounded borders (`panelBox`,
`paneBox`, `dialogBox`). Sibling roles are `dim`, `good`, `warn`, `cool`, `bad`.

The top header (`headerView`, `tui/tui.go:1459`) wraps the info fields in
`panelBox` (a `RoundedBorder` with padding) rendered beside the key menu. The
border consumes a top and bottom line plus left/right columns and forces an
alignment offset on the menu.

The machine list comes from `cluster.Clusters()` (`cluster/info.go:59`), which
sorts strictly by name. `ClusterInfo` (`cluster/info.go:15`) has no timestamp
field; only `SnapshotInfo` has `Created`. The runtime is queried via
`container ls -a` / `... --format json`.

Config flows `FileConfig` → resolved `Config` in `config/config.go`; there is no
UI or theme section today. `k3c config view` prints the effective config.

## Goals / Non-Goals

**Goals:**
- Ship a light-blue (`#89D7FB`) default accent in place of the purple.
- Render the top info panel without a frame to reclaim space.
- Let the user order the machine list by name (default) or by creation time,
  toggled at runtime.
- Let the color palette (accent + per-role label colors) be overridden from the
  config file, falling back to built-in defaults when unset.

**Non-Goals:**
- Full user-defined layouts, multiple named themes, or per-widget style
  overrides beyond the documented color roles.
- Runtime theme editing from inside the TUI (config-file only).
- Changing snapshot ordering semantics (snapshots keep their current order).

## Decisions

### 1. Default accent → light blue, keeping adaptive contrast
Set the default accent to a light-blue family: `AdaptiveColor{Light: "#0E86C7",
Dark: "#89D7FB"}`. The requested `#89D7FB` is the dark-terminal value (where the
purple lived visually); the light-mode companion is a darker blue so text and
borders stay readable on white. Config can override either way.
_Alternative — use `#89D7FB` for both modes:_ rejected because it is low-contrast
on light backgrounds; the adaptive pair preserves readability while honoring the
"light blue" intent.

### 2. Colors sourced from a config-driven theme struct
Replace the loose package vars with a single `theme` value (a struct holding the
colors and the derived `lipgloss.Style`s), constructed once from the resolved
config at `k3c ui` startup via `newTheme(cfg)` and stored in a package-level
`var theme` (the TUI is one instance per process). Call sites change mechanically
(`accent` → `theme.accent`, `dimSt` → `theme.dimSt`, …). Each color role reads
its config override if non-empty, else the built-in default.
_Alternative — thread a `*theme` through every render method:_ cleaner in theory
but touches far more signatures for no functional gain in a single-instance TUI.
_Alternative — mutate individual package vars in place:_ rejected; a struct keeps
construction in one place and makes the config→style mapping explicit.

### 3. Frameless info panel
In `headerView`, render `m.infoPanelView()` directly instead of
`panelBox.Render(...)`, and drop the now-unneeded one-line alignment offset on
the key menu so the two columns top-align. The compact/small-screen header
(`compactHeaderView`) already renders the panel frameless, so this brings the
normal layout in line with it. `panelBox` may be removed if it has no other use.
_Alternative — a borderless bordered style with padding:_ unnecessary; plain
`JoinHorizontal` of the two columns is simplest.

### 4. Sort snapshots (not machines) in the TUI
The machine order stays stable (name order, as `cluster.Clusters()` returns it).
The sort mode reorders each machine's snapshots only. The TUI holds a `sortMode`
(`name` | `date`) and, in `rebuildRows`, orders a copy of each machine's
snapshots (`m.snapsByMachine[name]`) via `sortedSnaps`. `SnapshotInfo.Created`
is already available (an RFC3339 string); date order is newest-first with name
as the tiebreaker, and snapshots with an unparseable/empty date sort last so the
mode degrades to name order.
_Alternative — sort the machine list itself:_ rejected on user feedback;
reordering machines is disruptive, and the useful grouping is by machine with its
snapshots ordered underneath. This also removes the need for a cluster creation
timestamp (no `ClusterInfo.Created`, no extra runtime JSON query).

### 5. Sort toggle keybinding + indicator
Bind `o` (order) in the main list handler to cycle `name → date → name`; show the
active mode on the "Machines" pane title (e.g. `Machines   snapshots by name` /
`by date`) and add the toggle to the help/key menu. The cursor follows the
previously selected row across a re-sort — the same snapshot when one was
selected, otherwise its machine.

### 6. Config schema: `ui.theme`
Add to `FileConfig`:
```yaml
ui:
  theme:
    accent: "#89D7FB"   # main/accent color
    dim:    ""          # muted text / separators
    good:   ""          # ok / running
    warn:   ""          # warning / paused
    cool:   ""          # secondary accent (keys, suspended)
    bad:    ""          # error / stopped
```
Every field optional; empty string means "use the built-in default". Values are
passed to `lipgloss.Color`, which accepts hex (`#RRGGBB`) or ANSI index strings,
so no strict validation is required beyond non-empty. Resolve into `Config` as a
`UITheme` struct of strings and include it in `config view` output.

## Risks / Trade-offs

- [A snapshot's `Created` may be empty or not parse as RFC3339] → Date sort
  treats it as unknown and orders it last, degrading to name order; no crash.
- [User supplies an invalid color string] → `lipgloss.Color` renders it as-is or
  as no color; acceptable and non-fatal. Optionally note invalid-looking values.
- [Light-mode accent differs from the literal `#89D7FB`] → documented decision;
  users on light terminals get better contrast and can override in config.

## Migration Plan

Purely additive and backward compatible: existing configs gain default theme
values, existing behavior (name sort) is the default, and the only visible change
without config is the new accent color and the frameless header. No data
migration or rollback steps beyond reverting the build.

## Open Questions

- None outstanding.
