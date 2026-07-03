## Context

k3c publishes a stable host-side Docker Engine socket at `dockerSocketPath(cfg)`
(`~/.config/k3c/docker.sock`). `startDockerSocket` (cluster/dockerports.go) accepts
each host connection and, per connection, calls `dialDockerEngine` then `splice`
(cluster/daemons.go). `dialDockerEngine` tries `dialSidecarPort(guestEnginePort=2375)`
**first** — a connection to the in-guest forwarder over the single
`--publish-socket`-bridged unix socket (`docker-fwd.sock` ↔ guest
`/run/k3c-docker-fwd.sock`, handled by `dockerfwd/dockerfwd.go`) — and falls back to
the Apple-published loopback `tcp://127.0.0.1:<DockerPort>` (`-p 127.0.0.1:<p>:2375`
in cluster/docker.go). The bridge is preferred because Apple's TCP publish is
believed to drop hijacked exec/attach streams.

### The bug (reproduced)

> The full step-by-step derivation — every probe run, its result, and what each
> ruled out — is in [`evidence.md`](./evidence.md). The tables below are the
> condensed conclusion.

A second Engine API request stalls while a first connection holds an
open-but-**undrained** streaming response body. Minimal repro against a plain
`redpanda start` container using the raw moby client (from
`vehub-budget-management-go/integration_tests`, tag `integration`):

| Experiment | Result |
|---|---|
| E1 — open `GET /logs` (follow=false), **don't drain**, then `Inspect` | ❌ hangs (12s deadline) |
| E2 — open logs, **drain + close**, then `Inspect` | ✅ instant |
| E3 — 20 concurrent `Inspect` (no open stream) | ✅ instant |
| E4 — hold a **follow** logs stream open **and actively drain it**, then `Inspect` | ✅ ~0.5s |

E3 shows plain concurrency is fine; E4 shows an *actively read* stream blocks
nothing; E2 shows draining clears it. Only E1 — a back-pressured, unread stream —
blocks the next request.

### Root cause is isolated to k3c's socket path (proven)

Running the identical E1–E4 sequence against the sidecar's Apple-published
loopback endpoint, bypassing k3c's `docker.sock` forwarder entirely:

| Transport | E1 | E2 | E3 | E4 |
|---|---|---|---|---|
| `unix://~/.config/k3c/docker.sock` (k3c forwarder → `dialSidecarPort` → `--publish-socket` bridge) | ❌ hang | ✅ | ✅ | ✅ |
| `tcp://127.0.0.1:<DockerPort>` (Apple `-p` publish, straight to dockerd) | ✅ | ✅ | ✅ | ✅ |

So dockerd and Apple's per-port `-p` publish handle a back-pressured stream +
concurrent request correctly. The head-of-line blocking is introduced by k3c's
`docker.sock` engine path — the `--publish-socket` bridge (`dialSidecarPort`)
and/or the host↔guest `splice`, where a single connection whose inbound bytes
are not being read stalls other connections that share the bridge transport.

### Why Testcontainers trips it

`Container.Logs()` in testcontainers-go does: `ContainerLogs()` (opens the stream)
→ **`Inspect()`** → returns the still-undrained reader to the caller. The Inspect
is the blocked second request, so `wait.ForLog` (used by the Redpanda and Kafka
modules for readiness) times out — even though raw `ContainerLogs` alone works.

## Goals / Non-Goals

**Goals:**
- The published Engine socket never head-of-line-blocks: a back-pressured or idle
  connection cannot stall other connections/requests.
- Testcontainers `wait.ForLog` (Redpanda/Kafka modules) reaches readiness through
  `k3c docker`'s socket.
- Preserve everything that works today: hijacked `docker exec`/`attach`
  interactive streams, `docker cp` (archive PUT), build, compose, k3d.

**Non-Goals:**
- Changing dockerd, the Apple runtime, or the loopback `-p` publish.
- Reworking nested published-port forwarding (`dockerfwd` for container ports) —
  only the **engine API** path is in scope.

## Decisions

### D1 — Root cause is k3c's `docker.sock` engine path (settled by the isolation table)
Not dockerd, not Apple `-p`. The fix lives in `cluster/dockerports.go` /
`dockerfwd/` / the `--publish-socket` usage.

