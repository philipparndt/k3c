## 1. Truncation helper

- [x] 1.1 Add a width-aware `truncate(s string, w int) string` helper that uses `lipgloss.Width` and appends `…` when clipping (handles styled/ANSI content); add a unit test covering plain, styled, and already-short strings.

## 2. Compact-mode detection

- [x] 2.1 Add `compactWidth` (and any height) constant(s) sized just above the wide header's natural width, and a `model.compact() bool` predicate.
- [x] 2.2 Branch `View()` to call `compactView()` when `m.compact()`, leaving the existing wide path unchanged above the threshold.

## 3. Compact header

- [x] 3.1 Render the info panel and key actions stacked vertically (not joined horizontally) in compact mode.
- [x] 3.2 Truncate every header line to the terminal width so the header never wraps.

## 4. Scrollable, non-wrapping list

- [x] 4.1 Add `listTop int` to the model and compute `visible` rows from `m.height` minus header/status/border heights (clamp ≥1).
- [x] 4.2 Clamp `listTop` so the selected row (`m.cur`) is always within `[listTop, listTop+visible)`; re-clamp on selection move and on `tea.WindowSizeMsg`.
- [x] 4.3 Render only `m.rows[listTop:listTop+visible]`, truncating each row to width so no row wraps.
- [x] 4.4 Show `↑ more` / `↓ more` indicators when rows are hidden above/below.

## 5. Compact dialogs

- [x] 5.1 Add a `fitDialog(content, preferred)` helper that sizes the dialog box to the preferred width but caps it to the terminal, letting lipgloss wrap long lines; route the confirm/input/rename/export dialogs through it.
- [x] 5.2 Pack the help dialog's sections into as many columns as fit the width (`packColumns`), make it scrollable via a viewport, drop its frame on a small screen, and route scroll keys to the viewport so only `?`/`esc` close it.
- [x] 5.3 Make the system diagram scrollable via a viewport (content rebuilt live each frame, offset persisted), with horizontal scrolling enabled (`SetHorizontalStep`) so a diagram wider than the terminal stays reachable, drop its frame on a small screen, and route scroll keys to the viewport so only `D`/`esc` close it.

## 6. Tests & verification

- [x] 6.1 Add render tests at small sizes asserting (a) no output line exceeds `m.width`, (b) total output height ≤ `m.height`, (c) the selected row appears for selection at first / middle / last row, and (d) a long confirm prompt wraps and the help dialog fits a narrow terminal.
- [x] 6.2 Run `gofmt`, `go build ./...`, and `go test ./tui/...`; manually run `k3c ui` and shrink the terminal below the threshold to confirm the compact, scrollable view and the wrapped dialogs.
