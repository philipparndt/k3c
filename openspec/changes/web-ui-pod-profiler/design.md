## Context

`k3c web` (the `web-ui` change) serves an embedded Preact/Vite front-end and two
HTTP endpoints — `GET /api/state` (read-only aggregate) and `POST /api/action`
(allow-listed lifecycle) — backed by the read-only `cluster` accessors. It shows
the system topology but nothing inside a cluster.

Separately, the `pod-profiling` capability (`cluster/profile.go`) already streams
exact per-pod accounting: `Profile(ctx, cfg, interval, duration, names, io.Writer)`
spawns one long-lived `sh` loop on the node that reads cgroup v2 (`cpu.stat`
`usage_usec`, `memory.current`, `inactive_file`) and emits a `Snapshot{TimeMillis,
Pods map[uid]PodSample}` per tick. `cpu_usec` is cumulative; rate is a consumer-side
delta. Pod UID→`namespace/name` resolution is best-effort via `kubectl`.

This change wires the profiler into the web server and adds a browser pods view.
No new measurement code is needed — the work is transport (stream structs to the
browser) and presentation (sparklines, heatmaps).

## Goals / Non-Goals

**Goals:**
- See the pods of a cluster in the browser with live per-pod CPU/memory.
- Per-pod CPU-rate and memory sparklines, plus cluster CPU/memory heatmaps.
- Reuse `cluster.Profile` and its sampling exactly; no duplicated cgroup logic.
- One profiler per cluster shared across all connected browsers; stop it when
  nobody is watching. Strictly read-only, like `/api/state`.

**Non-Goals:**
- Changing `k3c profile`'s CLI behavior or JSON output.
- Pod logs, exec, describe, or any mutation of pods/cluster from the web UI.
- Historical persistence — the window is the recent in-memory stream only.
- Multi-cluster simultaneous streaming UI (one selected cluster at a time).

## Decisions

### Consume `Snapshot` structs in-process, not JSON bytes
`Profile` currently encodes JSON to an `io.Writer`. The web server wants structs,
not bytes to re-parse. Add a thin in-process variant — e.g. `ProfileStream(ctx,
cfg, interval, names) (<-chan Snapshot, error)` — that runs the same node sampler
and sends `Snapshot` values on a channel; refactor `Profile` to be a JSON-encoding
wrapper over it so the CLI and web share one implementation.
- Alternative: have the web server shell out to `k3c profile` and parse stdout.
  Rejected — re-encodes/re-parses JSON, adds a child process, and loses typed
  access; the in-process channel is simpler and already in the same binary.

### Server-Sent Events for the live stream
Use SSE (`GET /api/pods/stream`, `text/event-stream`) for tick delivery. It is a
one-way server→client stream, which is exactly the shape, works with the native
`EventSource` API, needs no new dependency, and reconnects automatically.
- Alternative: WebSocket — bidirectional, heavier, needs a library; unnecessary
  for one-way data. Alternative: client polling a `/api/pods/latest` — loses the
  steady tick cadence the sparklines/heatmaps rely on and re-runs work per poll.

### One shared profiler per cluster, reference-counted
A `streamManager` keyed by cluster name holds a running `ProfileStream` plus a fan
-out to subscriber channels and a subscriber count. The first subscriber starts
the profiler; the last to leave cancels its context (which kills the node sampler,
matching `Profile`'s existing cancel-to-stop contract). This bounds node load to
one sampler per cluster no matter how many tabs are open.
- Alternative: one profiler per connection. Rejected — N tabs spawn N node
  samplers and N `kubectl` name-resolution loops.

### Compute CPU rate and build sparklines/heatmaps client-side
The server streams cumulative `cpu_usec` and memory unchanged; the browser keeps a
bounded ring buffer per pod and derives CPU rate from the `cpu_usec` delta over the
tick interval, skipping intervals where the counter decreased (restart). Sparklines
are inline SVG/canvas polylines; heatmaps are a pods×ticks grid colored by a value→
color scale (shared with the diagram's vocabulary where sensible).
- Alternative: pre-compute rates server-side. Rejected — keeps the server a thin,
  read-only transport and matches how `k3c profile` consumers already derive rate.

### Enable name resolution for the web stream
The web stream SHALL request `names` so pods show `namespace/name`, not bare UIDs;
resolution is best-effort and falls back to the UID (as in the CLI).

## Risks / Trade-offs

- [SSE connection left open leaks a profiler] → reference-count subscribers and
  cancel the profiler context when the count hits zero; also stop on server
  shutdown. Verify the node `sh` loop dies when the context is cancelled (the
  existing `Profile` relies on the same kill-on-cancel path).
- [Many pods make heatmaps unreadable] → cap rendered rows, sort by recent
  intensity, and window the time axis; the list view remains the complete picture.
- [Per-tick `kubectl` resolution adds API load] → reuse `Profile`'s existing lazy,
  throttled (≤ one call / 2s) refresh; do not resolve every tick.
- [Cluster stops mid-stream] → the node sampler exits, the channel closes, the SSE
  handler ends the response cleanly; the UI shows "no pods available".
- [Read-only guarantee] → the pods endpoints only read (cgroups + `kubectl get`),
  never mutate; keep them out of the `POST /api/action` path entirely.

## Migration Plan

Additive only — no existing endpoint, CLI flag, or output changes. New endpoints
and front-end components ship behind the existing `k3c web` command. Rollback is
removing the endpoints/components; nothing persists and no schema changes. The
front-end `web/dist` must be rebuilt (`npm --prefix web run build`) and committed
so `go build` embeds the new assets, consistent with the current web build flow.

## Open Questions

- Sparkline/heatmap placement: inline under each cluster node vs. a dedicated
  pods panel that opens for the selected cluster. (Leaning: a panel, to leave the
  diagram uncluttered.)
- Default stream interval for the web UI (CLI default is 500ms) — 1s may be
  smoother for the browser; confirm during implementation.
- Heatmap row cap and sort default (e.g. top 30 by recent CPU).
