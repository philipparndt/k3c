# k3c architecture

k3c runs k3s clusters and a Docker sidecar on Apple `container` —
lightweight, per-workload Linux VMs (Virtualization.framework) on Apple
Silicon. This document explains the layers, the networking (ingress / egress /
DNS), and how to trace problems.

The hard problem k3c solves: on a corporate full-tunnel VPN the VMs have **no
direct internet**, can't resolve corporate DNS, and don't trust the corporate
CA. k3c bridges that from the host, where DNS, CA trust and egress all work.

---

## 1. Virtualization layers

Each Apple `container` is a *VM with its own Linux kernel* — not a namespaced
process. The workload runs inside that VM.

```
macOS host  (Apple Silicon, arm64)
│
├─ k3c CLI ──▶ Apple `container` runtime (apiserver + plugins, launchd)
├─ k3c host daemons    CONNECT proxy · SNI gateway · pull-cache · port-forward
├─ corporate VPN/proxy client    carries all host-originated egress
│
└─ container VM  (Virtualization.framework, one per workload)
   │
   ├─ Linux kernel  (Kata; 6.18+ has br_netfilter + vxlan)
   ├─ vminitd       (guest init, from the bundled vminit:latest image)
   ├─ NICs:  eth0 vmnet   [ + eth1 gvnet in transparent-egress mode ]
   │
   └─ workload
      ├─ k3s      ──▶ pods                         ("k3c cluster")
      └─ dockerd  ──▶ containers                   ("docker sidecar")
                  └─▶ (optional) k3d = k3s in containers
```

Nesting depth:

| Shape              | host → VM → … workload                                |
|--------------------|-------------------------------------------------------|
| k3c cluster        | host → VM → **k3s** → pods                             |
| Docker sidecar     | host → VM → **dockerd** → containers                  |
| k3d on the sidecar | host → VM → dockerd → **k3d (k3s in containers)** → pods (3 levels) |

### How k3c finds the `container` runtime

`k3c` resolves the Apple `container` CLI in this order (see `runtime/`):

1. `K3C_CONTAINER_BINARY` · 2. `K3C_CONTAINER_FROM_PATH` · 3. `containerBinary`
in config · 4. **bundled** runtime embedded in release builds · 5. `container`
on `PATH`.

Release builds embed the whole runtime tree (`bin/container`, `libexec/…`
plugins, the `gvnet` helper, `init.tar`) and extract it once to
`~/.cache/k3c/runtime/<version>/`, then drive it with `CONTAINER_INSTALL_ROOT`
pointed there. On first use the bundled `vminit:latest` is loaded from
`init.tar`.

---

## 2. The Docker sidecar (`k3c docker up`)

A `docker:dind` VM exposing a real Docker Engine API (for Testcontainers, the
`docker` CLI, k3d). The image store lives on a volume that survives recreation.

```
macOS host                                  sidecar VM  "k3c-docker"
──────────                                  ───────────────────────
docker CLI / Testcontainers
  DOCKER_HOST = unix://…/docker.sock              dockerd (dind) :2375
    │ engine socket forwarder                          ▲
    └─▶ 127.0.0.1:<DockerPort> ─── Apple -p publish ───┘
                                                        ├─ docker0 / bridges
  127.0.0.1:<mapped port>                               └─ nested containers
    ▲ per-port mirror                                      (publish ports)
    └── docker-fwd.sock ─ --publish-socket (vsock) ─▶ k3c-docker-fwd
                                                        └─▶ 127.0.0.1:<port>
k3c host daemons
  pull-cache  :5011 ◀────────── containerd pulls via the mirror
  CONNECT pxy :3128 ◀────────── (default-mode egress; or transparent egress §4.2)

gateway 192.168.64.1                        eth0 192.168.64.x   [ eth1 gvnet ]
```

