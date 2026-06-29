# Findings — external Testcontainers validation (vehub-feedbackapi-go)

> **Note, not a spec delta.** A real downstream consumer exercised this change's
> contract end-to-end. Recorded here as evidence: the engine-socket goal is met;
> two host-reachability gaps remain (nested published ports + the reaper).
> **Date**: 2026-06-29 · **Against**: `k3c-local` dev build (this branch) + running sidecar.

## Setup

`vehub-feedbackapi-go`'s integration test (`./integration_tests`, build tag
`integration`) starts the fleet-standard stack via Testcontainers-go v0.38.0:
`confluentinc/confluent-local:7.6.2` (Kafka) + `confluentinc/cp-schema-registry:7.6.2`
on a custom docker network, then runs the real consumer pipeline.

Env used:
```
DOCKER_HOST=unix:///Users/<user>/.config/k3c/docker.sock
TESTCONTAINERS_RYUK_DISABLED=false   # (k3c docker env default)
```

## What works ✅ (engine socket — this change's primary goal)

- Testcontainers connected to the engine over the **host unix socket**:
  `Resolved Docker Host: unix://…/.config/k3c/docker.sock`, `Server Version: 29.6.1`.
- With the reaper disabled, a **custom docker network was created**, both images
  pulled, the **Kafka container reached ready**, and **Schema Registry started
  inside the container**: `Started NetworkTrafficServerConnector …0.0.0.0:8081`,
  `Server started, listening for requests…`.
- ⇒ The "back the host engine socket with a host-local endpoint" goal is
  effective: container create/start/network operations succeed via the socket.

## Gap 1 — nested published ports not reachable from the host 🔴

Testcontainers' host-side readiness probe for Schema Registry
(`wait.ForHTTP("/subjects").WithPort("8081/tcp")`) hits the **host-mapped port**
and times out, even though SR is healthy *inside* the container:

```
start schema registry: start container: started hook:
wait until ready: context deadline exceeded            (2-min startup timeout)
```

This is exactly the change's second concern — nested published ports must reach
the host without depending on guest L2. The engine socket works; the
runtime-chosen **published-port forwarding to the host does not** for these
Testcontainers-mapped ports. Until this is closed, any host process that talks to
a nested container's mapped port (the SR HTTP probe here; and subsequently the
Kafka/SR clients from the test) cannot complete.

## Gap 2 — Ryuk reaper container fails to start 🟠

With `TESTCONTAINERS_RYUK_DISABLED=false`, starting `testcontainers/ryuk:0.12.0`
fails immediately:

```
create network: reaper: new reaper: run container: started hook:
wait until ready: external check: … address: localhost:32768:
unexpected container status "removing": could not start container:
failed to create network
```

