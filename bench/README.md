# k3c vs OrbStack benchmark suite

A local, reproducible harness that pits **k3c** against **OrbStack** using the
methodology from OrbStack's published benchmarks
(<https://docs.orbstack.dev/benchmarks>) plus a bare-cluster bring-up test.

It measures **wall-clock time** and **per-engine energy impact** (the macOS
process-energy metric, sampled **without sudo**) for each benchmark, and prints
a side-by-side comparison.

Engines — native providers **k3c**, **OrbStack** (`orb`), **Rancher Desktop**
(`rancher`), **colima** — plus **k3d** on each provider's Docker (`orb-k3d`,
`rancher-k3d`, `colima-k3d`). k3d isn't a runtime, it's k8s-in-Docker, so it's
named per backend. Providers are mutually exclusive (each owns a VM / host
ports); the runner quiesces the others before each engine's phase.

## Go version (preferred)

The suite has been ported to Go (a nested module, isolated from the product
build). It is the primary implementation; the shell scripts remain until the
Go port's rd/k3d/pull/helm paths are validated live.

```bash
cd bench
make full        # build + run every engine & benchmark (the full bench)
make quick       # fast sanity: k3c vs orb, empty cold, no power
make summary     # print the table   |   make open  -> build+open the HTML report
make help        # all targets; override e.g.  make full ENGINES=k3c,orb ITER=5 POWER=false
```

Or drive the binary directly:

```bash
go build -o k3cbench .          # or: go run .

# run + accumulate (results append to results/store.jsonl)
./k3cbench -engines k3c,orb -benchmarks empty,resume,helm -iterations 3
# pull is opt-in (Docker Hub rate-limits cold pulls): make pull  /  -benchmarks pull

# INCREMENTAL: add an engine later, or more rounds — just run again; the
# append-only store accumulates and every summary/report means over all of it.
./k3cbench -engines k3d -benchmarks empty            # adds k3d to the same store
./k3cbench -engines k3c -benchmarks empty            # adds another round

./k3cbench -summary                                  # print table from the store
./k3cbench -report                                   # (re)generate results/report.html
```

Flags: `-engines k3c,orb,rd,k3d` · `-benchmarks empty,resume,helm,pull` (pull opt-in) ·
`-variants cold,warm` · `-iterations N` · `-power` / `-power=false` ·
`-power-window SECS` · `-ready-timeout SECS` · `-store PATH` · `-fresh`
(truncate) · `-report` · `-summary`. Pull-cache: set `K3C_CONFIG=$PWD/configs/k3c-pullcache.yaml`.

Every lifecycle command is logged (`exec: …`) so a run is fully auditable.

## Benchmarks

