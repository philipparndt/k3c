# cluster-diagnostics Specification

## Purpose

Diagnose the host, daemons, egress, and cluster health, and provide shells for
debugging from a pod's perspective — including into distroless/scratch
containers that ship no shell. This capability owns `k3c doctor` and `k3c
status`.

## Requirements

### Requirement: Read-only health diagnosis

`k3c doctor [CLUSTER]` SHALL run read-only checks across the host, daemons,
egress, and cluster health, including CIDR clashes, connectivity, runtime, and
CoreDNS.

#### Scenario: Run diagnostics

- **WHEN** the user runs `k3c doctor`
- **THEN** read-only checks are run and their results reported, with no changes
  made to the host or cluster

### Requirement: Interactive in-cluster debug shell

With `--shell`, `k3c doctor` SHALL start a debug pod (default
`nicolaka/netshoot`, digest-pinned, overridable with `--image`) and open an
interactive shell in it for testing DNS, egress, and service routing from a
pod's perspective. `--rm` SHALL remove the debug pod (on shell exit with
`--shell`, or standalone).

#### Scenario: Open a debug shell

- **WHEN** the user runs `k3c doctor --shell`
- **THEN** a netshoot debug pod is started and an interactive shell is opened in
  it

#### Scenario: Remove the debug pod on exit

- **WHEN** the user runs `k3c doctor --shell --rm`
- **THEN** the debug pod is removed when the shell exits

### Requirement: Attach a debug container to a running pod

With `--attach POD`, `k3c doctor` SHALL inject an ephemeral debug container
(default netshoot) into a running pod, sharing the target container's process
namespace, and open a shell inside the target's filesystem (`/proc/1/root`) with
netshoot's tools on PATH. `-n`/`--namespace` SHALL select the pod's namespace
(default `default`) and `--container` SHALL select the target container (default
the pod's first container). This SHALL be the way to get a shell into a
distroless/scratch container.

#### Scenario: Shell into a distroless pod

- **WHEN** the user runs `k3c doctor --attach mypod -n prod`
- **THEN** an ephemeral debug container is injected into `mypod` in namespace
  `prod`, sharing the target's process namespace, and a shell opens inside the
  target's filesystem with netshoot's tools available

### Requirement: Status overview

`k3c status [NAME]` SHALL show cluster, daemon, and node status.

#### Scenario: Show status

- **WHEN** the user runs `k3c status`
- **THEN** the cluster, daemon, and node status are printed

### Requirement: Environment info

`k3c info` SHALL print an at-a-glance summary of the k3c environment: the k3c
version, the resolved container runtime (which binary, why it was selected,
and its CLI version), the bundled runtime version if any, and where
configuration is read from (state directory, user and project config, the
container binary, and the active cluster and kube context). It SHALL be
read-only and SHALL NOT start the container system, so it stays useful for
diagnostics when the daemon is down. `--json` SHALL emit the same information
as machine-readable JSON.

#### Scenario: Show environment info

- **WHEN** the user runs `k3c info`
- **THEN** the k3c version, resolved container runtime and its CLI version, and
  the configuration in use are printed without starting the container system

#### Scenario: Machine-readable info

- **WHEN** the user runs `k3c info --json`
- **THEN** the same version, runtime, and configuration details are emitted as
  JSON
