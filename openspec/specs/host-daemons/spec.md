# host-daemons Specification

## Purpose

Run the host-side daemons that bridge VM connectivity from the host: the CONNECT
proxy, SNI gateway, pull-cache, registry forward, sidecar port-forward, and the
ignore-requests admission webhook. The daemons run as one detached process,
(re)spawned by cluster/sidecar lifecycle commands and managed via `k3c
daemons`. They must run with the project config (for the pull-cache) and the
current binary.

## Requirements

### Requirement: Daemon process lifecycle

`k3c daemons run` SHALL run the daemons in the foreground (the internal mode that
lifecycle commands spawn detached); `k3c daemons` invoked bare SHALL print help.
Running `k3c daemons run` while the daemons are already running SHALL fail with a
message pointing at `daemons restart`/`stop` rather than a raw port-bind error.
`k3c cluster` and `k3c docker` lifecycle commands SHALL (re)spawn the daemons as
needed. The daemons SHALL host the CONNECT proxy (:3128), SNI gateway (:443),
pull-cache (:5011), registry forward, sidecar port-forward, and admission
webhook.

#### Scenario: Daemons spawned on cluster start

- **WHEN** the user starts a cluster or brings the sidecar up
- **THEN** the host daemons are spawned detached if not already running

### Requirement: Manage and inspect daemons

`k3c daemons status` (aliases `list`, `ls`) SHALL show the daemons' process and
listener state. `k3c daemons restart` SHALL stop the daemons and spawn them
fresh so configuration changes take effect. `k3c daemons stop` SHALL stop the
daemons (a later cluster start spawns them again).

#### Scenario: Restart to pick up config changes

- **WHEN** the user edits the config and runs `k3c daemons restart`
- **THEN** the daemons are stopped and respawned with the new configuration

#### Scenario: Inspect listener state

- **WHEN** the user runs `k3c daemons status`
- **THEN** the daemon process state and each listener's state are printed

### Requirement: Daemons must run with the project config and current binary

The daemons SHALL run with the configuration of the invoking `k3c` command.
Running a lifecycle command from a directory without the project `k3c.yaml`
respawns the daemons without the pull-cache and breaks nested-cluster pulls;
lifecycle commands SHALL therefore be run from the project directory or with
`--config`.

#### Scenario: Stale config breaks nested pulls

- **WHEN** a lifecycle command runs from a directory lacking the project config
- **THEN** the daemons are respawned without the pull-cache, and nested-cluster
  image pulls fail until they are respawned with the project config
