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

### Requirement: Configure the terminal UI color theme

The config file SHALL support a `ui.theme` section that overrides the terminal
UI color palette, including the main/accent color and the per-role label colors
(dim, good, warn, cool, bad). Each color SHALL be optional; any unset or empty
color SHALL fall back to the built-in default. The effective theme SHALL be
included in the resolved configuration reported by `k3c config view`.

#### Scenario: Override the accent color

- **WHEN** the config file sets `ui.theme.accent` to a color and the user
  launches `k3c ui`
- **THEN** the UI renders its accent (title, selection, borders) in the
  configured color

#### Scenario: Unset colors fall back to defaults

- **WHEN** the config file sets only some theme colors (or none)
- **THEN** the unset colors use the built-in default palette

#### Scenario: Theme appears in the effective config

- **WHEN** the user runs `k3c config view`
- **THEN** the effective `ui.theme` colors are included in the printed
  configuration

### Requirement: Memory policy settings

The config SHALL support `cluster.memoryPolicy` with values `auto` (default:
the runtime sizes the VMs' memory balloons continuously, returning unused
memory to the host) and `off` (VM memory is managed manually), and
`cluster.memoryHeadroom` (a size like `1500M`) for the memory kept available
to a guest above its workload, defaulting to the runtime's built-in headroom.
`cluster.autoReclaim` SHALL remain as the fallback interval for container
builds without runtime memory-policy support (a duration like `10m`, default
`10m`, or `off`) and SHALL be ignored on policy-capable runtimes. The config
SHALL also support `cluster.memoryConvert` (`on-create` or `off`, default
`off`): with `on-create`, a freshly created VM is converted with one
suspend/restore cycle so boot-time memory returns to the host immediately;
with `off`, the first regular suspend or snapshot performs the conversion.

#### Scenario: Opting out of automatic memory management

- **WHEN** the user sets `cluster.memoryPolicy: off` and creates a cluster
- **THEN** the VMs run without the runtime's balloon controller and keep
  their configured memory resident once touched

#### Scenario: Converting boot memory at create time

- **WHEN** the user sets `cluster.memoryConvert: on-create` and creates a
  cluster on a policy-capable runtime
- **THEN** the freshly created VM is converted with one suspend/restore cycle
  so the memory touched during the k3s boot returns to the host immediately

### Requirement: Guest kernel and VM scheduling settings

The config SHALL support `cluster.kernel` selecting how the guest kernel is
managed: `bundled` (default) installs the 16K-page kernel shipped with k3c for
the best memory return but cannot run Rosetta/amd64 images, `recommended` uses
the runtime's 4K kata kernel (needed for amd64 images), and `keep` never
touches the kernel. The config SHALL support `cluster.cpuPriority` setting the
scheduling priority of the cluster VMs relative to interactive apps: `low`
(default, clamped below GUI apps) or `normal`.

#### Scenario: Run amd64 images with the recommended kernel

- **WHEN** the user sets `cluster.kernel: recommended` and creates a cluster
- **THEN** the VM runs the runtime's 4K kata kernel so amd64 images run under
  Rosetta, rather than the bundled 16K-page kernel

#### Scenario: Give the VMs normal scheduling priority

- **WHEN** the user sets `cluster.cpuPriority: normal`
- **THEN** the cluster VMs are scheduled at normal priority rather than
  clamped below interactive GUI apps