The container goes to `removing` right after start — "failed to create network".
A **user-defined** network for the test's own containers creates fine (Gap 1 got
past network creation), so this is **reaper-specific** (its container/network mode
or host-port bind). Workaround for consumers today: `TESTCONTAINERS_RYUK_DISABLED=true`
(the consumer's test cleans up its containers via `t.Cleanup`).

## Repro

```
cd ~/dev/k3c && K3C_CONTAINER_BINARY=~/.cache/k3c/runtime/container-CLI-version-7ed75e1*/bin/container \
  ./k3c-local docker up
cd ~/Dev/vehub-feedbackapi-go && \
  DOCKER_HOST=unix://$HOME/.config/k3c/docker.sock TESTCONTAINERS_RYUK_DISABLED=true \
  go test -tags integration ./integration_tests -run TestFeedbackReplayIT -v
```

## Implication

Engine-socket reachability (the headline change) is validated by a real consumer.
Testcontainers-based workflows still need: (a) host reachability of nested
**published ports**, and (b) the reaper container to start. Suggest tracking both
as follow-up tasks before declaring the Testcontainers contract fully met.

## Resolution (2026-06-29)

### Gap 1 — fixed (it was a deployment gap, not a design flaw)

The Phase-2 forwarder was never actually active in the failing run, for two
reasons, both now fixed:

1. **The unbundled dev build shipped no forwarder binary.**
   `runtime.DockerForwarderBinary()` looks for `k3c-docker-fwd` in the bundle or
   as a sibling of the k3c binary; `./k3c-local` had neither, so
   `ensureDockerForwarder` silently skipped. → **Fix:** `make build-unbundled`
   now also cross-compiles `k3c-docker-fwd` (linux/arm64) next to `$(BINARY)`
   (and `make install` copies it); bundled builds already ship it.
2. **The running sidecar predated `--publish-socket`.** It was created before
   Phase 2, so `docker up` only *started* it (no host↔guest bridge); the engine
   socket worked, nested ports could not. → **Fix:** `ensureDockerForwarder` now
   detects the missing bridge and tells the user to recreate
   (`k3c docker rm && k3c docker up`), and a fresh create wires `--publish-socket`.

**Verified end-to-end** against the real sidecar after recreating it with the
fixed build: forwarder running in the VM (`/run/k3c-docker-fwd.sock`), host bridge
at `~/.config/k3c/docker-fwd.sock`, and `docker run -d -p 18080:80 nginx:alpine`
→ `curl http://127.0.0.1:18080` from the host returned **HTTP 200**, through the
forwarder with the guest vmnet IP unreachable.

### Gap 2 — reaper: not reproducible on a fresh engine; standard workaround stands

Root cause (researched against testcontainers-go v0.38.0): Ryuk is the *only*
container TC pins to the engine's **default `bridge`** network (hardcoded
`NetworkMode=bridge`, no env override); `failed to create network` is the dind
daemon failing to attach Ryuk to that bridge at start (matches colima #2074 /
podman #2781). On a **fresh** k3c sidecar this **does not reproduce**: a
bridge-network container that publishes a port starts cleanly *and* its published
port is reachable from the host (`HTTP 200`), so the original failure was
transient / accumulated-network-state on the long-lived old sidecar.

Durable guidance (same as colima/podman/rancher-desktop): set
`TESTCONTAINERS_RYUK_DISABLED=true` for the sidecar. k3c now helps by **no longer
forcing `TESTCONTAINERS_RYUK_DISABLED=false`** in `k3c docker env` — so
`eval $(k3c docker env)` no longer clobbers a consumer's `=true`. Mapped ports
surface on loopback regardless; with Ryuk off the test cleans up via `t.Cleanup`.

### To re-run the consumer test

Rebuild so the forwarder ships, recreate the sidecar, then run with the reaper
off:
```sh
cd ~/dev/k3c && make build            # (bundled: ships gvnet + k3c-docker-fwd)
k3c docker rm && k3c docker up         # fresh create wires --publish-socket + forwarder
cd ~/Dev/vehub-feedbackapi-go && \
  DOCKER_HOST=unix://$HOME/.config/k3c/docker.sock TESTCONTAINERS_RYUK_DISABLED=true \
  go test -tags integration ./integration_tests -run TestFeedbackReplayIT -v
```
(For a fast unbundled iteration instead of `make build`: `make build-unbundled`
plus `K3C_GVNET_BINARY=<a built ./cmd/gvnet>` — the `gvnet` sibling name collides
with the `gvnet/` package dir, so dev builds pass it via env or use the bundle.)

### Confirmed GREEN (2026-06-29)

Re-ran end-to-end against a **freshly recreated** sidecar from the fixed unbundled
build (k3c + `k3c-docker-fwd` sibling + `K3C_GVNET_BINARY`, `RYUK_DISABLED=true`):
`TestFeedbackReplayIT` **passed**. Both Kafka and Schema Registry host-side readiness
probes succeeded (Gap 1's published-port forwarding now works), the consumer caught
up, and the read API returned every produced record. The `--publish-socket` bridge
required the **sidecar recreate** (`docker rm && docker up`) — a started-but-old
sidecar still lacks it, matching the resolution above.
