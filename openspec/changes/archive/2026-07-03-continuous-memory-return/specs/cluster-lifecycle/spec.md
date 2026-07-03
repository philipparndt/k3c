## MODIFIED Requirements

### Requirement: Reclaim unused memory

On policy-capable container builds, the cluster's VMs SHALL return unused
memory to the host continuously: the runtime sizes the memory balloon to the
guest's workload plus headroom, and deflates promptly when the guest runs
low. `k3c cluster reclaim [NAME]` SHALL re-arm the
runtime policy (after a manual override or with `memoryPolicy: off`) and
report the footprint. With `--release` it SHALL switch the VM to manual
memory management and restore the full configured memory.

On older container builds (balloon support only), `k3c cluster reclaim`
SHALL keep the previous behavior: balloon the VM down to its working set
once, `--release` deflates.

#### Scenario: Memory returns continuously

- **WHEN** a workload's memory demand drops on a policy-capable runtime
- **THEN** the host footprint follows it down within seconds, without any
  k3c command

#### Scenario: Reclaim re-arms the policy

- **WHEN** the user runs `k3c cluster reclaim` after a manual memory target
- **THEN** the runtime's automatic policy is re-armed and the footprint
  settles at the workload plus headroom

## ADDED Requirements

### Requirement: Cluster VMs are created with automatic memory management

`k3c cluster create` SHALL create the server VM with the runtime's automatic
memory policy enabled when `cluster.memoryPolicy: auto` (the default) on a
policy-capable container build. With `cluster.memoryConvert: on-create` it
SHALL additionally convert the freshly created VM with one
suspend/restore cycle, so memory touched during the k3s boot returns to the
host immediately (the hypervisor frees ballooned pages only for VMs
restored from saved state; without the conversion, the first regular
suspend or snapshot converts). A failed post-conversion restore SHALL fail
the create visibly. `k3c cluster start` SHALL re-arm the policy on VMs
created before policy support.

#### Scenario: Create leaves a lean cluster

- **WHEN** the user creates a cluster on a policy-capable runtime
- **THEN** after creation the VM's host footprint reflects the k3s workload
  plus headroom, not the boot-time peak

#### Scenario: Existing clusters benefit without recreation

- **WHEN** the user starts a cluster whose VM was created before memory
  policy support
- **THEN** the runtime's automatic memory policy is armed for that run
