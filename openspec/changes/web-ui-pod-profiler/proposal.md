## Why

The web UI (`k3c web`) shows the system topology — daemon, runtime, VMs, cache —
but stops at the cluster boundary: it cannot see inside a cluster to the pods
actually running there. k3c already has an exact, kernel-level per-pod CPU and
memory stream (`k3c profile`, the `pod-profiling` capability), but it is only
consumable as raw JSON lines on a terminal. Surfacing that stream visually in
the browser turns the web UI from a static diagram into a live workload monitor,
with no new measurement code required.

## What Changes

- Add a **pods view** to the web UI that lists the pods running on a cluster
  (namespace/name), shown when a cluster VM is selected/expanded.
- Stream the existing per-pod profiler samples to the browser over a new
  read-only HTTP endpoint (Server-Sent Events) so the page receives live ticks
  without polling a heavy command per request.
- Render a **per-pod sparkline** of recent CPU rate (derived from the cumulative
  `cpu_usec` delta between ticks) and memory working set, so each pod's recent
  trend is visible at a glance.
- Render two **heatmaps** (pods × time grid, cells colored by intensity) — one
  for CPU rate, one for memory working set — giving a cluster-wide view of which
  pods are hot over the recent window.
- The server SHALL run at most one profiler stream per cluster, shared across
  connected browsers, and stop it when no client is connected.
- All new endpoints are read-only and SHALL NOT mutate any cluster, pod, or
  daemon.

## Capabilities

### New Capabilities
- `web-ui-pods`: The web UI's in-cluster pod visibility — listing pods, streaming
  live per-pod CPU/memory samples to the browser, and rendering per-pod
  sparklines and CPU/memory heatmaps over a recent time window.

### Modified Capabilities
<!-- The web-ui capability is still a pending (unarchived) change; the pod view
     is additive and self-contained, so it is introduced as a new capability
     rather than a delta to web-ui. -->

## Impact

- `web/web.go`: new `GET /api/pods` (current pod list) and `GET /api/pods/stream`
  (SSE of profiler ticks) handlers; a shared per-cluster profiler stream manager
  with reference-counted start/stop.
- `cluster/profile.go`: reuse `Profile`/`Snapshot`/`PodSample` as-is; possibly a
  small in-process variant that emits `Snapshot` values to a channel instead of
  encoding JSON to an `io.Writer` (so the web server consumes structs, not bytes).
- `web/src/`: new components (pods list, sparkline, heatmap) and an SSE client;
  additions to `types.ts` for the pod sample/snapshot shapes.
- No changes to the `pod-profiling` capability's CLI behavior or output.
- New runtime dependency only on the browser's native `EventSource`; no new Go
  dependencies.
