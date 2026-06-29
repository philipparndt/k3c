## 1. Decision spike (prerequisite — gates Phase 2)

- [ ] 1.1 Exercise the `egress.transparent: false` single-NIC legacy path — it is a guarded, supported branch today, so reproduce the reported single-NIC failure or confirm the path runs (`cluster/gvnetctl.go` / `cluster/docker.go` / `config/config.go`)
- [ ] 1.2 With the single-NIC path exercised, test single-NIC vmnet reachability: does the host resolve+reach the guest vmnet IP at L2 when no gvnet NIC is attached? Record whether inertness is a dual-NIC bring-up bug or fundamental
- [ ] 1.3 Determine whether Apple `container`/`containerization` exposes a usable host↔guest **vsock** channel to an arbitrary in-guest process (not just `vminitd`); spike a minimal guest listener + host dial
- [ ] 1.4 Decide Phase 2 transport from 1.2/1.3: vsock (preferred) vs. static-published control port + in-guest mux; write the decision back into `design.md`

## 2. Engine endpoint: stop depending on guest vmnet L2 (Phase 1, high confidence)

- [ ] 2.1 Repoint `startDockerSocket` (`cluster/dockerports.go`) at the stable Apple-published loopback endpoint (`127.0.0.1:<DockerPort>`) instead of dialing `<vmIP>:2375`
- [ ] 2.2 Confirm `DockerHost`/`ensureDockerContext` wiring (`cluster/docker.go`) resolves to the host socket/loopback, never the guest IP; adjust `DockerHostTCP` callers if needed
- [ ] 2.3 Make the engine-API discovery poll (`dockerPublishedPorts`) read the engine over the stable endpoint rather than `http://<vmIP>:2375`
- [ ] 2.4 Verify `docker ps` / `docker run` and `eval $(k3c docker env)` work with the guest vmnet IP unreachable (simulate inert vmnet)

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
