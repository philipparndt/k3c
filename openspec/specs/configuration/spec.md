# configuration Specification

## Purpose

Resolve k3c's configuration and report version and status information. This
capability owns the `--config`/`-c` and `--json` global flags, the
configuration file layering and precedence, environment-variable overrides,
`k3c config view`, `k3c info`, `k3c status`, `k3c version`, and the
container-runtime resolution order. The effective config drives every other
command (cluster identity and sizing, CIDRs, ports, egress, pull-cache,
registries, the container binary). Behavior for individual feature sections
(`egress.*`, `pullCache.*`, `docker.*`, memory/kernel) is owned by the
respective capability; this capability owns how those settings are resolved and
reported.
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

### Requirement: Layer configuration files by precedence

The effective configuration SHALL be assembled by layering, from lowest to
highest precedence: (1) built-in defaults, (2) the per-user file
`~/.config/k3c/config.yaml` (under `$XDG_CONFIG_HOME` when set), (3) the project
config selected by `--config`/`K3C_CONFIG` or, when neither is given, `./k3c.yaml`
in the working directory, and, when no project config is found for a named
cluster, (4) the per-cluster config persisted at create time under
`<stateDir>/clusters/<name>/k3c.yaml`. A field set in a higher layer SHALL
replace the same field in the layer below entirely; list-valued fields SHALL NOT
be merged element-wise. An implicit `./k3c.yaml` SHALL apply only when it is not
overridden explicitly and either no cluster is named or its `cluster.name`
matches the requested cluster.

#### Scenario: Project config overrides the user defaults

- **WHEN** `~/.config/k3c/config.yaml` sets `cluster.memory: 4G` and a
  `./k3c.yaml` sets `cluster.memory: 12G`
- **THEN** the effective memory is `12G`, because the project layer replaces the
  user layer

#### Scenario: Named cluster resolves its persisted config

- **WHEN** a command targets a named cluster from a directory with no project
  `k3c.yaml`
- **THEN** the cluster's config persisted at create time under
  `<stateDir>/clusters/<name>/k3c.yaml` is used

### Requirement: Environment variable overrides

The following environment variables SHALL override configuration or paths:
`K3C_CONFIG` (project config path), `K3C_BASE_DIR` (state directory),
`XDG_CONFIG_HOME` (base of the per-user config dir), `K3C_TRANSPARENT_EGRESS`
(truthy forces `egress.transparent` on), and the runtime-resolution variables
`K3C_CONTAINER_BINARY` / `K3C_CONTAINER_FROM_PATH` (see the runtime-resolution
requirement above). `K3C_LOG_LEVEL` SHALL set log verbosity.

#### Scenario: Force transparent egress from the environment

- **WHEN** `K3C_TRANSPARENT_EGRESS=1` is set
- **THEN** transparent egress is enabled regardless of the `egress.transparent`
  value in the config files

### Requirement: Cluster identity, image, and networking settings

The config SHALL support the settings that identify and size a native cluster:
`cluster.name` (default `k3c`, from which the server VM name, registry VM name,
and kube context `<contextPrefix><name>` are derived), `cluster.contextPrefix`
(default `k3c-`), `cluster.image` (the k3s image, with a pinned default),
`cluster.apiHost` (default `127.0.0.1`, used as a TLS SAN and the kubeconfig
server host), `cluster.cpus` (default: host CPU count), `cluster.memory`
(default `8G`), `cluster.clusterCidr` (pod CIDR, default `10.42.0.0/16`), and
`cluster.serviceCidr` (service CIDR, default `10.43.0.0/16`). The service and
pod CIDRs SHALL be movable so an operator can steer them off a range claimed by
a full-tunnel VPN (see [[cluster-lifecycle]]).

#### Scenario: Move the service CIDR off a VPN-claimed range

- **WHEN** the operator sets `cluster.serviceCidr` to a range the VPN does not
  claim and creates the cluster
- **THEN** the cluster uses that service CIDR

### Requirement: Node tuning settings

The config SHALL support `cluster.extraK3sArgs` (a list appended verbatim to the
`k3s server` command line, later entries winning) and `cluster.sysctls` (a map
of guest kernel parameters merged over k3c's defaults, which raise the inotify
instance and watch limits). It SHALL support `cluster.ignoreCpuRequests` and
`cluster.ignoreMemoryRequests` (see [[admission-overrides]]).

#### Scenario: Append an extra k3s server argument

- **WHEN** `cluster.extraK3sArgs` includes an argument
- **THEN** that argument is appended to the `k3s server` command line

### Requirement: Host port, registry, and CA settings

The config SHALL support `ports.ingress` (host port publishing the cluster's
:443 ingress, default `8444`) and `ports.proxy` (host CONNECT proxy port,
default `3128`); `localRegistry.enabled` / `localRegistry.hostPort` (default
`5001`) for an optional dev registry (see [[registry-and-pull-cache]]);
`caCerts` (a list of globs, resolved relative to the declaring config file,
appended to the node's registry CA bundle); a verbatim `registries` block
(k3s `registries.yaml`, into which the pull-cache mirror is injected); and
`containerBinary` (the Apple `container` CLI path, overridden at runtime by
`K3C_CONTAINER_BINARY`). The `egress.*`, `pullCache.*`, and `docker.*` sections
SHALL be settable here; their behavior is owned by [[host-egress]],
[[registry-and-pull-cache]], and [[docker-sidecar]] respectively.

#### Scenario: Add a corporate CA to the node trust bundle

- **WHEN** `caCerts` lists a glob matching a corporate CA PEM relative to the
  config file
- **THEN** that CA is appended to the node's registry CA bundle

### Requirement: Machine-readable output

A global `--json` flag SHALL make the read-only reporting commands (`k3c info`,
`k3c version`, `k3c config view`, `k3c status`) emit machine-readable JSON
instead of human-formatted tables; `k3c profile` SHALL always emit JSON lines.
`k3c config view` SHALL print a curated view of the effective configuration for
inspection rather than a verbatim dump of every internal field.

#### Scenario: Read the effective config as JSON

- **WHEN** the user runs `k3c config view --json`
- **THEN** the curated effective configuration is printed as JSON suitable for
  scripting

