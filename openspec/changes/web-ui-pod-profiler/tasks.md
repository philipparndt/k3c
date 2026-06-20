## 1. Profiler in-process stream

- [x] 1.1 Add `ProfileStream(ctx, cfg, interval, names) (<-chan Snapshot, error)` in `cluster/profile.go` that runs the existing node sampler and sends `Snapshot` structs on a channel, closing it when the context ends or the node stream stops
- [x] 1.2 Refactor `Profile` to be a thin JSON-encoding wrapper over `ProfileStream` so the CLI and web share one sampling implementation (no change to CLI output)
- [x] 1.3 Add a unit test that `ProfileStream` emits snapshots and closes the channel on context cancel (parse-line/encode logic kept covered)

## 2. Shared per-cluster stream manager (web)

- [x] 2.1 Add a `streamManager` to `web/web.go` keyed by cluster name: starts one `ProfileStream` on first subscriber, fans ticks out to subscriber channels, and cancels the profiler when the last subscriber leaves
- [x] 2.2 Ensure the profiler context is also cancelled on server shutdown and verify the node `sh` loop dies on cancel (reuse `Profile`'s kill-on-cancel path)
- [x] 2.3 Resolve the target cluster from a query parameter, defaulting to the active cluster; request name resolution (`names=true`) for the web stream

## 3. Pod HTTP endpoints (web)

- [x] 3.1 Add `GET /api/pods` returning the current pod list (UID + `namespace/name`) for the target cluster, read-only; empty list when the cluster is not running
- [x] 3.2 Add `GET /api/pods/stream` (SSE, `text/event-stream`) emitting one event per tick with timestamp and per-pod `cpu_usec`/`mem_ws`/`mem_current`, subscribing through the stream manager
- [x] 3.3 Register both handlers in `Serve`'s mux alongside the existing `/api/state` and `/api/action`
- [x] 3.4 Add a `web_test.go` test asserting both endpoints are read-only and the stream emits events / cleans up on disconnect

## 4. Front-end data layer

- [x] 4.1 Add pod sample/snapshot types to `web/src/types.ts` (pod UID, name, cpu_usec, mem_ws, mem_current; snapshot timestamp)
- [x] 4.2 Add an `EventSource` client that subscribes to `/api/pods/stream`, maintains a bounded per-pod ring buffer of recent ticks, and reconnects on drop
- [x] 4.3 Derive per-pod CPU rate from the `cpu_usec` delta over the tick interval, skipping intervals where the cumulative counter decreased (restart)

## 5. Front-end pods view

- [x] 5.1 Add a `Sparkline` component (inline SVG/canvas polyline) and render CPU-rate and memory sparklines per pod
- [x] 5.2 Add a `Heatmap` component (pods × ticks grid, cells colored by value→intensity scale) and render CPU and memory heatmaps for the recent window, with a row cap and sort-by-recent-intensity; each pod row is sized in proportion to its share of the cluster's total computed CPU (CPU heatmap) / total memory working set (memory heatmap)
- [x] 5.3 Add a pods panel listing pods with their sparklines, reachable from a cluster node in the existing diagram, keeping the diagram available
- [x] 5.4 Show a "no pods available" state when the selected cluster is not running, without erroring

## 6. Build, docs, verification

- [x] 6.1 Rebuild and commit `web/dist` (`npm --prefix web run build`) so `go build` embeds the new front-end
- [x] 6.2 Manually verify against a running cluster: pod list, live sparklines, CPU/memory heatmaps, and that the profiler stops when all tabs close
- [x] 6.3 Run `go test ./...` and `npm --prefix web run build`; update any web-ui docs/README mentions of the new pods view