### D2 — Remediation direction — **RESOLVED by Spike S1: Option B**

S1 (run this session against the live sidecar, engine 29.6.1) pinned both
sub-questions decisively:

- **Where it blocks (S1.2):** inside **Apple's `--publish-socket` bridge**, not
  k3c's code. E1 hangs even when the client talks *directly* to the host side of
  the bridge (`docker-fwd.sock`, writing the `dockerfwd` port header), bypassing
  `startDockerSocket`'s `splice` entirely. k3c's `splice` and the in-guest
  forwarder are already per-connection independent (one goroutine pair per
  connection), so they cannot cross-block; the mux HOL is in Apple's socket
  bridge. → **C is a no-op** and is discarded.
- **Loopback hijack (S1.3):** the loopback `-p 127.0.0.1:<DockerPort>` publish
  **drops hijacked exec/attach** — `echo PAYLOAD | docker -H tcp://127.0.0.1:2375
  exec -i <c> cat` returns *empty*, while the same command over the bridge socket
  echoes `PAYLOAD` correctly. Archive uploads (`docker cp`, `PUT
  /containers/{id}/archive`) **do** work over loopback (they are ordinary
  request-body uploads, not hijacks). → **A is ruled out** (it would break
  `docker exec -i`/`-it` and `attach`).

**Chosen: B — route by request type.** In `startDockerSocket`'s per-connection
handler, peek the first HTTP request head on the accepted host connection and
route the *whole* connection:

- **Hijacked/upgrade streams → the `--publish-socket` bridge** (`dialSidecarPort`,
  full-duplex, carries hijack). Detected by a `Connection: Upgrade` request
  header (moby sets `Connection: Upgrade` + `Upgrade: tcp` for hijacks) **or** a
  hijack path (`POST /exec/{id}/start`, `.../attach`, `.../attach/ws`). Belt and
  suspenders because both signals are cheap.
- **Everything else → the loopback endpoint** (`dockerEngineEndpoint`, proven
  HOL-free): inspect, non-follow and follow logs, archive PUT (`docker cp`),
  build, compose, wait, events, etc.

First-request routing is sound because moby dials a **dedicated, unpooled**
connection for every hijack (`setupHijackConn`) and the upgraded connection is
never reused; pooled keep-alive connections only ever carry non-hijack requests.
So a connection's first request type is its type for life. If the bridge dial
fails (a pre-`--publish-socket` sidecar), hijack routing falls back to loopback —
degraded (hijack dropped) but no worse than today's fallback branch.

This keeps interactive exec/attach on the full-duplex bridge while moving the
common request/response + streaming path (the HOL-prone one, incl.
Testcontainers' undrained-logs-then-Inspect) onto the HOL-free loopback.

## Risks / Trade-offs

- **[Regressing interactive exec/attach]** → S1 must verify hijacked streams over
  whichever transport the fix routes them through; regression coverage for
  `docker exec -it` / `attach` before/after.
- **[Fixing the symptom, not the layer]** → if the HOL is in Apple's
  `--publish-socket`, only A/B (route around it) actually help; C would be a
  no-op. S1 disambiguates before implementation.
- **[`docker cp` archive PUT under load]** → the Redpanda module also injects
  config via `PUT /containers/{id}/archive`; the fix path must carry archive
  uploads. Cover it in the regression test.

## Migration Plan

1. **Spike S1** (below) — pin the blocking layer and the loopback hijack
   capability; choose A/B/C.
2. Implement the chosen remediation in the forwarder.
3. Land the k3c regression test (E1 sequence over the published socket) + a
   Testcontainers-style `ForLog` smoke.
4. Ships in the normal binary; a running sidecar picks it up on `k3c docker up`
   (or recreate). No config or CLI change.

## Open Questions

- **S1 — RESOLVED.** The HOL is in Apple's `--publish-socket` bridge (E1 hangs
  even talking directly to `docker-fwd.sock`, bypassing k3c's `splice`); the
  loopback `-p` publish **drops hijacked exec/attach** but carries archive PUT.
  Both settled empirically this session — see D2. Remediation **B** chosen.