| alias     | what it does | metrics |
|-----------|--------------|---------|
| `empty`   | bring up a bare cluster; time until coredns + local-path-provisioner + metrics-server are Ready (cold & warm) | `time_to_ready`, `cpu_power` |
| `helm`    | OrbStack "Battery: Kubernetes" — install Traefik + Grafana via Helm, sample steady-state power | `install_to_ready`, `cpu_power` |
| `pull`    | cold/warm image pull of a set of images **into the cluster** (exercises k3c's pull-through cache) | `pull_time`, energy |
| `edx`     | OrbStack's Open edX heavy build — clone devstack, `make pull` then time `make dev.provision` (docker providers only) | `provision_time`, energy |
| `build`   | OrbStack "Heavy Build: PostHog" — clone + `docker build` for arm64 then amd64 | `build_time`, `cpu_power` |
| `compose` | OrbStack "Battery: Supabase/Sentry" — `docker compose up` a stack, sample steady-state power | `time_to_up`, `cpu_power` |

## Usage

```bash
cd bench

# quick: bare-cluster bring-up, both engines, no power (no sudo)
./run.sh --benchmarks empty --no-power

# the OrbStack-comparable k8s set with power
./run.sh --engines k3c,orb --benchmarks empty,helm,pull --iterations 3

# everything (heavy: builds + compose pull big repos)
./run.sh --benchmarks empty,helm,pull,build,compose --power-window 600
```

Flags: `--engines k3c,orb` · `--benchmarks …` · `--variants cold,warm` ·
`--iterations N` · `--power-window SECS` (OrbStack used 600) · `--no-power` ·
`--ready-timeout SECS`.

Results land in `results/<run-id>/` (`results.jsonl` + `env.json`); a mean
comparison table prints at the end.

## Host port 443 is exclusive

OrbStack and k3c **both bind host `:443`** (and `:80`), so they cannot run at the
same time. The runner enforces this: before each engine's phase it stops the
other engine (`orb stop`, or for k3c `cluster delete` + `daemons stop`).

> ⚠️ The k3c quiesce stops the **shared host daemons**, which serve *every* k3c
> cluster — including any persistent one you use day-to-day. After a run that
> includes `orb`, restore them with `k3c daemons restart` (or restart that
> cluster). Stop any unrelated k3c clusters before benchmarking to avoid surprise.

## Requirements

`k3c`, `orb`, `kubectl`, `helm`, `docker`, `git` — and `go` for the Go version.
Energy sampling uses macOS `top` (built in, **no sudo**). Disable with
`-power=false`. (The legacy shell suite additionally used `jq`/`powermetrics`.)

## Caveats (read before trusting numbers)

- **Energy is per-engine "Energy Impact" (EI), sudo-free.** Like OrbStack, we
  attribute energy to the engine's own host processes (matched by command
  substring; see `EnergyPatterns` per engine) using the macOS process-energy
  metric via `top -stats power` — no root, independent of unrelated machine load.
  EI is a relative kernel score (Activity Monitor's metric), **not Watts**;
  directly comparable across engines on this machine, not 1:1 with OrbStack's
  published mW bars. Two caveats: (1) **k3d runs inside OrbStack's VM**, so its
  EI is the OrbStack VM process — k3d and orb are not separable; (2) the k3c
  matcher catches *all* k3c clusters' processes, so for clean numbers run only
  the cluster under test (stop any persistent one).
- **Cold/warm are defined per engine** (`lib/engine_*.sh`):
  - k3c cold = delete cluster + `pull-cache clear`; warm = pre-create once. Each
    run is a genuinely fresh VM + cluster.
  - orb has ONE persistent cluster + shared image store and no non-destructive
    wipe (`orbctl reset` destroys everything, so we don't), so orb cold ≈ warm:
    both measure a k8s **stop+start**, not a from-scratch cluster. The suite does
    **not** prune OrbStack's images.
- **Addon set differs by engine.** OrbStack's cluster ships only coredns +
  local-path-provisioner; k3s also runs metrics-server. So `empty_cluster` times
  each engine's own addons (k3c waits for 3, orb for 2). For a strictly
  apples-to-apples number, force a common set:
  `EMPTY_ADDONS="coredns local-path-provisioner" ./run.sh --benchmarks empty`.
- **Empty-cluster addons are bundled** in the k3s node image (airgap tar), so
  the cold/warm delta there is VM boot + k3s start, *not* registry pulls. The
  pull path is what `pull` measures.
- **To test k3c's pull cache**, enable it in your k3c config
  (`pullCache.enabled: true`) so the node's `registries.yaml` points at it;
  otherwise `pull` measures direct pulls.
- **Builds/compose are heavy** and clone large repos at pinned commits; expect
  long first runs and network/emulation (amd64-on-arm) effects. Override the
  targets via `BUILD_REPO`/`BUILD_REF`/`COMPOSE_REPO`/… env vars.

## Layout

```
bench/
  run.sh                 orchestrator + summary
  lib/
    common.sh            logging, timing, JSON results, kubectl/readiness helpers
    power.sh             powermetrics CPU-power sampling
    engine_k3c.sh        k3c adapter (cluster create/delete, docker sidecar)
    engine_orb.sh        OrbStack adapter (orb start/stop k8s, docker context)
  benchmarks/
    empty_cluster.sh  helm_workload.sh  image_pull.sh  docker_build.sh  compose_stack.sh
```
