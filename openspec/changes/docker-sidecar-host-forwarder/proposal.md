## Why

The docker-sidecar contract says the engine "SHALL be reachable from the host at
the sidecar's vmnet IP." k3c's own forwarders enforce that literally:
`startDockerSocket` dials `<vmIP>:2375` and `startDockerPortForward` dials
`<vmIP>:<port>` (`cluster/dockerports.go:52,57,128,155,188`). Both therefore
depend on the host resolving and reaching the **guest vmnet IP at L2**.

In at least one real environment that assumption is violated: ARP for the
sidecar's vmnet IP is incomplete (the guest vmnet NIC is inert / not answering
on `bridge100`), so every k3c forwarder that dials the guest IP fails — the
engine socket and **all** nested published ports — while Apple's own static
`-p 127.0.0.1:<port>:2375` publish (`cluster/docker.go:107`) keeps working. Net
result: `docker` and Testcontainers cannot run against the sidecar from the host.

A deep survey of every mature Docker-on-Mac VM stack (Lima/Colima, podman
machine, Docker Desktop, gvproxy/gvisor-tap-vsock, Rancher Desktop) found a
unanimous architecture: **none depend on the host reaching the guest IP at L2.**
They run an in-guest agent plus a host-side userspace forwarder over a stable
control channel (vsock / SSH / unix socket), and register runtime-chosen ports
dynamically. Depending on raw guest L2 is the foundation the ecosystem
abandoned. Full evidence and citations are in `design.md`.

## What Changes

- **Stop dialing the guest vmnet IP for the engine.** Back the host engine
  socket (and `DOCKER_HOST`) with a stable host-local endpoint that does not
  depend on guest L2 — the Apple-runtime-published loopback port (already
  created at `docker.go:107`) and/or the host unix socket. *(high confidence,
  small)*
- **Forward nested published ports over a stable channel, not the guest IP.**
  Keep the existing engine-API port discovery, but tunnel each published port to
  the sidecar over a control channel reachable independently of guest L2 (an
  in-guest forwarding agent on a statically-published control port, or vsock if
  Apple `container` exposes it to an arbitrary guest process). *(gated on the
  spike below)*
- **Make Testcontainers resolve mapped ports to a host-reachable address** —
  set `DOCKER_HOST` and, where required, `TESTCONTAINERS_HOST_OVERRIDE`, instead
  of relying on the guest IP being host-routable.
- **Prerequisite spike + the `egress.transparent: false` panic fix** that gates
  the architecture decision (is the inertness a dual-NIC bring-up bug or a
  fundamental Apple-`container` limitation? does it expose guest vsock?).

## Capabilities

### Modified Capabilities

- `docker-sidecar`: the engine-reachability invariant changes from "reachable at
  the guest vmnet IP" to "reachable over a stable host-local endpoint that does
  not depend on guest vmnet L2"; add a new requirement that nested published
  ports (including runtime/Testcontainers ports) are reachable from the host
  without guest L2.

## Impact

- **Code:** `cluster/dockerports.go` (socket + port-forward target resolution),
  `cluster/docker.go` (`DockerHost`/context wiring, sidecar control-port publish),
  `cluster/gvnetctl.go` + `config/config.go` (NIC bring-up, the `transparent:false`
  panic), possibly a new in-guest forwarder agent + its launch.
- **Affected but not modified:** `host-egress`'s "vmnet stays primary for host
  reachability" scenario — the sidecar will no longer *depend* on host-routable
  vmnet, though clusters may still use it for `containerIP`/kube-API.
- **Behavior:** the sidecar works even when the guest vmnet IP is not
  host-routable; existing working setups keep working (loopback path is the same
  endpoint Apple already publishes).
- **Non-goals:** no change to cluster (non-sidecar) networking; no removal of the
  vmnet NIC; transparent-egress semantics for pods unchanged.
