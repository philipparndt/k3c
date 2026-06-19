# pod-profiling Specification

## Purpose

Stream exact per-pod CPU and memory accounting read straight from the node's
cgroup v2 hierarchy, as a language-agnostic measurement primitive other tools
build on. This capability owns `k3c profile`. Unlike `kubectl top` (cAdvisor
stats refreshed only ~every 10s), it reads kernel accounting directly so
consumers can derive precise CPU rate and CPU-until-ready figures.

## Requirements

### Requirement: Stream per-pod accounting as JSON lines

`k3c profile [NAME]` SHALL read cgroup v2 accounting on the node — `cpu.stat`
`usage_usec` (cumulative CPU billing) and the memory working set — and write one
JSON object per sampling tick to stdout in the form
`{"t_ms":<unix-ms>,"pods":{"<pod-uid>":{"cpu_usec":N,"mem_ws":N,"mem_current":N}}}`.
`cpu_usec` SHALL be cumulative since the pod started so consumers derive rate
from the delta between ticks.

#### Scenario: Stream until interrupted

- **WHEN** the user runs `k3c profile` against a running cluster
- **THEN** one JSON object per tick is written to stdout until the command is
  interrupted

### Requirement: Configurable interval and duration

`--interval` SHALL set the sampling interval (default 500ms) and `--duration`
SHALL stop sampling after the given time (default 0 = run until interrupted).

#### Scenario: Bounded sampling at a fixed interval

- **WHEN** the user runs `k3c profile --interval 250ms --duration 10s`
- **THEN** samples are emitted every 250ms and the command exits after 10
  seconds

### Requirement: Resolve pod UIDs to names

With `--names`, each pod UID SHALL be resolved to its `namespace/name` via the
API server and added as a `name` field on the pod entry.

#### Scenario: Annotate with names

- **WHEN** the user runs `k3c profile --names`
- **THEN** each pod entry includes a `name` field set to its `namespace/name`
