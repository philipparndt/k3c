## 1. Config: ui.theme section

- [x] 1.1 Add a `UI` struct with a `Theme` sub-struct (`accent`, `dim`, `good`, `warn`, `cool`, `bad`, all `string`, all `yaml`-tagged optional) to `FileConfig` in `config/config.go`
- [x] 1.2 Add a resolved `UITheme` struct and field to `Config`, and populate it from `FileConfig` during resolution (empty strings preserved as "unset")
- [x] 1.3 Include the effective `ui.theme` in `k3c config view` output
- [x] 1.4 Document the `ui.theme` keys in the example/reference config (wherever config keys are documented)

## 2. Theme plumbing in the TUI

- [x] 2.1 Introduce a `theme` struct in `tui/tui.go` holding the color roles and derived `lipgloss.Style`s, plus a `newTheme(cfg)` constructor that applies config overrides over the built-in defaults
- [x] 2.2 Change the default accent to the light-blue pair `AdaptiveColor{Light: "#0E86C7", Dark: "#89D7FB"}`
- [x] 2.3 Replace the package-level color/style vars with the `theme` struct; construct it once from resolved config at `k3c ui` startup and update all call sites (`accent` → `theme.accent`, `dimSt` → `theme.dimSt`, etc.)
- [x] 2.4 `go build ./...` and `go vet ./...` clean (catches any missed references)

## 3. Frameless top info panel

- [x] 3.1 In `headerView` (`tui/tui.go`), render `infoPanelView()` without the `panelBox` frame and drop the key-menu alignment offset so the columns top-align
- [x] 3.2 Remove `panelBox` if it is no longer referenced
- [x] 3.3 Verify the compact/small-screen header still renders correctly
- [x] 3.4 Fix snapshot column misalignment: pad the snapshot-name column to the longest visible name (`snapNameWidth`) instead of a fixed width, so mode/size/date stay aligned when a name is longer

## 4. Snapshot name/date sort (machine order stays stable)

- [x] 4.1 Add a `sortMode` (name | date) to the TUI model, defaulting to name
- [x] 4.2 Add `sortedSnaps(machine)` returning a sorted copy of a machine's snapshots: by name (existing) or newest-first by parsed RFC3339 `Created` with name as tiebreaker and unknown dates last; use it in `rebuildRows` (machine order untouched)
- [x] 4.3 Bind `o` to cycle the sort mode; preserve the selected row (same snapshot, else its machine) across a re-sort
- [x] 4.4 Show the active sort mode on the "Machines" pane title (e.g. `Machines   snapshots by name` / `by date`) and add the toggle to the help/key menu

## 5. Specs, tests, and verification

- [ ] 5.1 Update `openspec/specs/terminal-ui/spec.md` and `openspec/specs/configuration/spec.md` at archive time (via the OpenSpec archive flow) to reflect the new requirements
- [x] 5.2 Add/adjust TUI render tests (in `tui/*_test.go`) covering the frameless header and the sort-mode indicator
- [x] 5.3 Add a config test covering `ui.theme` resolution and fallback to defaults
- [x] 5.4 Run `go test ./...` (all pass) and confirm `k3c config view` surfaces the theme; the light-blue theme, frameless header, and `o` sort toggle are covered by render tests (interactive `k3c ui` needs the Apple container runtime + a live terminal)
