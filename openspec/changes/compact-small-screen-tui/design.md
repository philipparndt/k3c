## Context

The main TUI screen (`tui/tui.go`, `model.View`) joins three blocks vertically:
`headerView` (a bordered info panel and the key menu joined **horizontally**),
`treeView` (the machine/snapshot list), and `statusView`. Rows are formatted
with fixed-width `fmt.Sprintf` columns (e.g. `%-24s` snapshot name, a `%9s`
size, a full RFC3339 timestamp). None of these blocks are width- or
height-aware:

- `headerView` places the panel beside the menu, so on a narrow terminal the
  two columns overrun the width and the terminal wraps them.
- `renderRow` emits a single fixed-width line; when that line is wider than the
  terminal, the terminal wraps it (the screenshot shows the timestamp spilling
  onto the next line). Selected rows are `padRight`-padded to `m.width-2` but
  never truncated.
- `treeView` renders **every** row; with more machines/snapshots than fit, rows
  run off the bottom and are effectively unreachable visually even though the
  cursor can still move to them.

The codebase already uses `bubbles/viewport` for the log dialog and clamps the
selection index in several places, so the building blocks exist. `lipgloss`
provides `Width`, `MaxWidth`, and truncation helpers.

## Goals / Non-Goals

**Goals:**
- Never wrap a row: at any width every rendered line fits on one screen line.
- Stack the header vertically when width is constrained so it never overflows.
- Scroll the list so the selected row is always visible and all rows reachable.
- Preserve all existing keybindings/actions; only presentation changes.

**Non-Goals:**
- No redesign of the normal (wide) layout — it renders unchanged above the
  threshold.
- No horizontal scrolling of rows (we truncate instead).
- No configurability of thresholds (hard-coded constants are fine).
- Dialog/overlay screens (help, log, confirm, input) already center within
  `m.width/m.height`; they are out of scope beyond not regressing.

## Decisions

### Decision: A width/height threshold selects a compact branch in `View`

`View` gains a `m.compact()` predicate (e.g. `m.width < compactWidth`). Below
it, `View` calls a `compactView()` that builds the same three logical blocks
but with the compact header and a scrolled, truncated list. Above it, the
existing path is unchanged.

- **Why a threshold rather than always-compact?** The wide layout is
  information-dense and preferred when there's room; only degrade when forced.
- **Alternative considered:** make every block fully fluid (one layout for all
  sizes). Rejected — larger diff, risks regressing the well-tuned wide layout,
  and the side-by-side header is genuinely nicer when it fits.
- Threshold is driven by the wide header's natural width (panel + menu). Pick a
  constant a little above that so we switch before the header collides.

### Decision: Truncate to width with a shared helper, applied to every emitted line

Introduce a single helper (e.g. `truncate(s string, w int) string`) using
`lipgloss.Width`-aware truncation with a trailing `…`, and apply it to each line
the compact path emits (header rows and list rows). Selected-row highlighting
still pads to the available width, but the pad target is `min(content, w)` and
the result is truncated so the highlight bar never exceeds the screen.

- **Why a shared helper?** Truncation must be ANSI/二-width aware (rows contain
  lipgloss styling); doing it once avoids miscounting escape codes.

### Decision: List scrolling via a top-offset clamped to the selection

Add `listTop int` to the model: the index of the first visible row. After any
selection move (and on resize), clamp so `listTop <= m.cur < listTop + visible`,
where `visible` is the height left after header + status + borders. Render only
`m.rows[listTop : listTop+visible]`.

- **Why a manual offset rather than `bubbles/viewport`?** The list is built from
  styled strings per frame and the selection already drives a clamped index;
  a top-offset is a few lines and avoids reconciling viewport content on every
  data refresh. The log dialog's viewport stays as-is.
- A scroll indicator (e.g. `↑ more` / `↓ more`) MAY be shown when rows are
  hidden above/below, so the user knows the list continues.

### Decision: Compute available height from the rendered header/status

`visible` = `m.height - lipgloss.Height(header) - lipgloss.Height(status) -
borders`. Clamp to ≥1. This keeps the math correct as the compact header's
height changes with content.

## Risks / Trade-offs

- [Truncation hides data, e.g. the snapshot timestamp tail] → Truncate the
  least-essential column first where practical (timestamp/size), keep name and
  mode visible; the wide layout still shows everything when there's room.
- [Off-by-one in the scroll clamp leaves the selection just off-screen] → Cover
  with a render test (selection at first/last row, mid-list) asserting the
  selected row appears in the output and output line count ≤ height.
- [Threshold flapping near the boundary on resize] → Single comparison, no
  hysteresis needed; `tea.ClearScreen` already fires on resize.
- [Existing render tests assume the wide layout] → Compact path only triggers
  below the threshold; wide-size tests are unaffected.

## Migration Plan

Pure additive rendering change in one file; no data, config, or CLI migration.
Rollback is reverting the commit. Validate by running `k3c ui` and shrinking the
terminal below the threshold, plus the new render tests at small sizes.

## Open Questions

- Exact threshold values (width, and whether a height threshold is also needed)
  — to be pinned during implementation against the wide header's measured width.
- Whether to also drop the list's border in compact mode to reclaim two columns
  — decide when measuring real widths.
