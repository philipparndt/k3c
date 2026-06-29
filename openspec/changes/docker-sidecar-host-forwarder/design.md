## Context

The docker sidecar is a `docker:dind` VM on Apple `container`. k3c exposes its
engine to the host **three ways, over two transports**, and only one transport
is failing:

```
                          ┌─────────────────── HOST (Mac) ───────────────────┐
  testcontainers ─────────┼──┐                                                │
                          │  │  (A) apple-publish   -p 127.0.0.1:2375:2375    │
                          │  ├──────────────────────────────────────────────►│ Apple `container`
                          │  │       ✅ WORKS (runtime's own port-forward)    │ runtime plumbing
                          │  │                                                │
                          │  │  (B) k3c engine socket  unix://docker.sock     │
                          │  ├─► startDockerSocket ──dial──► <vmIP>:2375 ─────┼──► vmnet L2
                          │  │       ❌ depends on guest L2                    │   ❌ INERT
                          │  │  (C) nested published ports                    │   (ARP incomplete)
                          │  └─► startDockerPortForward                       │
                          │         ├─ GET http://<vmIP>:2375/containers/json │
                          │         └─ dial <vmIP>:<dynPort> ─────────────────┼──► vmnet L2 ❌
                          └───────────────────────────────────────────────────┘
```

- **(A)** `docker.go:107` statically publishes the engine at `127.0.0.1:<DockerPort>`
  via Apple's runtime; this **works even when raw vmnet is inert**.
- **(B)** `dockerports.go:52,57` — the host unix socket forwarder dials
  `<vmIP>:2375`.
- **(C)** `dockerports.go:128,155,188` — the published-port watcher reads
  `http://<vmIP>:2375/containers/json` and dials `<vmIP>:<port>`.

Observed failure: ARP for the sidecar's guest vmnet IP is incomplete (the guest
vmnet NIC is inert on `bridge100` while the host gateway `.1` resolves cleanly),
so (B) and (C) both fail; only (A) survives. The single-NIC legacy path
(`egress.transparent: false`) is a guarded, supported branch in the code today
(`docker.go` skips every gvnet call and falls back to the SNI/CONNECT proxy;
`gvnetctl.go` returns early), so the spike must first reproduce the reported
single-NIC failure or confirm the path runs, before it can A/B-test whether
vmnet L2 works without the second gvnet NIC — i.e. whether the inertness is a
k3c dual-NIC
bring-up bug or a fundamental Apple-`container` limitation.

## Goals / Non-Goals

**Goals:**
- The sidecar engine and nested published ports are reachable from the host
  **without depending on the host reaching the guest vmnet IP at L2.**
- Testcontainers (incl. runtime-chosen ephemeral ports) works on the host.
- Keep existing working setups working — reuse the endpoint Apple already
  publishes; no regression where vmnet L2 *is* healthy.

**Non-Goals:**
- No change to cluster (non-sidecar) networking, `containerIP`, or kube-API
  reachability.
- No removal of the vmnet NIC; pod transparent-egress semantics unchanged.
- Not solving Kubernetes-in-VM port churn (out of scope; see Lima eBPF note).

## Research: how the ecosystem solves this (verified, cited)

A 5-angle deep search → 22 sources → 104 extracted claims → 25 adversarially
verified (3-vote; 17 confirmed, 8 killed). The convergent pattern:

```
   HOST                          stable control channel             GUEST (VM)
  ┌──────────────────┐         (vsock / SSH / unix sock)        ┌──────────────────┐
  │ userspace        │◀───────────────────────────────────────▶│ in-guest agent    │
  │ forwarder        │   dynamic Expose/Unexpose; "open :49xxx" │ watches new LISTEN│
  │ binds host ports │                                          │ sockets, tunnels  │
  └──────────────────┘                                          └──────────────────┘
   NEVER: host ──ARP──▶ guest vmnet IP ──▶ dial <vmIP>:<port>   ← what k3c does today
```

| Tool | Control channel | Dynamic-port discovery | Data plane | Guest L2? |
|------|-----------------|------------------------|------------|-----------|
| **Lima** (→ Colima) | gRPC over guest-agent conn (SSH optional, `LIMA_SSH_PORT_FORWARDER`) | guest agent diffs `LISTEN` sockets via netlink `SOCK_DIAG`, streams `Added/RemovedLocalPorts` | gRPC tunnel | **No** |
| **gvproxy / gvisor-tap-vsock** (→ podman machine) | HTTP `Expose`/`Unexpose` over unix socket *and* in-VM `192.168.127.1:80` | host or guest POSTs a forward at runtime | host binds port, injects Ethernet frames over **vsock** into a gVisor netstack | **No** |
| **Docker Desktop** | custom signaling (legacy vpnkit slirp-proxy → now gvisor-tap-vsock) | per-published-port proxy subprocess | userspace | **No** |
| **Rancher Desktop (macOS)** | — | — | does **not** forward to localhost; sets `TESTCONTAINERS_HOST_OVERRIDE=$(rdctl info --field ip-address)` | uses VM IP (and still hits IPv6-only localhost bugs) |

**Confirmed findings (high confidence unless noted):**
- All mature stacks avoid raw L2/ARP reachability of the guest IP; gvproxy is a
  pure-Go gVisor netstack that binds host ports and injects frames over vsock —
  no host-side ARP of the guest IP.
  `github.com/containers/gvisor-tap-vsock`,
  `pkg.go.dev/.../gvisor-tap-vsock/pkg/services/forwarder`.
