## Why

The TUI's `D` system diagram is constrained by the terminal: monospace blocks,
box-drawing connectors, and no motion. The same data — daemon listeners, the
container runtime, the cluster and sidecar VMs, the pull-cache, and live traffic
— reads far better in a browser, where the data flow can actually animate, nodes
are status-colored and clickable, and stats update live. `k3c web` offers that
as an alternative front-end to `k3c ui`, backed by the exact same status
functions.

## What Changes

- Add a new `k3c web` command that starts a local HTTP server and (by default)
  opens the system diagram in the browser. `--port` selects the listen port
  (default chosen automatically when busy), `--addr` overrides the bind address
  (default loopback), and `--no-open` skips launching the browser.
- Render the k3c components as an animated data-flow diagram: status-colored
  nodes and frames (running green, paused yellow, suspended blue, stopped gray),
  a red marker for a down listener, a live net/cache stats strip, and flow
  particles along an edge **only when that edge is actually carrying data**
  (measured from live traffic and pull-cache activity) so the animation never
  implies flow that isn't happening.
- Let the user **start, pause, and stop** a machine from the UI via a
  `POST /api/action` endpoint that executes the matching `k3c` lifecycle command
  (validating the machine and action so it cannot become an arbitrary command).
- Expose a read-only `GET /api/state` JSON endpoint that aggregates the live
  system state from the existing `cluster` functions (`Clusters`,
  `DockerSidecarInfo`, `DaemonsState`, `PullCacheStats`, `Traffic`), with
  server-side net-rate sampling. The page polls it on an interval.
- Build the front-end with a minimal Preact + Vite setup (3 dev-deps:
  `preact`, `vite`, `@preact/preset-vite`). Vite outputs to `web/dist/`, which
  is embedded in the binary (`go:embed`) and committed so `go build` needs no
  node step. `make web` (wired into `make build`) regenerates it.

## Capabilities

### New Capabilities
- `web-ui`: A local web front-end (`k3c web`) that serves a live, animated
  data-flow diagram of the k3c system, backed by a JSON state endpoint sourced
  from the existing status functions, plus start/pause/stop lifecycle actions.

### Modified Capabilities
<!-- No requirement changes to existing capabilities. The web server reuses the
     existing read-only status accessors and the existing lifecycle commands
     (via the k3c binary) without altering them. -->

## Impact

- **Code**: new `cmd/web.go` (Cobra command) and a new `web/` package — the Go
  HTTP server (state aggregation + server-side net-rate sampling, the action
  endpoint, embedded assets) plus a Preact + Vite front-end (`web/src`,
  `web/dist`). Reuses `cluster.DaemonsState` and the existing
  `cluster.Clusters` / `DockerSidecarInfo` / `PullCacheStats` / `Traffic`
  accessors; lifecycle actions shell out to the `k3c` binary like the TUI does.
- **Dependencies**: Go side uses only the standard library. Front-end adds 3
  node dev-deps (`preact`, `vite`, `@preact/preset-vite`); the built bundle has
  no runtime CDN and is embedded.
- **Build**: `make web` builds the front-end into `web/dist` and is a
  prerequisite of `make build` / `make build-unbundled`; `web/dist` is committed
  (gitignore exception) and guarded in goreleaser's pre-build hooks.
- **Behavior**: `GET /api/state` is read-only; `POST /api/action` mutates only
  via validated `k3c` lifecycle commands against known machines.
