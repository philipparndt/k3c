# web-ui Specification

## Purpose
TBD - created by archiving change web-ui. Update Purpose after archive.
## Requirements
### Requirement: Serve the web UI

`k3c web` SHALL start a local HTTP server that serves a self-contained system
diagram page and, unless suppressed, open it in the user's browser. The server
SHALL bind to a loopback address by default. `--port` SHALL select the listen
port; when it is unavailable the server SHALL bind a free port instead and
report the resulting URL. `--addr` SHALL override the bind address and
`--no-open` SHALL skip launching the browser. The page is a built front-end
embedded in the binary so no external files are required at runtime.

#### Scenario: Launch the web UI

- **WHEN** the user runs `k3c web`
- **THEN** a local HTTP server starts, the chosen URL is printed, and the system
  diagram page opens in the browser

#### Scenario: Requested port is busy

- **WHEN** the user runs `k3c web --port` with a port already in use
- **THEN** the server binds an available port instead and reports the URL it is
  actually listening on

#### Scenario: Do not open a browser

- **WHEN** the user runs `k3c web --no-open`
- **THEN** the server starts and prints its URL without launching a browser

### Requirement: Live system state endpoint

The server SHALL expose a `GET /api/state` endpoint returning JSON that
aggregates the current system state: the host daemon's process and listener
state, the machines (clusters and the docker sidecar) with their state, the
pull-cache statistics, and the active cluster's network traffic rate. The
endpoint SHALL compute the traffic rate from successive samples it retains
between requests, skipping a sample whose counters have reset. `GET /api/state`
SHALL read state only and SHALL NOT start, stop, or otherwise mutate any
cluster, sidecar, or daemon.

#### Scenario: Fetch current state

- **WHEN** a client requests `GET /api/state`
- **THEN** the server returns JSON containing the daemon and listener state, the
  machines and their states, the pull-cache statistics, and the network rate

#### Scenario: State endpoint does not mutate

- **WHEN** the page polls `GET /api/state`
- **THEN** no cluster, sidecar, or daemon is started, stopped, or modified by the
  poll

### Requirement: Lifecycle actions from the web UI

The web UI SHALL let the user start, pause, and stop a machine via a
`POST /api/action` endpoint that accepts the target machine and the action. The
server SHALL perform the action by executing the `k3c` binary with the
corresponding lifecycle arguments (so config resolution and logging match the
CLI). The server SHALL reject any request whose target is not a currently listed
machine or whose action is not one of the allowed lifecycle actions, and SHALL
reject non-POST requests, so the endpoint cannot be turned into an arbitrary
command. After an action the UI SHALL refresh so the diagram reflects the new
state.

#### Scenario: Start, pause, or stop a machine

- **WHEN** the user activates start, pause, or stop on a machine in the web UI
- **THEN** the server runs the matching `k3c` lifecycle command for that machine
  and the diagram refreshes to show the resulting state

#### Scenario: Reject an unknown machine or action

- **WHEN** a `POST /api/action` request names a machine that is not listed or an
  action that is not an allowed lifecycle action
- **THEN** the server rejects it with a client error and runs nothing

### Requirement: Animated data-flow diagram

The served page SHALL render the k3c components as a data-flow diagram. Each
component node SHALL be colored by its state using the shared vocabulary —
running green, paused yellow, suspended blue, stopped gray — applied to both the
node and its frame, and a host-daemon listener that is down SHALL be visually
marked. Flow indicators (animated particles) along an edge SHALL be shown only
when that edge is actually carrying data, determined from measured signals
(live network traffic for egress, a rise in pull-cache activity for image
pulls); an edge with no current activity SHALL render without particles. The
page SHALL poll the state endpoint on an interval and update the diagram so it
stays current, and SHALL retain the last good state if a poll fails.

#### Scenario: Diagram reflects live state

- **WHEN** the page is open and a component's state changes
- **THEN** on the next poll the corresponding node, frame, and status update to
  the new state

#### Scenario: Flow shown only when data flows

- **WHEN** an edge has no measured activity
- **THEN** the edge is drawn without animated particles, and particles appear
  only while real traffic or pulls are observed on that edge

#### Scenario: A poll fails

- **WHEN** a state poll fails transiently
- **THEN** the page keeps displaying the last good state rather than blanking the
  diagram

