# configuration Specification

## Purpose

Resolve k3c's configuration and report version information. This capability owns
the `--config`/`-c` global flag, `k3c config view`, `k3c version`, and the
container-runtime resolution order. The effective config drives every other
command (cluster sizing, ports, egress, pull-cache, registries, the container
binary).

## Requirements

### Requirement: Resolve and view the effective configuration

A global `--config`/`-c` flag SHALL select the config file. `k3c config view
[NAME]` SHALL print the effective configuration (project `k3c.yaml` merged with
defaults) for inspection.

#### Scenario: View effective config

- **WHEN** the user runs `k3c config view`
- **THEN** the effective configuration is printed

#### Scenario: Select a config file explicitly

- **WHEN** the user passes `--config path/to/k3c.yaml` to any command
- **THEN** that file is used as the project configuration

### Requirement: Resolve the container runtime in a fixed order

The container runtime SHALL be resolved in this order: `K3C_CONTAINER_BINARY`,
`K3C_CONTAINER_FROM_PATH`, `containerBinary` in config, the bundled runtime
embedded in release builds, then `container` on `PATH`. Release builds SHALL
extract the bundled runtime once to `~/.cache/k3c/runtime/<version>/` and drive
it with `CONTAINER_INSTALL_ROOT` pointed there.

#### Scenario: Fall back to the bundled runtime

- **WHEN** no container binary is configured or found on `PATH` in a release
  build
- **THEN** the embedded bundled runtime is extracted and used

### Requirement: Report version information

`k3c version` SHALL print the k3c version, and additionally the bundled
container runtime version when one is embedded.

#### Scenario: Show versions

- **WHEN** the user runs `k3c version`
- **THEN** the k3c version is printed, followed by the bundled container version
  if present
