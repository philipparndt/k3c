## Context

The TUI diagram (`tui/tui.go`, `diagramScreen`) already proves out the model and
the data sources. The web front-end reuses the same read-only accessors:
`cluster.Clusters`, `cluster.DockerSidecarInfo`, `cluster.DaemonsState`,
`cluster.PullCacheStats`, and `cluster.Traffic(cfg, name)`. Commands are wired as
Cobra subcommands under `cmd/`, resolving config via `loadConfigDefault(args)`
(see `cmd/ui.go` for the closest analogue — it just calls `tui.Run(cfg)`).

`cluster.Traffic` returns cumulative byte counters; a *rate* needs two samples
over time. The TUI computes this from its tick loop; the web server must keep
the previous sample itself.

## Goals / Non-Goals

**Goals:**
- `k3c web` starts a local server and opens the animated diagram, as an
  alternative front-end to `k3c ui`.
- Live updates via a polled JSON endpoint, no websocket complexity.
- Single static binary — assets embedded with `go:embed`.
- Bind to loopback by default; pick a free port when the requested one is busy.

**Non-Goals:**
- Lifecycle actions are limited to start / pause / stop (and resume as the
  paused-state form of start). No logs streaming, snapshots, or config editing
  in this change.
- No auth/TLS — it is a localhost dev tool bound to loopback.
- No websockets/SSE — polling is sufficient for a status diagram.

## Decisions

### Decision: New `web/` package, thin `cmd/web.go`
`cmd/web.go` resolves config and calls `web.Serve(cfg, opts)`; all HTTP logic
lives in `web/`. **Why:** mirrors how `cmd/ui.go` delegates to `tui`, keeps the
command layer thin, and makes the server unit-testable. **Alternative:** inline
in `cmd/` — rejected, the handler + state aggregation is more than a command
should hold.

### Decision: Polled `GET /api/state` JSON, not websockets
The page fetches `/api/state` on an interval (~2s) and re-renders. **Why:** a
status diagram changes slowly; polling is trivial, robust, and needs no
streaming infrastructure. The payload is small. **Alternative:** SSE/websocket —
rejected as unjustified complexity for v1.

### Decision: Server-side net-rate sampling
The server keeps the last `Traffic` sample per cluster (guarded by a mutex) and
computes the rate as a delta on each `/api/state` call, skipping the sample when
counters reset (a VM restart), exactly as the TUI does. **Why:** rate is a
property of two readings over time; the stateless client cannot compute it
reliably across polls and the server already holds the timing. **Alternative:**
have the client diff successive payloads — rejected, it breaks on missed polls
and tab backgrounding.

### Decision: Preact + Vite front-end, committed bundle
The page is a Preact app built by Vite to `web/dist`, embedded via `go:embed`.
Dependencies are kept to three (`preact`, `vite`, `@preact/preset-vite`); the
bundle has no runtime CDN. `web/dist` is committed (a gitignore exception) so
`go build ./...` and CI need no node step, while `make web` (a prerequisite of
`make build`) regenerates it for contributors. **Why:** the UI will grow
(actions, more views) and a component model beats hand-rolled DOM string
updates; committing the tiny content-hashed bundle keeps the Go build
self-contained, unlike the large runtime payload which is generated-only.
**Alternative:** a single static HTML file (zero deps) — rejected as it does not
scale; a build-tag + stub embed — rejected as it breaks `go run . web` in dev.

### Decision: Lifecycle actions execute the k3c binary
`POST /api/action` runs the `k3c` binary with the matching lifecycle args
(`cluster start|stop|pause|resume NAME`, `docker up|down|pause|resume`), exactly
as the TUI does via `os.Executable`. The handler validates the target against
the current machine list and the action against an allow-list, and rejects
non-POST. **Why:** reuses the CLI's config resolution and logging, keeps one
code path for lifecycle, and the validation prevents argument injection.
**Alternative:** call `cluster.Start/Stop/...` directly — rejected, it would
duplicate the command layer's config/log wiring and diverge from the CLI.

### Decision: Flow particles only on measured activity
Animated particles render on an edge only when a real signal says data is
flowing: live network traffic rate for egress edges, and a rise in pull-cache
hits+misses between polls for the pull edges (the active cluster's host edge
follows its traffic). Idle edges draw a dim static line. The front-end keeps
stable element keys so Preact reuses the SMIL nodes during continuous flow (no
per-poll jitter). **Why:** constant animation falsely implies constant traffic;
showing motion only when it is real makes the diagram trustworthy.
**Alternative:** always animate — rejected, it misleads.

### Decision: Embed assets with `go:embed`
The HTML/CSS/JS ship as one embedded file served at `/`. **Why:** preserves the
single-static-binary property; release builds already embed a runtime tree, so
embedding is established. **Alternative:** serve from disk — rejected, breaks the
portable binary.

### Decision: Auto-pick a free port, open the browser by default
If the requested port is busy, bind `:0` and report the chosen URL. Open the
browser via the platform opener (`open` on macOS) unless `--no-open`. **Why:**
zero-friction launch like other dev UIs. **Alternative:** fail on a busy port —
rejected, annoying for a convenience tool.

## Risks / Trade-offs

- **`Traffic`/`DaemonsState` latency on each poll** → `DaemonsState` dials
  listeners (already concurrent) and `Traffic` execs into the VM; a 2s poll is
  well within budget, but a very short interval could pile up. Mitigation: a
  sane default interval and a single in-flight handler per request (net/http
  already serializes per connection; the work is bounded).
- **Binding beyond loopback** → `--addr` could expose an unauthenticated UI on
  the LAN. Mitigation: default to `127.0.0.1`; document that `--addr` widens
  exposure and add no auth in this read-only v1.
- **Stale state if a poll fails** → a transient error shouldn't blank the
  diagram. Mitigation: the client keeps the last good state and shows a subtle
  "reconnecting" indicator on fetch failure.
