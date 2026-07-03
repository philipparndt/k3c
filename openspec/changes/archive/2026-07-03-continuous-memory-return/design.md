# Continuous memory return — design

## Measured constraints (Virtualization.framework, macOS 26)

All of these were measured on a live VM; they drive the design.

1. Only the *traditional* virtio balloon exists (no free-page reporting, no
   stats queue), so continuous return must be a host-driven control loop.
   The device negotiates `DEFLATE_ON_OOM`: a squeezed guest survives, but
   deflates ~1MB per OOM-notifier call (thrashes at 300% CPU) — pressure
   relief must come from the host, fast.
2. A freshly booted VM's ballooned pages are never freed by the hypervisor.
   One suspend/restore cycle "converts" the VM; afterwards a fresh inflate
   frees host pages within ~5s.
3. Restore re-commits the whole guest memory, and only *freshly* ballooned
   pages are freed — so after every restore the balloon must be fully
   deflated and re-inflated (recycle).
4. Deflating re-commits the delta immediately; the balloon keeps its state
   across suspend/restore, and ballooned pages are not written to the saved
   state (an 8G VM squeezed to 1G suspends to a ~116MB vmstate).
5. Balloon inflation itself reclaims guest page cache — no `drop_caches`
   needed when the target is computed from `MemAvailable`.

## Controller (container fork, runtime helper)

Per-VM `AutoBalloonController` actor in `container-runtime-linux`:

- Reads whole-VM guest memory via `LinuxContainer.guestMemoryInfo()`
  (`/proc/meminfo` over the agent's copy vsock — no guest process, works
  with any deployed vminitd).
- Every 10s: `workload = total − available − balloon`;
  `target = clamp(workload + headroom, min, max)`. Shrinks only past a
  256MB hysteresis; grows immediately.
- Pressure path: `available < max(headroom/2, 256M)` → boost the target by
  `max(headroom, max/8)` at a 1s poll interval until pressure clears.
- Restore/unknown balloon state → full recycle (deflate, settle, re-inflate).
- Lifecycle: started at bootstrap (fresh boot and restore), stopped before
  suspend/pause (vsock dies with the guest), restarted on resume from the
  kept target. An explicit `memory target` pauses the policy; re-arming
  starts from the last known target to avoid multi-GB deflate churn.

## k3c integration

- Capability probe: `container memory --help` contains "policy".
- Create: `--memory-policy auto` (+ `--memory-headroom` from config) for the
  server VM and docker sidecar; after the cluster/sidecar is ready, one
  suspend/restore cycle converts the VM (constraint 2) so boot-storm memory
  returns immediately instead of at the first snapshot.
- Start / docker up on existing VMs: `container memory policy <vm> auto`
  re-arms (covers VMs created before policy support without recreating).
- Reclaim commands re-arm the policy and report the footprint; `--release`
  switches to manual and deflates. The old balloon-squeeze path stays as the
  fallback for older container builds, as does the daemon auto-reclaim loop.

## Alternatives rejected

- libkrun backend: native free-page reporting, but no warm snapshots.
- Free-page reporting on Virtualization.framework: device is closed; the
  framework exposes only the traditional balloon.
- Keeping the k3c daemon loop as the controller: 1-minute polling through
  `container exec` is too slow for the pressure path and fights the runtime
  on suspend/restore; the runtime helper owns the VM lifecycle events.

## The 16K kernel (shipped with the release)

On Apple silicon the hypervisor releases ballooned guest memory at host
page granularity (16K). A 4K guest hands the balloon scattered 4K pages,
so after real workload churn almost nothing covers a full host page and
the footprint stays at its high-water mark (measured: a 32G cluster stuck
at 36G despite an idle 18G workload). With a 16K guest kernel every
ballooned page is exactly one host page: the same cluster runs at ~19G,
footprint == dirty == workload. The kernel is built from the kata config
(k3s-complete, extracted from the previous kernel's IKCONFIG) plus 16K
pages, balloon compaction, and PSI, and ships prebuilt inside the
containerization fork (kernel builds need a running container runtime,
which bundler CI cannot provide). k3c installs it as the container
default per cluster.kernel: bundled|recommended|keep — "recommended"
(4K kata) remains for amd64 images, which need Rosetta and therefore
cannot run on 16K pages.

## Memory-number semantics (measured)

phys_footprint — what Activity Monitor shows — is a commitment ledger,
not RAM usage: it includes swapped and compressed pages and only shrinks
when the hypervisor decommits. VZ decommits ballooned pages of fresh-boot
VMs never, and of restored VMs only freshly inflated ones. In practice:
fresh/running and cold-restored clusters show the honest workload number
(the early squeeze keeps commitment from ever growing); warm restores
re-commit the configured size, and macOS trims the excess under real
memory pressure (measured: 32G resident → 5.9G under pressure while the
guest kept running). The k3c TUI should prefer resident-based numbers.
