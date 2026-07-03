# Tasks

## containerization fork (feat/auto-balloon)

- [x] `GuestMemoryInfo` type + `/proc/meminfo` parser (with tests)
- [x] `LinuxContainer.guestMemoryInfo()` over the copy vsock machinery

## container fork (feat/auto-balloon)

- [x] `ContainerConfiguration.MemoryPolicy` (mode/min/headroom), persisted
      in `Resources` with backward-compatible decoding
- [x] `AutoBalloonController` in the runtime helper (interval/pressure/
      hysteresis/recycle policy)
- [x] Lifecycle wiring: bootstrap (fresh + restore), suspend, pause/resume,
      exit cleanup; manual `memory target` pauses the policy
- [x] Re-arm carries the known balloon target (no deflate churn); unknown
      state resolves with a recycle
- [x] XPC routes `memoryPolicy`/`memoryStatus` end to end (helper, client,
      apiserver, CLI)
- [x] CLI: `--memory-policy/--memory-min/--memory-headroom` create flags,
      `container memory policy`, `container memory status`
- [x] Apiserver adopts running containers after a restart (pre-existing
      flaw: loadAtBoot stranded live VMs as "stopped")

## k3c

- [x] Config: `cluster.memoryPolicy` (auto|off, default auto),
      `cluster.memoryHeadroom`
- [x] Capability probe (`memory --help` contains "policy")
- [x] Create args + post-create conversion cycle (cluster + sidecar)
- [x] Re-arm policy on `cluster start` / `docker up` for pre-policy VMs
- [x] `reclaim` re-arms the policy (`--release` → manual + deflate);
      legacy path kept for old container builds
- [x] Daemon auto-reclaim loop disabled on policy-capable runtimes
- [x] Spec deltas (cluster-lifecycle, configuration, docker-sidecar)

## Verification (measured on claude-memtest, 8G VM)

- [x] Fresh create with policy: footprint ~460MB at idle
- [x] 3G page-cache churn recycles inside the squeezed window (no growth)
- [x] 3G anon burst: pressure deflate within ~1s ticks, no OOM kill
- [x] Converted VM: burst 7.1G → 2.3G automatically within 30s
- [x] Suspend with inflated balloon + restore: recycle drops 3.4G → 1.4G
- [x] Re-arm/manual-target/re-arm cycle keeps the target (no churn)
