## 1. Decision spike (prerequisite — gates Phase 2)

- [x] 1.1 ~~Fix the `egress.transparent: false` panic~~ — **N/A: there is no panic** (it is a guarded, supported branch). Verified no `panic(` in `cluster/`/`config/`/`proxy/`/`gvnet/`; `egress.transparent: false` degrades cleanly to the single-NIC CONNECT-proxy path (`gvnetctl.go:61`, `docker.go:120`). Design doc claim corrected.
- [x] 1.2 Single-NIC vmnet reachability tested (macOS 26.5.1, bundled runtime `7ed75e1`, 2026-06-29): **host→guest vmnet L2 is dead by Apple-`container`/vmnet design, NOT a dual-NIC bug.** A plain single-NIC `container run` (`192.168.64.8`, default network only) is as host-unreachable (ARP `(incomplete)` on `bridge100`) as the dual-NIC sidecar; isolation is directional (guest→gateway and guest→guest work). Optimistic "Phase 1 suffices" branch falsified → Phase 1 + Phase 2 both required. See `design.md` OQ#2.
- [x] 1.3 Host↔guest channel for an arbitrary in-guest process — **confirmed via `--publish-socket`** (macOS 26.5.1, bundled runtime `7ed75e1`, 2026-06-29). End-to-end spike: `alpine/socat` listening on `/run/sk.sock`, `--publish-socket /tmp/k3c-sk.sock:/run/sk.sock`, host `nc -U /tmp/k3c-sk.sock` → received the guest banner, **no vmnet involved**. Framework-level arbitrary vsock exists too but isn't reachable from k3c's Go (CLI shell-out only); `--ssh` is the reverse direction.
- [x] 1.4 Phase 2 transport decided: **`--publish-socket` unix-socket bridge** (in-guest forwarder listens on a unix socket, published once at sidecar creation; host multiplexes per-port `127.0.0.1` listeners through it). No fork/Swift/vsock-API changes; static-published TCP control port kept as fallback. Written back into `design.md` (Decision §2, OQ#1).

## 2. Engine endpoint: stop depending on guest vmnet L2 (Phase 1, high confidence)

- [x] 2.1 `startDockerSocket` now dials the stable loopback endpoint via new helper `dockerEngineEndpoint(cfg)` = `127.0.0.1:<DockerPort>` instead of `containerIP`+`<vmIP>:2375` (`cluster/dockerports.go`). Hermetic test `TestStartDockerSocketForwardsToLoopbackEngine` exercises the real function end-to-end.
- [x] 2.2 `DockerHost` already returns the host unix socket (`unix://<BaseDir>/docker.sock`) and `ensureDockerContext` sets the context host to it — both loopback/socket, never the guest IP. `DockerHostTCP` had **no callers**; repointed it to the loopback endpoint anyway to remove the guest-IP landmine.
- [x] 2.3 `dockerPublishedPorts(endpoint)` now queries `http://127.0.0.1:<DockerPort>/containers/json` (signature `ip`→`endpoint`); `reconcileDockerPorts(cfg, …)` passes `dockerEngineEndpoint(cfg)`. Unit test `TestDockerPublishedPortsQueriesEndpointAndParses`.
- [x] 2.4 Verified live against the real engine (vmnet `192.168.64.7:2375` confirmed DEAD): `docker -H tcp://127.0.0.1:2375 version` → server 29.6.1; `GET /containers/json` → OK; and the full `DOCKER_HOST=unix://` path through a loopback-backed unix socket → `docker version` + `docker ps` exit 0. (Data plane for nested ports stays on vmnet — Phase 2, §3.)

## 3. Nested published ports: control-channel forwarder (Phase 2, gated on §1)

- [ ] 3.1 Implement the in-guest forwarding agent (gvproxy-style `Expose/Unexpose`, or a minimal TCP mux) and its launch in the sidecar
- [ ] 3.2 Replace the `dial <vmIP>:<port>` data plane in `reconcileDockerPorts`/`acceptDockerForward` with a tunnel over the chosen stable channel (vsock or the static control port)
- [ ] 3.3 Keep the existing host-side `127.0.0.1:<published>` listener-per-port model; open/close forwards as discovery diffs the published set
- [ ] 3.4 Verify a runtime-chosen ephemeral published port is reachable from the host within one poll cycle, with the guest vmnet IP unreachable

## 4. Testcontainers integration & docs

- [ ] 4.1 Ensure mapped ports surface on host loopback; set `DOCKER_HOST` accordingly in `DockerEnv`/context
- [ ] 4.2 Only if loopback surfacing is impossible, set `TESTCONTAINERS_HOST_OVERRIDE` to a host-reachable address; document the precedence
- [ ] 4.3 End-to-end: run a real Testcontainers test (e.g. a Postgres container) against the sidecar from the host with vmnet L2 inert
- [ ] 4.4 Update ARCHITECTURE.md / runbook with the new host-reachability model

## 5. Spec & validation

- [ ] 5.1 Apply the `docker-sidecar` spec deltas (modified "Start the sidecar" invariant + new nested-published-port requirement)
- [ ] 5.2 `openspec validate docker-sidecar-host-forwarder --strict` passes
- [ ] 5.3 Note in `host-egress` impact that the sidecar no longer depends on "vmnet stays primary for host reachability" (no spec change unless cluster behavior shifts)
