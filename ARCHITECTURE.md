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
  DOCKER_HOST = tcp://192.168.64.x:2375 ─── vmnet eth0 ──▶ dockerd (dind) :2375
  (the vmnet IP — host-routable)                            │
                                                            ├─ docker0 / bridges
                                                            └─ nested containers
                                                               └─ (optional) k3d
k3c host daemons
  pull-cache  :5011 ◀────────── containerd pulls via the mirror
  CONNECT pxy :3128 ◀────────── (default-mode egress; or transparent egress §4.2)

gateway 192.168.64.1                        eth0 192.168.64.x   [ eth1 gvnet ]
```

- **Host → engine:** `DockerHost` / `containerIP` return the **vmnet** IP
  (`192.168.64.x`), which is host-routable; Testcontainers reaches mapped
  container ports on that address.
- **Resources are fixed at create:** `k3c docker up --cpus N --memory XG`
  re-creates the sidecar (volume preserved); `k3c docker rm` removes it.
- **k3d on the sidecar** adds a third nesting level. Its API (`:64403` etc.) is
  published *inside* the dind and mirrored to the host by the
  `dockerPortForward` daemon.

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

**Host can reach the VM IP but not the published `127.0.0.1:<port>`**
The runtime forwards published ports to the *primary* NIC — keep **vmnet
first** so that's the host-routable address (gvnet's `192.168.127.x` is not).

### Toggling transparent egress for A/B comparison
`K3C_TRANSPARENT_EGRESS=1` (or `egress.transparent: true`) enables it; unset to
revert to the proxy/SNI path and compare behaviour.
```
