# web-ui-pods Specification

## Purpose
TBD - created by archiving change web-ui-pod-profiler. Update Purpose after archive.
## Requirements
### Requirement: List pods for a cluster

The web server SHALL expose a read-only `GET /api/pods` endpoint that returns the
pods running on a cluster as JSON (each entry carrying at least its pod UID and
`namespace/name`). The endpoint SHALL accept the target cluster as a query
parameter and SHALL default to the active cluster when none is given. It SHALL
read state only and SHALL NOT start, stop, or otherwise mutate any cluster, pod,
or daemon.

#### Scenario: Fetch the pod list

- **WHEN** a client requests `GET /api/pods` for a running cluster
- **THEN** the server returns JSON listing each pod's UID and `namespace/name`

#### Scenario: Cluster is not running

- **WHEN** a client requests `GET /api/pods` for a cluster that is not running
- **THEN** the server returns an empty pod list (or a client error) and starts
  nothing

#### Scenario: Pod list does not mutate

- **WHEN** a client requests `GET /api/pods`
- **THEN** no cluster, pod, or daemon is started, stopped, or modified

### Requirement: Stream live per-pod samples to the browser

The web server SHALL expose a read-only `GET /api/pods/stream` endpoint that
streams the per-pod CPU and memory accounting produced by the profiler to the
browser as Server-Sent Events, one event per sampling tick, where each event
carries the tick timestamp and every pod's cumulative `cpu_usec`, working-set
memory, and current memory. The server SHALL run at most one profiler stream per
cluster regardless of how many browsers are connected, and SHALL stop the
underlying profiler when the last client disconnects. The endpoint SHALL read
state only and SHALL NOT mutate any cluster, pod, or daemon.

#### Scenario: Receive live ticks

- **WHEN** a client connects to `GET /api/pods/stream` for a running cluster
- **THEN** the server emits one event per sampling tick, each containing the
  timestamp and per-pod CPU and memory accounting

#### Scenario: One profiler shared across clients

- **WHEN** multiple browsers are connected to the stream for the same cluster
- **THEN** the server runs a single profiler stream feeding all of them

#### Scenario: Profiler stops when no client remains

- **WHEN** the last client disconnects from the stream
- **THEN** the server stops the underlying profiler for that cluster

### Requirement: Per-pod CPU and memory sparklines

The web UI SHALL render, for each pod, a sparkline of its recent CPU rate and a
sparkline of its recent memory working set over the streamed window. The CPU rate
SHALL be derived from the delta of the cumulative `cpu_usec` between successive
ticks divided by the elapsed time, and a tick whose cumulative counter has reset
(a pod restart) SHALL be skipped rather than rendered as a negative spike.

#### Scenario: Sparkline reflects live samples

- **WHEN** new ticks arrive on the stream
- **THEN** each pod's CPU and memory sparklines advance to include the latest
  samples

#### Scenario: Counter reset is not a negative spike

- **WHEN** a pod's cumulative `cpu_usec` decreases between two ticks (a restart)
- **THEN** that interval is skipped rather than rendered as a negative CPU rate

### Requirement: Cluster CPU and memory heatmaps

The web UI SHALL render two heatmaps over the recent streamed window — one for
per-pod CPU rate and one for per-pod memory working set — as a grid of pods by
time whose cells are colored by intensity, so the hottest pods over the window
are visible at a glance. Each pod's row SHALL be sized (its height/area) in
proportion to that pod's share of the cluster's total computed resource — total
memory working set for the memory heatmap, total CPU rate for the CPU heatmap —
so a pod consuming more of the cluster occupies proportionally more of the
heatmap.

#### Scenario: Heatmap shows recent intensity

- **WHEN** the stream has delivered several ticks
- **THEN** the CPU and memory heatmaps show one row per pod and one column per
  recent tick, with cell color scaled to that pod's value at that tick

#### Scenario: Row size reflects share of total

- **WHEN** the heatmaps are rendered
- **THEN** each pod's row is sized in proportion to its share of the cluster's
  total computed CPU (CPU heatmap) or total memory working set (memory heatmap)

### Requirement: Pods view is reachable from the diagram

The web UI SHALL present the pods view (list, sparklines, and heatmaps) for a
cluster, reachable from that cluster's node in the existing system diagram, and
SHALL keep the diagram available. When the selected cluster is not running, the
view SHALL indicate that no pods are available rather than erroring.

#### Scenario: Open the pods view for a cluster

- **WHEN** the user selects a running cluster in the web UI
- **THEN** the pods view for that cluster is shown with its pod list, sparklines,
  and heatmaps

#### Scenario: Selected cluster is not running

- **WHEN** the user selects a cluster that is not running
- **THEN** the pods view indicates no pods are available and does not error

