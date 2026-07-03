## 1. Daemon state accessor

- [x] 1.1 In `cluster/daemons.go`, add exported types for daemon state — e.g. `DaemonsInfo{State string; Pid string; Spawned string; Listeners []ListenerState}` and `ListenerState{Name, Port, Detail string; Up bool}`.
- [x] 1.2 Add `DaemonsState(cfg *config.Config) DaemonsInfo` that builds the process state (via `pidAlive`) and the config-driven listener list (proxy, sni-gateway, egress, forwards, webhook, registry, pull-cache) with `portOpen` for each — the same set `DaemonsStatus` prints.
- [x] 1.3 Refactor `DaemonsStatus` to call `DaemonsState` and only format/print it, so CLI output is byte-for-byte unchanged.
- [x] 1.4 (Optional, per design risk) dial listeners concurrently in `DaemonsState` to keep refresh latency low.

## 2. Feed daemon state into the TUI model

- [x] 2.1 In `tui/tui.go`, add a `daemons cluster.DaemonsInfo` field to `model` and a `daemons *cluster.DaemonsInfo` field to `dataMsg`.
- [x] 2.2 In `refresh()` (`tui/tui.go:264`), call `cluster.DaemonsState(cfg)` and attach it to the emitted `dataMsg`.
- [x] 2.3 In the `dataMsg` case of `Update()`, store the daemon state on the model.

## 3. Diagram screen state & keybinding

- [x] 3.1 Add `showDiagram bool` to `model`.
- [x] 3.2 In `Update()` key handling, bind a key (`D`) to toggle `showDiagram`; ensure opening it closes other overlays and `esc`/`D` closes it — mirror the `showHelp`/`showLog` handling so only one overlay is open at a time.
- [x] 3.3 Add a `case m.showDiagram:` branch to `View()` (`tui/tui.go:963`) returning `m.diagramScreen()`.

## 4. Render the data-flow diagram

- [x] 4.1 Implement `diagramScreen()` building one bordered lipgloss block per component: host daemon (+ listeners from `m.daemons`), `container` runtime, each cluster VM (from `m.clusters`), docker sidecar (from the `Kind=="docker"` entry), and pull-cache (from `m.cacheLine`/cache stats).
- [x] 4.2 Render each block's status with the existing dot vocabulary/colors (`stateDot`/`dotChar`, `tui/tui.go:924`); map listener up→running-green, down→bad-red.
- [x] 4.3 Compose the blocks into a top-to-bottom data-flow layout (host → runtime → guest VMs, pull-cache branching off) using `lipgloss.JoinHorizontal`/`JoinVertical`, with dim directional arrow glyphs (`→ ↓ ↑ ⇅`) between blocks; center with `m.center()`.
- [x] 4.4 Add a title and a footer hint (`D or esc to close`), and a small legend mapping dots to states.
- [x] 4.5 Gate column layout on `m.width`: stack blocks vertically when narrow, and show a "resize to view" hint below a minimum width.

## 5. Discoverability & polish

- [x] 5.1 Add the diagram keybinding to `helpScreen()`'s GENERAL column (`tui/tui.go:1246`).
- [x] 5.2 Verify diagram statuses update live on the refresh tick while open, and that resize repaints the diagram cleanly.

## 6. Verify

- [x] 6.1 `go build ./...` and `go vet ./...` pass.
- [x] 6.2 Manually run the TUI: open the diagram, confirm all present components render with correct statuses; stop a listener / pause a VM and confirm the diagram reflects it on the next refresh.
- [x] 6.3 Confirm `k3c daemons status` output is unchanged after the refactor.
