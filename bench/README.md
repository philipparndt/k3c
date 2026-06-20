# k3c vs OrbStack benchmark suite

A local, reproducible harness that pits **k3c** against **OrbStack** using the
methodology from OrbStack's published benchmarks
(<https://docs.orbstack.dev/benchmarks>) plus a bare-cluster bring-up test.

It measures **wall-clock time** and **average CPU power** (mW, via `powermetrics`)
for each benchmark, and prints a side-by-side comparison.

## Benchmarks

| alias     | what it does | metrics |
|-----------|--------------|---------|
| `empty`   | bring up a bare cluster; time until coredns + local-path-provisioner + metrics-server are Ready (cold & warm) | `time_to_ready`, `cpu_power` |
| `helm`    | OrbStack "Battery: Kubernetes" — install Traefik + Grafana via Helm, sample steady-state power | `install_to_ready`, `cpu_power` |
| `pull`    | cold/warm image pull of a set of images **into the cluster** (exercises k3c's pull-through cache) | `pull_time`, `cpu_power` |
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

`k3c`, `orb`, `kubectl`, `helm`, `docker`, `jq`, `git`, and `powermetrics`
(built in). Power sampling needs `sudo` — the runner primes it once. Use
`--no-power` to skip.

## Caveats (read before trusting numbers)

- **Power ≠ OrbStack's exact metric.** OrbStack attributes *per-process* energy
  via kernel sampling and converts to mW. This harness samples **whole-system
  CPU power** and reports the average over the window. Run with both engines
  stopped once to get an idle baseline if you want a delta. Treat the absolute
  mW as comparable-between-engines-here, not 1:1 with OrbStack's published bars.
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
