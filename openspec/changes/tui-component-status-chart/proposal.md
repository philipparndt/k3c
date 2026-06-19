## Why

k3c is several moving parts stacked on top of each other — the host daemon
process and its listeners, the Apple `container` runtime, the k3s cluster VMs,
the docker sidecar, and the pull-cache — and when something is wrong (a port
isn't bound, the daemon died, the cluster is unreachable) the user has to drop
to the CLI (`k3c daemons status`, `k3c doctor`, `k3c status`) to find out which
layer broke. The TUI shows machines and traffic but gives no picture of *how the
pieces connect* or *which layer is unhealthy*. A data-flow diagram makes the
system legible at a glance and turns "something's broken" into "the SNI gateway
listener is down."

## What Changes

- Add a new full-screen **system diagram** view to the TUI, toggled by a
  keybinding (e.g. `D`) and dismissed with the same key or `esc`, in the same
  style as the existing help (`?`) and log (`l`) screens. It is a separate
  screen, not added to the main machines view.
- Render the system as a **data-flow diagram**: blocks for each component
  (host daemon + its listeners, container runtime, cluster VM(s), docker
  sidecar, pull-cache) connected by directional flow arrows (host → guest
  egress, pulls → cache, port forwards), each block annotated with a live
  status (up/down/running/paused/stopped/unknown) using the TUI's existing
  state-dot vocabulary and colors.
- Drive the diagram from the existing refresh cycle so statuses stay live, and
  add a programmatic accessor for the host-daemon listener state (today
  `DaemonsStatus` only prints to stdout) so the TUI can read it without shelling
  out.
- Document the keybinding in the in-app help screen.

## Capabilities

### New Capabilities
- `tui-system-diagram`: A toggleable full-screen TUI view that renders the k3c
  components and the data flow between them as a labeled block diagram, with a
  live per-component health status sourced from the existing status/diagnostic
  functions.

### Modified Capabilities
<!-- No spec-level requirement changes to existing capabilities. The new
     programmatic daemon-state accessor is an internal addition that does not
     change the documented behavior of `k3c daemons status`. -->

## Impact

- **Code**: `tui/tui.go` (new `showDiagram` screen + `diagramScreen()`
  renderer, keybinding, help entry, View() switch). A new structured accessor
  in `cluster/daemons.go` (refactor `DaemonsStatus` to build a
  `DaemonsState`/listener struct that both the printer and the TUI consume).
  Reuses existing `cluster.Clusters`, `cluster.DockerSidecarInfo`,
  `cluster.PullCacheStats` data already flowing into the model.
- **Dependencies**: none new — lipgloss box/border styling already in use.
- **Behavior**: additive and read-only; no change to lifecycle commands or
  existing screens.
