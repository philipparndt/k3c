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

- [x] 3.1 In-guest forwarder implemented as a minimal unix-socket port mux: package `dockerfwd` + `cmd/k3cdockerfwd` (cross-compiled linux/arm64 in the Makefile bundle step, resolved via `runtime.DockerForwarderBinary()`), launched detached in the VM by `ensureDockerForwarder` (`cluster/docker.go`); `--publish-socket <host>:/run/k3c-docker-fwd.sock` bridges its socket to the host.
- [x] 3.2 Data plane rerouted off the guest vmnet IP: new `dialSidecarPort`/`dialTarget` (`cluster/dockerports.go`) dial the published unix socket with a `"<port>\n"` mux header. Applied to **both** the nested-port mirror (`acceptDockerForward`) **and** the contested-port arbitration (`arbListener`/`arbitrate`/`:443` ingress in `cluster/daemons.go`) via the `sidecar:<port>` target scheme.
- [x] 3.3 Host-side `127.0.0.1:<published>` listener-per-port model kept; `reconcileDockerPorts` opens/closes listeners as discovery diffs the published set (now fully vmnet-independent — dropped the `containerIP` gate).
- [x] 3.4 Data path verified: hermetic e2e tests run the real `dockerfwd.Serve` + real `dialSidecarPort`/`acceptDockerForward` against a fake nested port; and a **live** test ran the real cross-compiled forwarder in a throwaway VM — host → `--publish-socket` → forwarder → nested `:9000` returned `NESTED_9000_OK` with no vmnet. (Full e2e against the user's `k3c-docker` needs a bundled build + sidecar recreate; deferred to avoid disrupting the running sidecar.)

## 4. Testcontainers integration & docs

- [x] 4.1 Mapped ports surface on host loopback (Phase 2 mirror) and `DOCKER_HOST`/the docker context point at the host unix socket (`DockerHost`). With a unix-socket `DOCKER_HOST`, Testcontainers resolves mapped-port connections to `localhost`, which the mirror serves. Verified: a `-p 18080:80` container on the real sidecar engine surfaces in `/containers/json` over the loopback endpoint (`PublicPort 18080`), the source for Testcontainers' `GetMappedPort`.
- [x] 4.2 `TESTCONTAINERS_HOST_OVERRIDE` deliberately **not** set — loopback surfacing makes it unnecessary, and the vmnet IP it would name isn't host-reachable on macOS 26. Precedence documented in `DockerEnv` (the host-resolution rule) and ARCHITECTURE.md §4.6.
- [~] 4.3 Data path verified component-wise against real components: discovery over the loopback engine API (real sidecar, above) + the in-guest forwarder data plane (live throwaway-VM test in §3.4). A full Testcontainers *client* run against `k3c-docker` needs the forwarder shipped in a bundled build + sidecar recreate (`make bundle` Swift toolchain) — deferred to avoid disrupting the running sidecar; offered as the rollout step.
- [x] 4.4 ARCHITECTURE.md updated: §2 sidecar diagram + bullets rewritten to the loopback-engine / forwarder model, new §4.6 "Host ⇆ sidecar engine & nested ports (no guest-L2 dependency)", and a §5 runbook symptom for unreachable mapped ports / Testcontainers.

## 5. Spec & validation

- [ ] 5.1 Apply the `docker-sidecar` spec deltas (modified "Start the sidecar" invariant + new nested-published-port requirement)
- [ ] 5.2 `openspec validate docker-sidecar-host-forwarder --strict` passes
- [ ] 5.3 Note in `host-egress` impact that the sidecar no longer depends on "vmnet stays primary for host reachability" (no spec change unless cluster behavior shifts)
