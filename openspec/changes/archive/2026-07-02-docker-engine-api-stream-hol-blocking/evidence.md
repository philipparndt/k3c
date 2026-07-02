# Evidence & derivation

How the root cause was localized, by **elimination** — each step ruled out a
layer and narrowed the next. Every result below was produced this session against
the live k3c docker sidecar (engine 29.6.1) on macOS/Apple `container`. The Go
reproducers live in `vehub-budget-management-go/integration_tests/k3c_*_diagnosis_test.go`
(build tag `integration`, `t.Skip` unless the named `K3C_*_DEBUG` env is set).

Common env for every run: `DOCKER_HOST=unix:///Users/<u>/.config/k3c/docker.sock`,
`TESTCONTAINERS_RYUK_DISABLED=true`.

## TL;DR

The published Engine socket (`~/.config/k3c/docker.sock`) head-of-line-blocks: a
single **open-but-undrained streaming response** stalls the *next* Engine API
request. Localized by elimination to **k3c's own `docker.sock` path** — proven
because the identical sequence is HOL-free against the sidecar's Apple loopback
publish (`tcp://127.0.0.1:2375`). dockerd, the Apple `-p` port publish, plain
concurrency, the `/logs` endpoint, log multiplexing, exec, and HTTP checks are
all **ruled out**.

---

## Step 0 — Baseline: is it redpanda, or k3c networking? → both fine

`docker run redpandadata/redpanda:v24.2.7 redpanda start --mode dev-container --smp 1 --memory 1G`,
ports published with `-P`, queried from the macOS host:

| Check | Result |
|---|---|
| Time to log `Successfully started Redpanda!` | **~10 s** |
| Host → admin API `:9644 /` | `http=404 time=0.004s` |
| Host → Kafka `:9092` TCP | connect succeeded |
| Host → Schema Registry `:8081 /subjects` | `http=200` |

**Ruled out:** redpanda being slow; k3c port-mapping being broken; the container
being unhealthy. The stack is healthy in ~10 s and fully reachable from the host.
Yet `testcontainers redpanda.Run` times out at its 60 s `wait.ForAll` readiness.

## Step 1 — Which wait strategy fails? → only `ForLog`

`TestK3CWaitStrategyDiagnosis` (`K3C_WAIT_DEBUG=1`): start redpanda directly
(`ForMappedPort` only — proven to pass), let it boot, then run each strategy
the module's `wait.ForAll` uses, individually:

| Strategy | Result |
|---|---|
| `ForMappedPort(9092)` | ✅ 0.0 s |
| `ForListeningPort(9092).SkipInternalCheck` (external dial) | ✅ 0.0 s |
| `ForListeningPort(9092)` full (external dial **+ internal `/bin/sh` exec check**) | ✅ 0.1 s |
| `ForExec([true])` | ✅ 0.2 s |
| `ForHTTP("/").WithPort(9644)` expecting 404 | ✅ 0.1 s |
| `ForLog("Successfully started Redpanda!")` | ❌ **fail, 20.1 s timeout** |

**Ruled out:** mapped-port dials, the container-`exec` path (both `ForExec` and
`ForListeningPort`'s internal check run `/bin/sh` in the container via the Engine
exec API — they pass), and the admin HTTP check. **Isolated to the logs path.**

## Step 2 — Which part of the logs path? → not `/logs`; the follow-up `Inspect`

`ctr.Logs()` in testcontainers-go does: `ContainerLogs()` → **`Inspect()`** →
return an (undrained) demux reader. `TestK3CLogsDiagnosis` (`K3C_LOGS_DEBUG=1`)
splits it:

| Probe | Result |
|---|---|
| A — raw `ContainerLogs` (full body) + `io.ReadAll` | ✅ 0.0 s — **137 858 bytes, marker present** |
| B — raw `ContainerLogs` `Tail=10` | ✅ 0.0 s — 1 632 bytes, marker present |
| C — `ctr.Logs()` (the testcontainers path) | ❌ **15 s timeout, failing at `GET /containers/{id}/json`** (the internal `Inspect`) |
| D — raw `ContainerLogs` on a tiny `alpine echo` container | ✅ 0.0 s; and that container's own `ForLog` wait **succeeded** |

**Ruled out:** the `/logs` endpoint itself (raw reads work — full 137 KB *and*
tailed), the stdcopy multiplexing/demux, and log size. Also ruled out "ForLog is
fundamentally broken": on a tiny, instantly-drained log (D) `ForLog` works. The
failure in C is precisely the **`Inspect` issued while the logs stream is open and
undrained** — not the logs read.

Supporting fact (moby client v0.4.0, `defaultHTTPClient`): default `http.Transport`
(no `MaxConnsPerHost` limit), `MaxIdleConns=6`. So the follow-up `Inspect` is free
to open a **new** connection — it is not queued behind the logs connection by the
client. The stall is below the client.

## Step 3 — What property of the stream blocks? → back-pressure, not concurrency

`TestK3CConcurrencyDiagnosis` (`K3C_CONC_DEBUG=1`), raw moby client:

| Experiment | Result | Isolates |
|---|---|---|
| E1 — open `follow=false` logs, **don't drain**, then `Inspect` | ❌ **12 s timeout** | the failing case |
| E2 — open logs, **drain + close**, then `Inspect` | ✅ 0.0 s | draining clears it |
| E3 — **20 concurrent** `Inspect` (no open stream) | ✅ 0.0 s | plain concurrency is fine |
| E4 — hold a **`follow=true`** stream open **and actively drain it**, then `Inspect` | ✅ 0.5 s | an *actively read* stream blocks nothing |

