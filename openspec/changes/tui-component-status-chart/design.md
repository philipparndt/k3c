## Context

The TUI (`tui/tui.go`, Bubble Tea + lipgloss, alt-screen) currently renders a
machines tree, an info panel (context, net rate, cache hits), and a status
line. Its model already pulls live data every ~5s in `refresh()`
(`tui/tui.go:264`): `cluster.Clusters`, `cluster.DockerSidecarInfo`,
`cluster.Traffic`, and `cluster.PullCacheStats`.

The system k3c manages has five distinct runtime components:

1. **Host daemon process** + its listeners (CONNECT proxy `:3128`, SNI gateway
   `:443`, pull-cache `:5011`, registry forward, docker port-forward, egress
   forwards, admission webhook). Health: `pidAlive()` + `portOpen()` in
   `cluster/daemons.go`. Today only `DaemonsStatus()` exposes it — and it
   *prints* to stdout, returning nothing structured.
2. **Apple `container` runtime** — resolved binary; cluster/VM states come from
   `clusterStates()` parsing `container ls -a`.
3. **k3s cluster VM(s)** — `<name>-server` (+ optional `-registry`); state via
   `cluster.Clusters` → `ClusterInfo{Server, Registry, Context, Active, ...}`.
4. **Docker sidecar VM** — `cluster.DockerSidecarInfo` → `ClusterInfo{Kind:
   "docker"}`.
5. **Pull-cache daemon** — `cluster.PullCacheStats` → `PullStats{Hits, Misses,
   ...}` via HTTP `:5011/stats`.

The TUI already has a state-dot vocabulary in `stateDot()`/`dotChar()`
(`tui/tui.go:924`): running `●` green, paused `◐` yellow, suspended `◌` blue,
stopped `○` dim, unknown `·` dim.

The screen-toggle pattern is established: `View()` (`tui/tui.go:963`) switches
to `logScreen()` (`l`) or `helpScreen()` (`?`) when `showLog`/`showHelp` is set;
those screens are full-screen and dismiss with the same key or `esc`.

## Goals / Non-Goals

**Goals:**
- A separate, full-screen diagram view toggled by a single key, matching the
  existing help/log screen interaction model.
- Render components as labeled blocks connected by directional flow arrows —
  legible as a *data-flow diagram*, not a flat list.
- Each block shows a live status using the existing dot vocabulary/colors,
  refreshed by the existing tick so it stays current while open.
- Expose host-daemon listener state programmatically so the TUI reads it
  without shelling out.

**Non-Goals:**
- No interactivity inside the diagram (no selecting/acting on a node) in this
  change — it is read-only.
- No new CLI command; this is TUI-only. `k3c daemons status` output is
  unchanged.
- No graph auto-layout engine; the layout is hand-composed for k3c's fixed,
  known topology.
- No new diagnostics — statuses reuse what `status`/`doctor`/`daemons status`
  already determine.

## Decisions

### Decision: A separate toggled screen, not a main-screen panel
Add `showDiagram bool` to the model and a `case m.showDiagram:` branch in
`View()` returning a new `diagramScreen()`. Bind a key (proposed `D`) in
`Update()`'s key handling that toggles it, and make `esc`/`D` close it — mirror
how `showHelp`/`showLog` are handled so behavior is consistent (only one
overlay screen open at a time). **Why:** the user explicitly wants a second
screen, not clutter on the machines view; the toggle pattern already exists and
keeps the main view unchanged. **Alternative considered:** an always-visible
panel in `headerView()` — rejected, it crowds the main screen and the user
asked against it.

### Decision: Hand-composed lipgloss block layout
Build each component as a bordered lipgloss box (reuse `paneBox`/`panelBox`
styles) containing a title, a status dot+label, and key facts (ports, hit rate,
context). Compose rows with `lipgloss.JoinHorizontal` and connect them with
literal arrow glyphs (`→ ↓ ↑ ⇅`) rendered as dim text between boxes; center the
whole thing with the existing `m.center()` helper. **Why:** k3c's topology is
fixed and small (5 components in 3–4 layers) — a hand-tuned layout reads far
better than a generic graph renderer and needs no new dependency. lipgloss box
joining is already used throughout the file. **Alternative considered:** an
ASCII graph/DAG library — rejected as overkill for a fixed topology and a new
dependency.

### Decision: Layered data-flow orientation (host → runtime → guest)
Lay the diagram out top-to-bottom as the flow the user reasons about:
**host daemon + listeners** at the top, the **`container` runtime** in the
middle, and the **guest VMs (cluster, sidecar)** at the bottom, with the
**pull-cache/registry** branching off the egress/pull path. Arrows show
direction: pulls flow guest → cache → upstream; egress flows guest → proxy/SNI;
port-forwards flow host → sidecar. **Why:** matches ARCHITECTURE.md's mental
model and makes a down listener visibly break a flow path.

### Decision: Structured daemon-state accessor
Refactor `cluster/daemons.go` so the listener list (name, port, detail, and
`up`/`down` from `portOpen`) plus process state (`running`/`stopped`, pid,
spawned version) are produced by a new exported function (e.g.
`DaemonsState(cfg) DaemonsInfo`) that both `DaemonsStatus()` (for printing) and
the TUI consume. **Why:** `DaemonsStatus` currently only `fmt.Printf`s; the TUI
must not capture stdout or re-dial ports ad hoc. Single source of truth keeps
CLI and TUI consistent. **Alternative considered:** the TUI re-dials ports
itself — rejected, it duplicates the listener list and config logic and would
drift from `daemons status`.

### Decision: Reuse the existing refresh tick; fetch daemon state in `refresh()`
Add the daemon state to the `dataMsg` produced by `refresh()` (alongside
clusters/traffic/cache), so the diagram updates on the same ~5s cadence with no
new timer. **Why:** consistent freshness, no extra goroutines, and the data is
cheap (local PID check + TCP dials with a 1s timeout already used by
`portOpen`).

## Risks / Trade-offs

- **`portOpen` blocking the refresh** → each `portOpen` dials with a 1s timeout;
  several down listeners could add latency to a refresh. Mitigation: dial the
  listeners concurrently in the accessor, or accept that the existing
  `daemons status` already pays this cost serially and listeners are few.
- **Narrow terminals truncating the diagram** → a fixed multi-column block
  layout can overflow a small window. Mitigation: gate columns on `m.width`
  (stack vertically when narrow) and rely on the resize full-repaint already in
  place; show a "resize to view" hint below a minimum width.
- **Status vocabulary mismatch for listeners** → listeners are up/down, not the
  five VM states. Mitigation: map up→running-green, down→stopped/bad-red using
  the existing dot styles so the legend stays coherent.
- **Diagram drifts from real topology** if listeners/config change → the
  listener set is config-driven (egress ports, forwards, webhook, registry,
  pull-cache toggles). Mitigation: derive blocks from the same config-driven
  list the accessor builds, not a hardcoded set.
