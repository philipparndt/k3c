# cluster-lifecycle Specification

## Purpose

Create and manage the lifecycle of local k3s clusters that run directly inside
Apple `container` VMs (no Docker nesting). This capability owns the `k3c
cluster` command group and the workarounds that make k3s boot reliably inside a
Virtualization.framework VM on Apple Silicon.

## Requirements

### Requirement: Create a k3s cluster

`k3c cluster create [NAME]` SHALL provision a k3s server VM, wait for the node
to become Ready, and merge the resulting kubeconfig into `~/.kube/config`,
switching the current context to the new cluster. NAME SHALL default to the
configured cluster name when omitted.

The created cluster SHALL apply the Apple `container` VM workarounds required
for k3s to function: the iptables-legacy backend (no nftables in the guest
kernel), `--flannel-backend=host-gw` (no vxlan), and `kube-proxy
--masquerade-all` (no br_netfilter). amd64 images SHALL run via Rosetta binfmt.

#### Scenario: Create with defaults

- **WHEN** the user runs `k3c cluster create` with no arguments
- **THEN** a k3s server VM is started using the configured CPUs, memory, and
  image, the node reaches Ready, and the kube context `<contextPrefix><name>`
  becomes current

#### Scenario: Service CIDR clashes with a VPN-claimed range

- **WHEN** the configured service CIDR overlaps a range claimed by a
  full-tunnel VPN
- **THEN** the operator can move the service CIDR off the clash via
  `cluster.serviceCidr`, and `k3c doctor` reports the clash

### Requirement: Stop and resume cluster state

The lifecycle SHALL provide reversible state transitions that trade off restore
speed against released host resources. `stop`/`start` SHALL halt and resume a
cluster while keeping its disk state. `pause`/`resume` SHALL freeze a running
cluster in memory for instant resume with pods still running. `suspend` SHALL
save the cluster to disk and release CPU and memory, with `start` restoring it.

#### Scenario: Stop then start

- **WHEN** the user runs `k3c cluster stop` and later `k3c cluster start`
- **THEN** the cluster halts keeping its state, and on start the VM resumes and
  the node returns to Ready

#### Scenario: Pause keeps pods running on resume

- **WHEN** the user runs `k3c cluster pause` then `k3c cluster resume`
- **THEN** the cluster is frozen in memory and resumes instantly with its pods
  still running

### Requirement: Reclaim unused memory

`k3c cluster reclaim [NAME]` SHALL return memory the cluster no longer uses to
the host by ballooning the VM down to its working set. With `--release` it SHALL
deflate the balloon and restore the cluster's full configured memory.

#### Scenario: Reclaim idle memory

- **WHEN** the user runs `k3c cluster reclaim`
- **THEN** unused guest memory is returned to the host while the balloon stays
  sized to current usage

### Requirement: Activate a cluster as current

`k3c cluster activate [NAME]` (alias `use`) SHALL make a cluster current:
resume or start it, switch host-port routing to it, and switch the kube
context. Activating a cluster SHALL reclaim any contested host ports (e.g. the
:443 ingress port) from the Docker sidecar.

#### Scenario: Activate switches routing and context

- **WHEN** the user runs `k3c cluster activate other`
- **THEN** the `other` cluster is brought up, owns the shared host ports, and
  becomes the current kube context

### Requirement: Delete a cluster

`k3c cluster delete [NAME]` SHALL remove the cluster and its state. With
`--snapshots` it SHALL also delete the cluster's snapshots.

#### Scenario: Delete without snapshots

- **WHEN** the user runs `k3c cluster delete`
- **THEN** the cluster and its state are removed and its snapshots are
  preserved

### Requirement: List clusters

`k3c cluster list` (alias `ls`) SHALL list the known clusters.

#### Scenario: List clusters

- **WHEN** the user runs `k3c cluster ls`
- **THEN** the known clusters are printed
