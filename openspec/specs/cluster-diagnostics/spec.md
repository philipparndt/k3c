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