**Ruled out:** connection concurrency as such (E3: 20 parallel requests fine), and
long-lived streams as such (E4: a followed, drained stream is fine). **Confirmed:**
the trigger is a **back-pressured (open, unread) response body** — the reader isn't
consuming, TCP back-pressure builds on that connection, and the *next* request
stalls (E1). Draining removes it (E2).

## Step 4 — Which transport layer? → k3c's `docker.sock`, not dockerd/Apple `-p`

k3c publishes dockerd two ways (`cluster/docker.go`): the `docker.sock` forwarder
(`startDockerSocket` → `dialDockerEngine` → `dialSidecarPort`, i.e. the in-guest
forwarder over the single `--publish-socket` bridge, with loopback TCP as
fallback) **and** a direct Apple port publish `-p 127.0.0.1:<DockerPort>:2375`.
Re-running E1–E4 with `DOCKER_HOST=tcp://127.0.0.1:2375` (straight to dockerd,
bypassing k3c's `docker.sock`):

| Transport | E1 | E2 | E3 | E4 |
|---|---|---|---|---|
| `unix://…/k3c/docker.sock` (k3c forwarder → `--publish-socket` bridge / `splice`) | ❌ hang | ✅ | ✅ | ✅ |
| `tcp://127.0.0.1:2375` (Apple `-p` publish → dockerd) | ✅ | ✅ | ✅ | ✅ |

(`docker -H tcp://127.0.0.1:2375 version` → server 29.6.1, confirming the loopback
endpoint is the same engine.)

**Ruled out:** dockerd (handles the exact sequence correctly) and Apple's per-port
`-p` publish. **Localized:** the HOL is introduced by k3c's own `docker.sock`
engine path — the `--publish-socket` guest bridge (`dialSidecarPort`) and/or the
host↔guest `splice` (`cluster/daemons.go`), where a connection whose inbound bytes
are not being read stalls other connections sharing that transport.

---

## Consolidated: what is ruled out

| Hypothesis | Verdict | Killed by |
|---|---|---|
| Redpanda too slow to start under k3c | ❌ ruled out | Step 0 (ready ~10 s) |
| k3c port-mapping / host reachability broken | ❌ ruled out | Step 0 (all ports reachable) |
| Mapped-port dial waits fail | ❌ ruled out | Step 1 (ForMappedPort/ForListeningPort pass) |
| Container `exec` API broken | ❌ ruled out | Step 1 (ForExec + internal check pass) |
| Admin HTTP check fails | ❌ ruled out | Step 1 (ForHTTP passes) |
| `GET /logs` endpoint broken | ❌ ruled out | Step 2 A/B (raw reads work, incl. 137 KB) |
| stdcopy log multiplexing / demux broken | ❌ ruled out | Step 2 A/B (raw multiplexed reads work) |
| Log body too large | ❌ ruled out | Step 2 B (tail) & Step 3 |
| `ForLog` fundamentally unsupported | ❌ ruled out | Step 2 D (tiny-log ForLog works) |
| Client connection-pool limit serializes requests | ❌ ruled out | moby client transport has no per-host cap; Step 3 E3 |
| Plain request concurrency limited | ❌ ruled out | Step 3 E3 (20 parallel pass) |
| Any long-lived stream blocks | ❌ ruled out | Step 3 E4 (drained follow stream fine) |
| dockerd mishandles the sequence | ❌ ruled out | Step 4 (loopback TCP passes) |
| Apple `-p` port publish mishandles it | ❌ ruled out | Step 4 (loopback TCP passes) |
| **k3c `docker.sock` path HOL-blocks a back-pressured stream** | ✅ **confirmed** | Steps 3 + 4 |

## What remains open (→ Spike S1)

The confirmed layer is k3c's `docker.sock` path, but two sub-questions decide the
fix (see design.md D2 and tasks.md §1):

1. **Where within it:** Apple's `--publish-socket` bridge (multiplexing all
   connections over one transport with HOL) vs. k3c's `splice` / `dialSidecarPort`.
   Test: reproduce E1 while checking whether a second `dialSidecarPort` connection
   even reaches `dockerfwd.handle` in the guest while the first stream is stalled.
2. **Whether the loopback publish carries hijacked streams** (`docker exec`/`attach`
   interactive) and `docker cp` archive — the stated reason `dialDockerEngine`
   prefers the bridge. This decides remediation A (prefer loopback) vs. B (route
   hijacked vs. non-hijacked) vs. C (repair the bridge).

## Reproducers

```bash
cd vehub-budget-management-go
export DOCKER_HOST=unix://$HOME/.config/k3c/docker.sock TESTCONTAINERS_RYUK_DISABLED=true
K3C_WAIT_DEBUG=1  go test -tags integration -run TestK3CWaitStrategyDiagnosis  ./integration_tests/ -v
K3C_LOGS_DEBUG=1  go test -tags integration -run TestK3CLogsDiagnosis          ./integration_tests/ -v
K3C_CONC_DEBUG=1  go test -tags integration -run TestK3CConcurrencyDiagnosis   ./integration_tests/ -v
# Step-4 isolation: same concurrency test against the loopback publish
K3C_CONC_DEBUG=1 DOCKER_HOST=tcp://127.0.0.1:2375 \
                  go test -tags integration -run TestK3CConcurrencyDiagnosis   ./integration_tests/ -v
```