- **Host → engine:** `DOCKER_HOST` is a host unix socket (`DockerHost`) the
  daemon forwards to the engine — never the guest vmnet IP, which is not
  host-reachable at L2 on macOS 26 (see §4.6). The socket path is stable across
  sidecar recreate. Each connection is **routed by request type** (peek the first
  HTTP head, `routeEngineConn`): hijacked/interactive streams (`docker
  exec`/`attach`, `Connection: Upgrade`) go over the full-duplex
  `--publish-socket` bridge, which carries hijacks; everything else (inspect,
  logs, `docker cp` archive PUT, build, …) goes over the **Apple-published
  loopback** `127.0.0.1:<DockerPort>`. This split exists because the
  `--publish-socket` bridge **head-of-line-blocks** — a single open-but-undrained
  streaming response stalls the next request on it (Apple's mux, not k3c code) —
  while the loopback publish does not but drops hijacked streams. Routing the
  common request/response path to the HOL-free loopback is what lets
  Testcontainers' `wait.ForLog` (open logs → concurrent Inspect, undrained) reach
  readiness. The whole connection follows its first request's type: moby dials a
  dedicated, unpooled connection per hijack, so pooled connections only ever
  carry non-hijack requests.
- **Nested published ports** are discovered via the engine API (over the loopback
  endpoint) and mirrored onto host `127.0.0.1:<port>`; the data plane tunnels
  through the in-guest forwarder `k3c-docker-fwd` over a unix socket the runtime
  bridges with `--publish-socket` (vsock) — again no vmnet dependency (§4.6).
- **Testcontainers** works out of the box: with a unix-socket `DOCKER_HOST` it
  resolves mapped-port connections to `localhost`, which the mirror serves, so
  `TESTCONTAINERS_HOST_OVERRIDE` is not needed.
- **Resources are fixed at create:** `k3c docker up --cpus N --memory XG`
  re-creates the sidecar (volume preserved); `k3c docker rm` removes it.
- **k3d on the sidecar** adds a third nesting level. Its API (`:64403` etc.) is
  published *inside* the dind and reaches the host through the same nested-port
  forwarder.

---

## 3. The cluster (`k3c cluster create`)

k3s runs directly in a VM (no docker nesting). A second small VM hosts the
optional local registry.

```
macOS host                                  server VM  "<name>-server"
──────────                                  ──────────────────────────
kubectl
  server: 127.0.0.1:<apiport> ─── vmnet ──▶ k3s server :6443  (kube API)
  (published -p ──▶ VM:6443)                 │
                                             ├─ flannel CNI · kube-proxy
ingress browser/host                         ├─ CoreDNS  (service CIDR)
  127.0.0.1:<ingress> ──────── vmnet ──────▶ ingress controller :443
                                             └─ pods …

k3c host daemons + registry VM              node-ip pinned to vmnet eth0
```

- **Host → API / ingress** go via published ports
  (`-p 0.0.0.0:<apiport>:6443`, `-p 127.0.0.1:<ingress>:443`). The kubeconfig
  points at `127.0.0.1:<apiport>`.
- **CIDRs:** defaults `10.42.0.0/16` (pods) / `10.43.0.0/16` (services). If a
  full-tunnel VPN claims `10.53.0.0/16`, move the service CIDR off it (e.g.
  `10.52` / `10.54`) — a claimed CIDR black-holes ClusterIP traffic and CoreDNS
  times out. `k3c doctor` checks for this clash.
- **Node IP** is pinned to the **vmnet** NIC so the host can always reach the
  kubelet / API even when gvnet owns the default route (see §4).

---

## 4. Networking

### 4.1 The two NICs

vmnet is always present; gvnet is added only in transparent-egress mode.

```
vmnet  (Apple shared mode)              gvnet  (transparent egress, opt-in)
─────────────────────────              ───────────────────────────────────
host bridge "bridge100"                per-VM userspace netstack
gateway 192.168.64.1                     (gvisor-tap-vsock), one process per VM
VM gets 192.168.64.x  (eth0)           VM gets 192.168.127.x  (eth1)
HOST ⇄ VM is routable                  gateway 192.168.X.1
reaches host loopback daemons          NOT host-routable (lives in the netstack);
                                       re-originates every outbound connection
                                       from host sockets
```

**Dual-NIC ordering matters.** k3c attaches `--network default` (vmnet)
**first** so it stays the *primary* NIC — the runtime targets it for published
ports and `containerIP`. gvnet is attached second; the guest entrypoint then
repoints the **default route** and **`/etc/resolv.conf`** at the gvnet NIC:

```
guest routing (transparent-egress mode)
  default          via 192.168.127.1 dev eth1     egress goes out gvnet
  192.168.64.0/24  dev eth0                        host ⇄ VM stays on vmnet
  192.168.127.0/24 dev eth1
  nameserver 192.168.127.1                         DNS via the gvnet resolver
```

> Per-VM gvnet networks each need a **distinct /24** (`192.168.127`, `.128`, …)
> — the runtime rejects overlapping subnets. The `gvnet` netstack is a separate
> binary (keeps the gvisor stack out of the lean `k3c` binary), spawned detached
> (`setsid`) so it outlives the `k3c` process, and exits when its VM disconnects.

### 4.2 Egress — two modes

**(a) Default: CONNECT proxy + SNI gateway** (the VMs have no direct internet)

```
containerd image pull
  guest ─ HTTP_PROXY 192.168.64.1:3128 ─▶ host CONNECT proxy ─▶ VPN ─▶ registry

pod HTTPS egress
  pod ─ DNS(egress domain) ─▶ CoreDNS ─ override ─▶ answers 192.168.64.1
  pod ─ :443 ─▶ host SNI gateway ─ reads ClientHello SNI ─▶ dials the real host
                                                            via the VPN, or the
                                                            cluster ingress for
                                                            ingressDomains
```

**(b) Transparent (gvnet)** — `egress.transparent: true` / `K3C_TRANSPARENT_EGRESS=1`

```
any guest connection
  guest ─ default route ─▶ gvnet NIC ─▶ host-side netstack ─ re-originates from
        a host socket ─▶ VPN ─▶ internet
  DNS:  guest ─▶ 192.168.127.1 (gvnet resolver) ─▶ host resolver
```

No SNI gateway, no CoreDNS override, no `HTTP_PROXY` — pods resolve real DNS
and connect directly; the netstack terminates the guest TCP/IP and re-emits
each connection from the host, so the corporate VPN/proxy carries it (the same
proven principle as the SNI gateway).

> **docker.io stays corp-blocked even with transparent egress** — that's why
> the pull-cache / registry mirror is still required (next section).

### 4.3 Pull cache & registries (corporate CA termination)

The guest doesn't trust the corporate CA, so it can't pull from the corporate
registry directly over TLS. The host pull-cache solves this:

```
guest containerd ─ registries.yaml mirror ─▶ host pull-cache :5011  (plain HTTP)
                                              │ maps docker.io → corporate mirror,
                                              │ does DNS + corporate-CA TLS +
                                              ▼ egress ON THE HOST
                                            corporate registry
```

Every registry the cluster pulls from needs a `─▶ http://192.168.64.1:5011`
mirror entry. Dropping it makes the guest pull the corporate registry directly
and fail with `x509: certificate signed by unknown authority`.

### 4.4 Ingress

```
browser/host ─ :<ingress> ─▶ host published port ─ vmnet ─▶ VM :443
                                                            └ ingress controller ─▶ pod
```

In default mode the SNI gateway additionally routes configured `ingressDomains`
to the cluster ingress instead of egressing them.

### 4.5 Host daemons (`k3c daemons`)

One detached process, (re)spawned by `k3c docker up` / `k3c cluster …`. It must
run with the **project config** (for the pull-cache) and the **current binary**:

| listener          | port      | purpose                                            |
|-------------------|-----------|----------------------------------------------------|
| CONNECT proxy     | `:3128`   | containerd image pulls (default-mode egress)       |
| SNI gateway       | `:443`    | pod HTTPS egress (default-mode)                     |
| pull-cache        | `:5011`   | registry pull-through + corporate-CA termination   |
| registry forward  | cfg       | host → local registry VM                            |
| dockerPortForward | dynamic   | mirror the sidecar VM's published ports to the host |
| webhook           | internal  | ignore-cpu/memory-requests admission               |

> The daemons' config is whatever the **last** `k3c` invocation used. Running
> `k3c docker up` / `k3c cluster …` from a directory **without** the project
> `k3c.yaml` respawns them without the pull-cache and breaks nested-cluster
> pulls. Always run from the project directory (or pass `--config`).

### 4.6 Host ⇆ sidecar engine & nested ports (no guest-L2 dependency)

The host reaches the sidecar's docker engine and its nested published ports
**without ever dialing the guest vmnet IP** — that IP is not host-reachable at L2
on macOS 26 (Apple `container`/vmnet blocks host→guest ARP even single-NIC;
guest→host and guest→guest still work). Two paths replace the old `<vmIP>:2375`
dialing:

- **Engine (Phase 1).** dockerd is statically published by the runtime at
  `127.0.0.1:<DockerPort>` (`-p 127.0.0.1:<DockerPort>:2375`). The host engine
  socket (`DockerHost` → `unix://…/docker.sock`) and the published-port discovery
  poll both forward to that loopback endpoint, never the vmnet IP.
- **Nested published ports (Phase 2).** A small in-guest forwarder
  (`k3c-docker-fwd`, cross-compiled linux/arm64, shipped in the runtime bundle,
  staged into the VM via the `/k3c-ca` mount and run with `container exec -d`)
  listens on `/run/k3c-docker-fwd.sock`. The runtime bridges that socket to the
  host (`docker-fwd.sock`) over **vsock** via `--publish-socket`. The daemon
  discovers published ports over the engine API, opens a `127.0.0.1:<port>`
  listener per port, and tunnels each connection through the forwarder with a
  `"<port>\n"` header → `127.0.0.1:<port>` inside the VM. The same channel backs
  contested-port arbitration (e.g. `:443` ingress to a nested k3d cluster).

This is the pattern Lima, podman+gvproxy, and Docker Desktop all use (a userspace
forwarder over a stable control channel, never raw guest-L2). Because the engine
and mapped ports surface on loopback, **Testcontainers needs no
`TESTCONTAINERS_HOST_OVERRIDE`** — its unix-socket `DOCKER_HOST` resolves to
`localhost`, which the mirror serves. If the forwarder binary is absent (an
unbundled dev build), the engine still works (Phase 1) but nested published ports
are not surfaced — recreate with a bundled build to enable them.

---

## 5. Tracing problems

A symptom-first runbook. `CB` = the resolved container binary
(`k3c container …` is a passthrough with the right environment).

### Where the logs live
```
~/.config/k3c/daemons.log          host daemons (proxy / SNI / cache / forward)
~/.config/k3c/gvnet/<vm>.log       per-VM transparent-egress netstack
~/.cache/k3c/runtime/<version>/    extracted bundled runtime (+ init.tar)
container logs <name>              a VM's stdio / k3s / dockerd output
```

### First checks
```sh
k3c doctor                      # CIDR clashes, connectivity, runtime, coredns
k3c container ls -a             # VM states + IPs (vmnet,gvnet)
k3c container image ls          # is vminit:latest present? (init image)
k3c docker status               # sidecar state + endpoint
```

### Symptom → likely cause

**`could not extract host from reference vminit:latest`**
The bundled init image isn't loaded. `k3c container image ls`; if absent,
`k3c container image load -i ~/.cache/k3c/runtime/<version>/init.tar`.

**Cluster / sidecar "not reachable" (API/engine refused)**
Almost always the **host daemons**, not the workload. Check:
```sh
cat ~/.config/k3c/proxy.pid ; ps -p $(cat ~/.config/k3c/proxy.pid)   # right binary + --config?
lsof -nP -iTCP:5011 -sTCP:LISTEN                                     # pull-cache up?
```
Daemons running a stale binary, or without the project config (no pull-cache),
break nested pulls and port-forwards. Fix: re-run `k3c docker up` from the
project dir (with `k3c.yaml`) using the current binary.

**Pods `Pending` / `Insufficient memory`**
The VM is too small. The sidecar defaults to 8G (`cluster.memory` only sizes
*native* clusters). `k3c docker up --cpus N --memory XG` (re-creates), or add a
`docker:` section to `k3c.yaml`.

**`x509: certificate signed by unknown authority` on image pull**
A registry mirror lost its `─▶ :5011` cache endpoint; the guest is pulling the
corporate registry directly without the CA. Restore the cache mirror.

**`UnknownHostException` for a `*.svc` (cluster-internal) name**
Not DNS — the target pod isn't `Running` (so its headless-service record
doesn't exist). Fix the pod (usually the resource/pull issue above).

**Transparent egress not working**
```sh
CB exec <vm> sh -c 'ip route; cat /etc/resolv.conf'   # default via gvnet? DNS = gvnet gw?
kill -0 $(cat ~/.config/k3c/gvnet/<vm>.pid)           # netstack alive?
tail ~/.config/k3c/gvnet/<vm>.log
```
The default route must be the gvnet NIC and `/etc/resolv.conf` the gvnet
gateway. The netstack is per-VM and exits when its VM stops — it is respawned
on `up`/`start`. `connect … failed: 2/61` during a VM bootstrap means a missing
(2) or dead (61) netstack socket — respawned by re-running `up`.

**Published `127.0.0.1:<port>` unreachable / Testcontainers can't reach mapped ports**
Published ports surface via the runtime's own `-p 127.0.0.1:…` forward (engine,
kube API, ingress) or — for the docker sidecar's *nested* container ports — via
the in-guest forwarder (§4.6). Neither depends on the guest vmnet IP (not
host-reachable on macOS 26). For the sidecar / Testcontainers, check the
forwarder:
```sh
CB exec k3c-docker pidof k3c-docker-fwd     # in-guest forwarder running?
ls -l ~/.config/k3c/docker-fwd.sock         # host side of --publish-socket present?
grep "forwarding .* -> sidecar" ~/.config/k3c/daemons.log
```
An unbundled build ships no forwarder binary, so nested published ports aren't
surfaced (the engine itself still works via the loopback endpoint). Recreate with
a bundled build: `k3c docker rm && k3c docker up`. Testcontainers needs no
`TESTCONTAINERS_HOST_OVERRIDE` — the unix-socket `DOCKER_HOST` resolves to
`localhost`, which the mirror serves.

### Toggling transparent egress for A/B comparison
`K3C_TRANSPARENT_EGRESS=1` (or `egress.transparent: true`) enables it; unset to
revert to the proxy/SNI path and compare behaviour.
```
