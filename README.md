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
make install        # /usr/local/bin
make install-user   # GOPATH/bin, no sudo
```

## Usage

```
k3c cluster create [NAME]     k3c kubeconfig get   [NAME]
k3c cluster delete [NAME]     k3c kubeconfig merge [NAME]
k3c cluster start  [NAME]     k3c config view [NAME]
k3c cluster stop   [NAME]     k3c status [NAME]
k3c cluster list              k3c version
```

Multiple clusters are supported; only one can *run* at a time (they share
the published host ports). `stop`/`start` preserves cluster state.

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

## Requirements

- macOS 26+ on Apple Silicon
- [Apple `container`](https://github.com/apple/container) **>= 1.0.0**
  (earlier versions cannot pull images behind VPNs)
- `kubectl`

## License

Apache-2.0
