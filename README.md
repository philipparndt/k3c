# k3c

Local [k3s](https://k3s.io) clusters on [Apple `container`](https://github.com/apple/container) —
like [k3d](https://k3d.io), but for Apple's native container runtime instead of Docker.

No Docker Desktop, no OrbStack, no Colima — no license costs and no extra VM
layer. Just macOS 26+, the Apple `container` tool, and `kubectl`.

```console
$ k3c cluster create
2026-06-10T17:03:57+02:00 [   0] INFO  [  1] starting host daemons (proxy :3128, sni-gateway :443)
2026-06-10T17:03:57+02:00 [   0] INFO  [  1] preparing node config (registries.yaml, CA bundle)
2026-06-10T17:03:57+02:00 [   0] INFO  [  1] starting k3s server (14 cpus, 8G memory)
2026-06-10T17:03:58+02:00 [   1] INFO  [  1] waiting for kubeconfig
2026-06-10T17:04:00+02:00 [   3] INFO  [  1] merging kubeconfig (context: k3c-k3c)
2026-06-10T17:04:00+02:00 [   3] INFO  [  1] waiting for node to become Ready
NAME         STATUS   ROLES                  AGE   VERSION
k3c-server   Ready    control-plane,master   3s    v1.33.9+k3s1
2026-06-10T17:04:07+02:00 [  10] INFO  [  1] configuring CoreDNS egress override
2026-06-10T17:04:28+02:00 [  31] INFO  [  1] cluster is up (context: k3c-k3c)
```

## Why

k3d and kind both require a Docker API; Apple `container` doesn't provide
one, and its one-VM-per-container model comes with kernel and networking
constraints that break a naive k3s setup. k3c bundles all the workarounds:

| Problem | k3c's answer |
|---|---|
| VM kernel has no **nftables** → kube-proxy dies | forces the iptables-legacy backend |
| no **vxlan** → flannel's default backend fails | `--flannel-backend=host-gw` |
| no **br_netfilter** → same-node service replies dropped (pod DNS dead) | `kube-proxy --masquerade-all` |
| `container exec`/`cp` hang on a running k3s container | config and kubeconfig flow through a bind mount |
| amd64-only images fail with `exec format error` | `--rosetta` (binfmt, like Docker Desktop) |
| corporate full-tunnel **VPNs block all VM egress** | host-side CONNECT proxy (image pulls) + SNI gateway (pod HTTPS egress) + CoreDNS override |

The VPN piece deserves a sentence: VMs behind e.g. Cisco-style full-tunnel
VPNs have *no* outbound connectivity, but they can reach the host. k3c runs
a CONNECT proxy that containerd uses for image pulls, and an SNI-routing
gateway on host :443 — CoreDNS resolves your configured corporate domains to
the host, the gateway reads the TLS SNI and splices the connection to the
real destination from the host's network. TLS stays end-to-end.

## Install

```sh
brew install philipparndt/k3c/k3c
```

Or from source:

```sh
make install        # /usr/local/bin
make install-user   # GOPATH/bin, no sudo
```

## Usage

```
k3c cluster create [NAME]     k3c kubeconfig get   [NAME]
k3c cluster delete [NAME]     k3c kubeconfig merge [NAME]
k3c cluster start  [NAME]     k3c config view [NAME]
k3c cluster stop   [NAME]     k3c status [NAME]
k3c cluster pause  [NAME]     k3c version
k3c cluster resume [NAME]     k3c image import IMAGE
k3c cluster activate [NAME]   k3c cluster list
```

`activate` (alias `use`) makes a cluster current: resumes or starts it if
needed, points the public ingress/registry routing at it, and switches the
kube context. `cluster list` marks the current cluster.

`pause`/`resume` freeze the cluster VM in memory: resuming takes well under
a second and every pod keeps running — no restart cascade, no crash-loop
backoffs. A paused cluster frees CPU but keeps its memory allocated, and
does not survive a host reboot (use `stop`/`start` or snapshots for that).

Clusters run on private per-cluster host ports, so **multiple clusters can
run side by side** (RAM permitting — macOS swaps cold pages). The public
ingress (:443) and registry ports are owned by the k3c daemon and routed to
the *active* cluster — the one most recently created, started, or resumed.
Combined with `pause`/`resume`, switching between full clusters takes well
under a second. `stop`/`start` preserves cluster state.

### Snapshots

```
k3c cluster snapshot save [CLUSTER] [NAME]     # default name: timestamp
k3c cluster snapshot restore [CLUSTER] NAME
k3c cluster snapshot list [CLUSTER]
k3c cluster snapshot delete [CLUSTER] NAME
```

Snapshots APFS-clone the cluster's VM root filesystem (copy-on-write):
saving and restoring is **instant** and a snapshot costs almost no disk
space. Snapshot a fully provisioned cluster once, then reset to it in
seconds instead of reinstalling — also after destructive experiments.

A running cluster is stopped for the (sub-second) clone and restarted, so
expect a few seconds of API downtime per save. Restore requires the cluster
container to exist (the snapshot captures state, not the container).

### Local images

Two ways to run locally built images:

- **Local registry** (`localRegistry.enabled: true`): a registry container
  published on the host. Push with
  `container image push --scheme http localhost:5001/my/image:dev` and
  reference it in pods; add a `registries` mirror entry so the node reaches
  it via the vmnet gateway (see the example config).
- **`k3c image import IMAGE [CLUSTER]`**: loads an image from the host's
  `container` image store into the cluster under its original name — no
  registry involved.

### Ignoring resource requests

Charts sized for production rarely fit on a laptop. With

```yaml
cluster:
  ignoreCpuRequests: true
  ignoreMemoryRequests: true
```

k3c registers a mutating admission webhook (served by the k3c host daemon —
no in-cluster components) that replaces pod CPU/memory requests with
negligible values at creation, so everything schedules regardless of the
node size. Limits are kept. `failurePolicy: Ignore` means pods keep their
requests if the daemon is down.

## Configuration

Layered, all optional:

1. built-in defaults (vanilla k3s, no mirrors, no egress)
2. `~/.config/k3c/config.yaml` — user defaults, e.g. your corporate CA,
   registry mirrors, and egress domains
3. `./k3c.yaml` — project config (or `--config FILE` / `K3C_CONFIG`)

Set fields replace the layer below. Full example:

```yaml
cluster:
  name: dev
  contextPrefix: k3d-          # kube context = <prefix><name>
  apiHost: k3s.example.test    # TLS SAN + kubeconfig server host
  clusterCidr: 10.52.0.0/16
  serviceCidr: 10.53.0.0/16
  cpus: 8                      # default: all host cores
  memory: 16G
  extraK3sArgs: []
  sysctls:                     # node kernel parameters, merged over the
    vm.max_map_count: "262144" # defaults (raised inotify limits)

ports:
  ingress: 8444                # cluster :443 publish (fronted by SNI gateway)
  proxy: 3128

localRegistry:                 # registry:2 container for local pushes
  enabled: true
  hostPort: 5001

caCerts:                       # added to the node's registry CA bundle
  - certs/*.crt                # relative to this file

egress:
  domains: [example.com]       # pod HTTPS egress via the SNI gateway
  ingressDomains: [example.test] # ...except these: routed to the ingress

registries: |                  # verbatim k3s registries.yaml
  mirrors:
    "docker.io":
      endpoint:
        - https://mirror.example.com
  # note: k3s' containerd ignores a wildcard ("*") TLS entry here — list
  # every private-CA registry host explicitly
  configs:
    "mirror.example.com":
      tls:
        ca_file: /etc/rancher/k3s/ca-bundle.pem
```

## Bundled container runtime

Release builds of k3c can embed Apple's `container` runtime directly in the
binary, so k3c is self-contained and needs no separate `container` install.
On first use the bundled tree is extracted once to
`~/.cache/k3c/runtime/<version>/` and driven via `CONTAINER_INSTALL_ROOT`;
the guest init image is loaded automatically if missing.

A plain `go build` does **not** embed anything and drives a host-installed
`container`. To produce a bundled binary:

```sh
make bundle STAGING_DIR=/path/to/container/staging   # stage the runtime tree
make build-bundled                                   # go build -tags bundled
```

The runtime selection precedence (highest first):

| Source | Selected when |
| --- | --- |
| `K3C_CONTAINER_BINARY=/path/to/container` | always (explicit override) |
| `K3C_CONTAINER_FROM_PATH=1` (or `true`) | use `container` from `PATH` |
| `containerBinary:` in config | set to a non-default path |
| bundled runtime | embedded (release build) |
| `container` from `PATH` | fallback (dev builds) |

Use `K3C_CONTAINER_FROM_PATH=1` to ignore the bundled runtime and use a
`container` from `PATH`, or `K3C_CONTAINER_BINARY` to point at a specific one
(e.g. a local fork with pause/resume/suspend).

## Requirements

- macOS 26+ on Apple Silicon
- [Apple `container`](https://github.com/apple/container) **>= 1.0.0** —
  not required when using a bundled build
- `kubectl`

## License

Apache-2.0
