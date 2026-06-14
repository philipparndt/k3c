# Nested k3d on k3c (unmodified k3d config)

Goal: a team's existing `k3d cluster create --config <file>` flow — the same k3d
config they use on Linux — runs on macOS/k3c **with no edit to that config**, so
switching to k3c feels exactly like the old runtime and the "I can go back any
time" safety net holds.

How it works: with the k3c docker sidecar up, a Docker engine is reachable, so a
runtime-selecting wrapper (or plain `k3d`) runs the **k3d** path against the
sidecar. The k3s node is then a container nested inside the sidecar VM. Three
nesting assumptions break a stock config; k3c handles all three transparently
(its own settings live in `k3c.yaml`, never in the k3d config).

## 1. Egress + DNS — transparent egress (gvnet)

`egress.transparent: true` in `k3c.yaml` runs the sidecar on a per-VM
gvisor-tap-vsock netstack that re-originates every connection from host sockets,
so a corporate VPN/proxy carries it. The nested k3d node and pods egress and
resolve real DNS through it (no SNI gateway, no CoreDNS override, no per-domain
list). This also defuses a service-CIDR clash with a VPN-claimed range: the node
is isolated in the sidecar, ClusterIP traffic stays node-local (kube-proxy), and
egress leaves via gvnet rather than a host route the tunnel claims. Verified with
a CIDR a full-tunnel VPN otherwise black-holes: kube-dns reachable, in-cluster
and external DNS both resolve.

Netstack lifetime fix: the netstack now follows the live guest socket and
survives VM restarts (it used to latch one peer and die on `docker down`/`up`,
silently breaking egress). See `gvnet/gvnet.go` (`dynamicUnixgramConn`).

## 2. Corporate CA trust — baked into the node image

A k3d config whose mirrors point at corporate HTTPS registries with no explicit
`ca_file` relies on the node's **system** trust. The usual Linux trick injects
the CA with `--volume <cert>:/etc/ssl/certs/<name>@all`, but that host path does
not exist inside the sidecar VM, so docker bind-mounts an **empty directory** and
pulls fail with `x509: certificate signed by unknown authority`.

k3c instead **bakes** the configured `caCerts` into the node image's CA bundle on
`docker up`, for every image listed in `docker.k3sNodeImages` (idempotent, keyed
on the CA hash, retagged in place so k3d uses it without a config change). See
`cluster/nodeprep.go`.

```yaml
# k3c.yaml
caCerts:
  - certs/*.crt
docker:
  k3sNodeImages:
    - rancher/k3s:v1.33.9-k3s1   # keep in sync with the k3d config's image:
```

## 3. Native architecture — avoid the emulated-amd64 seccomp trap

An **emulated amd64** k3s node on Apple `container` breaks containerd's seccomp
detection: every pod sandbox fails with
`failed to generate seccomp spec opts: seccomp is not supported`, even though the
kernel fully supports seccomp and a fresh probe in the same node reports it
enabled. The identical image at the host's **native arm64** arch works (a native
k3c cluster never hits this). Root cause is unresolved (suspected Rosetta/
containerd interaction), so k3c sidesteps it: `nodeprep` rebuilds the node image
at the sidecar's native architecture. amd64 **workload** images still run on the
native node via the sidecar's Rosetta binfmt.

> Reproduction note: the trap is easy to hit if the sidecar's (persisted) image
> store already holds an amd64 `rancher/k3s` from earlier use — k3d then runs an
> amd64 node. `nodeprep` forces native arch, which fixes it.

## Known gap: amd64-only workload images

A native arm64 node pulls the arm64 variant of multi-arch images (all the k3s
system images, and most popular charts — these work). An **amd64-only** image
fails to pull with `no match for platform in manifest: not found`, because
containerd pulls the node's platform and there is no arm64 variant. Some macOS
runtimes paper over this by transparently pulling amd64 and emulating; stock
containerd does not. Options if a workload needs it:

- publish those images multi-arch (preferred), or
- a per-registry containerd default-platform override on the node (future
  `nodeprep` enhancement), or
- use a k3c-native cluster (`k3c cluster create`), which runs amd64 via
  `--rosetta` and is unaffected.

## Tracing

```sh
kubectl --context k3d-<cluster> get pods -A                       # overall
kubectl --context k3d-<cluster> get events -n kube-system | grep -iE 'seccomp|x509|platform'
docker --context k3c image inspect <node-image> --format '{{.Architecture}} {{index .Config.Labels "k3c.ca-injected"}}'
docker --context k3c exec <node> sh -c 'grep -c BEGIN.CERT /etc/ssl/certs/ca-certificates.crt'  # CA baked?
```
