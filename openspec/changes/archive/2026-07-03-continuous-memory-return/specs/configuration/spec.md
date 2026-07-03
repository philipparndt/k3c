## ADDED Requirements

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
