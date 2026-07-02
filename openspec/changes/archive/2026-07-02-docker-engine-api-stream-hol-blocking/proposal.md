## Why

The host-side Docker Engine socket that k3c publishes for the sidecar
(`~/.config/k3c/docker.sock`) **head-of-line-blocks**: while one connection holds
an open-but-undrained streaming response body (e.g. `GET /containers/{id}/logs`
with `follow=false`, opened but not yet read), a *second* concurrent Engine API
request stalls until it times out. Plain concurrency is fine; the trigger is a
single back-pressured (unread) stream.

This breaks common Docker tooling that pipelines requests around a streaming
call. Concretely, **Testcontainers' `wait.ForLog` strategy hangs**: its
`Container.Logs()` opens the log stream and then issues a `ContainerInspect`
*before the caller drains the stream* — the Inspect is the blocked second
request, so the wait times out at 60s. The Redpanda and Kafka Testcontainers
modules gate readiness on `ForLog`, so `k3c docker up`-backed CI cannot start a
Kafka/Schema-Registry container even though the container itself is healthy in
~10s and reachable on every mapped port.

The bug is **proven to live in k3c's own `docker.sock` path**, not in dockerd or
Apple's runtime (see the isolation table in design.md): the identical request
sequence succeeds against the sidecar's Apple-published loopback endpoint
(`tcp://127.0.0.1:<DockerPort>`) and hangs only through
`~/.config/k3c/docker.sock`.

## What Changes

- **Fix head-of-line blocking on the published Docker Engine socket** so a
  back-pressured or idle streaming connection can never stall other Engine API
  requests. After the fix, opening a log stream and issuing a concurrent
  `Inspect`/other request MUST both make progress.
- **Restore Testcontainers compatibility**: the Testcontainers Redpanda/Kafka
  modules (and any client that relies on `wait.ForLog`) MUST reach readiness
  through `k3c docker`'s engine socket.
- Add a **regression test** in k3c that reproduces the failing sequence
  (undrained `follow=false` logs stream + concurrent `Inspect`) over the
  published socket and asserts it no longer blocks.
- The exact remediation (route non-hijacked Engine traffic over the loopback
  endpoint vs. de-multiplex/repair the `--publish-socket` guest bridge) is a
  **design decision gated on one spike** — see design.md.

## Capabilities

### New Capabilities
<!-- none -->

### Modified Capabilities
- `docker-sidecar`: the "Expose the engine to shells and CI" requirement gains a
  guarantee that the published Engine API socket carries concurrent requests and
  streaming/back-pressured responses without head-of-line blocking, so standard
  Docker clients and Testcontainers work unchanged.

## Impact

- **Code** (all in `cluster/` + `dockerfwd/`): `cluster/dockerports.go`
  (`startDockerSocket`, `dialDockerEngine`, `dialSidecarPort`), `cluster/daemons.go`
  (`splice`), `dockerfwd/dockerfwd.go` (in-guest forwarder), and the
  `--publish-socket` / `-p 127.0.0.1:<DockerPort>:2375` wiring in
  `cluster/docker.go`.
- **Behavioural contract**: the change must preserve hijacked-stream support
  (`docker exec`/`attach` interactive) — the reason the engine currently prefers
  the `--publish-socket` bridge over the loopback publish. This constraint is why
  a naive "just use loopback" reorder needs the spike first.
- **Users**: unblocks Testcontainers-based CI (the veHub Go services'
  integration suites — e.g. `vehub-budget-management-go`'s Kafka harness) on
  k3c; no user-facing CLI change. Picked up on the next `k3c docker up` /
  sidecar recreate.
- **Related prior work**: extends the archived change
  `2026-06-29-docker-sidecar-host-forwarder` (which introduced this forwarder).
