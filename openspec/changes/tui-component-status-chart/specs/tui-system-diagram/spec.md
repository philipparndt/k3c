## ADDED Requirements

### Requirement: Toggle the system diagram screen

The TUI SHALL provide a dedicated full-screen system diagram view, opened with a
single keybinding and closed with the same key or `esc`. The diagram SHALL be a
separate screen layered over the main view (like the help and log screens), and
SHALL NOT alter or appear on the main machines view. At most one overlay screen
(help, log, or diagram) SHALL be open at a time.

#### Scenario: Open the diagram

- **WHEN** the user presses the diagram keybinding on the main view
- **THEN** the main machines view is replaced by the full-screen system diagram

#### Scenario: Close the diagram

- **WHEN** the user presses the diagram keybinding again or `esc` while the
  diagram is open
- **THEN** the diagram closes and the main machines view is shown

#### Scenario: Diagram keybinding is discoverable

- **WHEN** the user opens the in-app help screen
- **THEN** the diagram keybinding is listed among the general shortcuts

### Requirement: Render components as a data-flow diagram

The diagram SHALL render the k3c runtime components as labeled blocks connected
by directional flow arrows, laid out as a data-flow diagram (not a flat list).
It SHALL include blocks for the host daemon process and its active listeners,
the `container` runtime, each k3s cluster VM, the docker sidecar (when present),
and the pull-cache. Arrows SHALL indicate the direction of the flows between
components (guest egress to the host proxy/SNI, image pulls to the cache, and
host port-forwards to the sidecar).

#### Scenario: Components and flows are shown

- **WHEN** the diagram is open
- **THEN** each present component is drawn as a labeled block and the blocks are
  connected by directional arrows showing the flow between them

#### Scenario: Listener set reflects configuration

- **WHEN** the host daemon's configured listeners change (for example egress
  forwards, the admission webhook, the local registry, or the pull-cache are
  enabled or disabled)
- **THEN** the daemon block shows exactly the listeners that the host daemon is
  configured to run

### Requirement: Show live per-component status

Each block SHALL show a live health status using the TUI's existing state
vocabulary and colors: VM components SHALL use the running / paused / suspended
/ stopped / unknown states, and daemon listeners SHALL be shown as up or down.
The statuses SHALL refresh on the TUI's existing periodic refresh while the
diagram is open, without the user re-opening the screen.

#### Scenario: Status reflects current state

- **WHEN** a component's state changes (for example a listener stops accepting
  connections or a cluster VM is paused) while the diagram is open
- **THEN** on the next refresh the corresponding block's status indicator
  updates to the new state

#### Scenario: A down listener is visually distinct

- **WHEN** a configured listener is not accepting connections
- **THEN** its entry in the daemon block is marked down and visually
  distinguished from the listeners that are up

### Requirement: Programmatic daemon state for the diagram

The host-daemon status logic SHALL expose the process state (running or stopped,
pid, spawned version) and the per-listener state (name, port, detail, and
up/down) through a programmatic accessor that the TUI consumes directly, without
shelling out or capturing command output. The textual `k3c daemons status`
output SHALL remain unchanged.

#### Scenario: TUI reads daemon state directly

- **WHEN** the TUI builds the diagram
- **THEN** it obtains the daemon process and listener state from the
  programmatic accessor rather than by running `k3c daemons status`

#### Scenario: CLI output is preserved

- **WHEN** the user runs `k3c daemons status`
- **THEN** the printed process and listener state is the same as before this
  change
