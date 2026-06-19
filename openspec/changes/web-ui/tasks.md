## 1. Web package: state aggregation

- [x] 1.1 Create `web/` package with JSON state types (daemon, listeners, machines, cache, net) and a `Server` holding `cfg` plus a mutex-guarded map of last `sample` per machine.
- [x] 1.2 Implement `collectState()` aggregating `cluster.DaemonsState`, `cluster.Clusters`, `cluster.DockerSidecarInfo`, `cluster.PullCacheStats`, and per-machine traffic.
- [x] 1.3 Sample traffic for every running machine (clusters and the docker sidecar) via `cluster.MachineTraffic`, computing a per-machine rate (skip on counter reset / first sample) so each VM's own activity is visible — not just the active cluster.

## 2. Web package: HTTP server + actions

- [x] 2.1 Embed the built front-end (`//go:embed all:dist`) and serve it via a file server at `/`.
- [x] 2.2 Add read-only `GET /api/state` returning `collectState()` as JSON.
- [x] 2.3 Add `POST /api/action` (start/stop/pause/resume) that executes the `k3c` binary, validating the target machine and action against allow-lists and rejecting non-POST.
- [x] 2.4 Implement `Serve(cfg, opts)` binding loopback by default, falling back to a free port when busy, and opening the browser unless `--no-open`.

## 3. Front-end (Preact + Vite)

- [x] 3.1 Scaffold a minimal Vite + Preact project (`preact`, `vite`, `@preact/preset-vite`) with `vite.config.ts`, `index.html`, and `src/`.
- [x] 3.2 Components: status-colored nodes + frames, daemon listeners (down marked red, clean one-per-line list), pull-cache, per-VM nodes with ▶ ⏸ ⏹ action buttons, stats strip, legend.
- [x] 3.3 `Edges` overlay: structural + egress + pull edges; animated particles render **only** on edges with measured activity; stable keys keep SMIL stable across polls.
- [x] 3.4 Poll `GET /api/state`; derive flow activity from real signals (per-machine traffic for egress, cache delta for pulls) with a short hold window; keep last good state on a failed poll ("reconnecting").
- [x] 3.5 Dispatch ▶ ⏸ ⏹ to `POST /api/action` and refresh.
- [x] 3.6 Polish: no hover-lift; fixed-height, no-wrap stats so a long rate never shoves the diagram down.

## 4. Command wiring

- [x] 4.1 Add `cmd/web.go`: `k3c web` with `--port`, `--addr`, `--no-open`, resolving config via `loadConfigDefault` and calling `web.Serve`; registered on `rootCmd`.

## 5. Build integration

- [x] 5.1 `make web` builds the front-end into `web/dist` (installs node deps on first run; falls back to the committed bundle if npm is absent); wired as a prerequisite of `make build` and `make build-unbundled`, plus `make web-clean`.
- [x] 5.2 Commit `web/dist` (gitignore exception) so `go build ./...` works without a node step; guard it in goreleaser's pre-build hooks.

## 6. Verify

- [x] 6.1 `go build ./...`, `go vet ./...`, and `npm run build` pass.
- [x] 6.2 Unit-test the `actionArgs` mapping (allowed lifecycle actions only; unknown actions rejected).
- [x] 6.3 Run `k3c web --no-open`, curl `/` + assets + `/api/state` (per-machine rates present), and confirm `/api/action` rejects unknown machines/actions (400) and non-POST (405).