- gvproxy exposes a runtime control API — `Expose(proto, local, remote)`,
  `Unexpose(...)`, `Mux() http.Handler` — over a unix socket
  (`POST /services/forwarder/expose|unexpose`, `GET /all`); changes take effect
  immediately, no restart. Drivable from the guest too (`192.168.127.1:80`). *ibid.*
- Lima's guest agent enumerates `TCPListen`/`UDPUnconnected` sockets via netlink
  `SOCK_DIAG`, diffs per tick, streams `Added/RemovedLocalPorts` over a gRPC
  server-stream; the host opens/closes forwards dynamically. gRPC forwarder is
  default since v1.1.0 (was reverted in v1.0.1, re-enabled #3046).
  `lima/pkg/guestagent/guestagent_linux.go`, lima issue #3046.
- podman machine delegates **all** forwarding to the external `gvproxy` binary;
  podman itself does not forward. podman commit `7ef3981`.
- **Apple `container`**: each container is a lightweight VM on a vmnet network
  (`default`, e.g. `192.168.64.0/24`); `--publish` supports **only static host
  ports at launch** (incl. loopback `127.0.0.1:…`), with **no documented runtime
  forwarding API** through v1.0.0 (2026-06-09). `apple/container` `docs/how-to.md`,
  `docs/technical-overview.md`. This is exactly why dialing the guest IP is the
  fragile path and why dynamic ports cannot be re-published after launch.
- Testcontainers: `TESTCONTAINERS_HOST_OVERRIDE` (`host.override`) sets the host
  on which ports are exposed, overriding auto-detection.
  `java.testcontainers.org/features/configuration/`,
  `docs.rancherdesktop.io/.../using-testcontainers/`.

**Refuted / soft (do not rely on):**
- vpnkit's exact transport (a "9P directory" signaling claim and a "pure vsock,
  never L2" claim) — refuted; treat the legacy vpnkit data-plane detail as soft.
- OrbStack's exact mechanism — **unverified** by any surviving primary source.
- Lima eBPF kprobe port-watch (PR #3067) — closed, explicitly broken for k8s
  iptables churn; not shipping. Successor #4066 unverified.

## Decision

Adopt the ecosystem pattern, staged by confidence:

1. **Engine (Phase 1, high confidence, small):** back the host engine socket and
   `DOCKER_HOST` with the **stable Apple-published loopback endpoint** (`127.0.0.1:
   <DockerPort>`, already created at `docker.go:107`) instead of dialing
   `<vmIP>:2375`. This is the same move the ecosystem made — dial a stable
   host-local endpoint, never the guest IP — and it makes `docker`/the engine
   work with zero guest-L2 dependency.

2. **Nested ports (Phase 2, gated on the spike):** keep the existing engine-API
   port discovery, but replace the `dial <vmIP>:<port>` data plane with a tunnel
   over a control channel that does **not** require guest L2:
   - **If Apple `container` exposes usable host↔guest vsock to an arbitrary guest
     process:** run a small in-guest forwarder (gvproxy-style or a minimal mux)
     and reach it over vsock — the cleanest design.
   - **Otherwise:** statically `--publish` **one** control port at sidecar
     creation and multiplex every dynamic container port through an in-guest
     forwarder over that single stable port.
   Either way the host opens a `127.0.0.1:<published>` listener per discovered
   port and proxies through the stable channel.

3. **Testcontainers:** surface mapped ports on host loopback and set
   `DOCKER_HOST` (+ `TESTCONTAINERS_HOST_OVERRIDE` only if a non-loopback host is
   unavoidable), per the Rancher-Desktop precedent.

## Open Questions (the spike gates Phase 2)

1. **Does Apple `containerization` expose a usable host↔guest vsock channel to an
   arbitrary in-guest process (a dind sidecar / forwarder), or is vsock reserved
   for `vminitd`?** Decides vsock vs. static-control-port multiplexing. Note: k3c
   already vendors `gvisor-tap-vsock` for transparent egress, but only as a
   host-side netstack with the guest NIC attached over a unixgram socket — not an
   arbitrary guest *process* speaking vsock — so this question is still open (the
   library being on hand does lower the cost of the vsock option if it pans out).
2. **Is the guest vmnet L2 inertness a k3c dual-NIC bring-up bug or a fundamental
   Apple-`container` limitation?** Cheapest experiment: exercise the
   `egress.transparent: false` single-NIC path (reproduce the reported failure or
   confirm it runs), then test single-NIC vmnet reachability. If it's a bug,
   Phase 1 may suffice and Phase 2 shrinks.
3. Did Lima's PR #4066 (successor to the eBPF #3067) land and solve k8s
   iptables-churn detection? (Informs the discovery mechanism only.)

## Risks / Trade-offs

- **[Phase 2 needs a new in-guest agent + launch path]** → start from the proven
  gvproxy `Expose/Unexpose` building block or a minimal TCP mux; keep the host
  discovery (engine-API poll) we already have.
- **[Static-control-port fallback adds a multiplexing hop]** → acceptable;
  Lima/podman accept the same userspace-forwarder cost. Bounded by one extra
  proxy per connection.
- **[`TESTCONTAINERS_HOST_OVERRIDE` couples to host topology]** → prefer loopback
  surfacing so the override is unnecessary; only set it as a last resort.
- **[Regression where vmnet L2 is healthy]** → none expected: the loopback
  endpoint is the same one Apple already publishes; Phase 1 is a target swap, not
  a transport addition.
