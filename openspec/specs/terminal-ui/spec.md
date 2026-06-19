# terminal-ui Specification

## Purpose

Provide an interactive terminal UI for managing clusters, snapshots, and
lifecycle operations without remembering individual commands. This capability
owns `k3c ui`.

## Requirements

### Requirement: Interactive lifecycle management

`k3c ui` SHALL open an interactive terminal UI for viewing clusters and
snapshots and driving lifecycle operations (create, start, stop, delete, and
related transitions).

#### Scenario: Launch the UI

- **WHEN** the user runs `k3c ui`
- **THEN** an interactive terminal UI opens listing clusters and snapshots and
  offering lifecycle actions

### Requirement: Live traffic display

The UI SHALL display live traffic information, showing both the current rate and
the total traffic.

#### Scenario: Observe traffic

- **WHEN** traffic flows while the UI is open
- **THEN** the UI shows the current rate alongside the cumulative total
