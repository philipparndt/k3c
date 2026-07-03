## Why

A k3c VM's host memory footprint only ever grows, because pages the guest
frees stay resident on the host — over time clusters consume far more
memory than their workloads need. The existing reclaim is coarse — a manual command or a 10-minute
daemon loop that drops all guest caches and squeezes the balloon with 2GB+
headroom — so clusters bloat between runs and memory never returns
continuously. A libkrun backend would return memory natively but loses warm
snapshots, which is not an option.

## What Changes

- The container runtime fork gains an automatic memory policy: a per-VM
  controller sizes the virtio balloon continuously — the target follows the
  guest's workload plus headroom (read from `/proc/meminfo` over the agent
  connection), returning unused memory to the host within seconds and
  deflating within ~1s when the guest runs low. On restore the controller
  recycles the balloon (restore re-commits all guest memory; the hypervisor
  frees only freshly ballooned pages).
- k3c enables the policy at VM creation (`--memory-policy auto`) for cluster
  servers and the docker sidecar, re-arms it on start for VMs created before
  policy support, and converts freshly created VMs with one suspend/restore
  cycle (a freshly booted VM's ballooned pages are not freed by the
  hypervisor until the first restore).
- `k3c cluster reclaim` / `k3c docker reclaim` re-arm the runtime policy on
  policy-capable runtimes (`--release` switches to manual and deflates); the
  k3c daemon's coarse auto-reclaim loop is superseded and disabled there.
- Config: `cluster.memoryPolicy: auto|off` (default auto) and
  `cluster.memoryHeadroom` (default: the runtime's 1G).
- Snapshots are unaffected; suspend keeps the balloon inflated, so squeezed
  VMs suspend to smaller state files.

## Capabilities

### Modified Capabilities

- `cluster-lifecycle`: reclaim becomes continuous on policy-capable runtimes;
  creation enables the policy and converts the VM; start re-arms the policy.
- `configuration`: new `cluster.memoryPolicy` and `cluster.memoryHeadroom`
  settings; `cluster.autoReclaim` demoted to the legacy fallback.
- `docker-sidecar`: the sidecar gets the same policy at up/start/reclaim.

## Impact

- `~/dev/oss/containerization` (fork): `LinuxContainer.guestMemoryInfo()`
  reads whole-VM `/proc/meminfo` via the copy machinery (no guest changes).
- `~/dev/oss/container` (fork): `AutoBalloonController` in the runtime
  helper; `--memory-policy/--memory-min/--memory-headroom` create flags;
  `container memory policy|status` commands; new XPC routes; apiserver
  adopts running containers after a restart (pre-existing flaw surfaced by
  deploying this change).
- k3c: `cluster/memorypolicy.go` (new), `cluster.go`, `docker.go`,
  `reclaim.go`, `autoreclaim.go`, `config/config.go`.
