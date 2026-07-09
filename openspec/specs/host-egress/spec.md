# host-egress Specification

## Purpose

Give cluster and sidecar VMs working outbound connectivity even behind a
corporate full-tunnel VPN, where the VMs themselves have no direct internet and
cannot resolve corporate DNS. All egress is bridged from the host, where DNS,
CA trust, and egress work. Two modes are supported: the default transparent
(gvnet) path and a legacy proxy/SNI path selected with `egress.transparent:
false`.

## Requirements

### Requirement: Default egress via transparent gvnet

Each VM SHALL, by default (`egress.transparent` unset or true, or
`K3C_TRANSPARENT_EGRESS=1`), gain a second gvnet NIC backed by a per-VM
userspace netstack. The
guest default route and `/etc/resolv.conf` SHALL be repointed at the gvnet
gateway, and the netstack SHALL re-originate every outbound connection from a
host socket so the VPN carries it. In this mode there SHALL be no SNI gateway,
no CoreDNS override, and no `HTTP_PROXY`; pods resolve real DNS and connect
directly.

#### Scenario: Default cluster uses transparent egress

- **WHEN** the operator brings the cluster up without setting
  `egress.transparent` (or sets it true)
- **THEN** each VM gets a gvnet NIC, its default route and DNS point at the
  gvnet gateway, and pod egress is re-originated from the host through the VPN

#### Scenario: vmnet stays primary for host reachability

- **WHEN** a VM runs in transparent-egress mode
- **THEN** vmnet remains the primary NIC so published ports and `containerIP`
  stay host-routable, while the gvnet NIC carries egress

### Requirement: Legacy egress via CONNECT proxy and SNI gateway

When `egress.transparent` is false, image pulls SHALL traverse a host-side
CONNECT proxy that the guest reaches via `HTTP_PROXY` at the host gateway, and
pod HTTPS egress SHALL traverse a host-side SNI gateway. CoreDNS SHALL be
overridden to answer configured egress domains with the host gateway address so
pod :443 traffic is routed to the SNI gateway, which reads the ClientHello SNI
and dials the real host through the VPN.

#### Scenario: Select the legacy proxy/SNI path

- **WHEN** the operator sets `egress.transparent: false` and brings the cluster
  up
- **THEN** the VM runs without a gvnet NIC, image pulls go through the host
  CONNECT proxy, and pod :443 traffic is routed via the CoreDNS override to the
  SNI gateway

#### Scenario: Pod reaches an external HTTPS host

- **WHEN** a pod connects to a configured egress domain on :443 in legacy mode
- **THEN** CoreDNS answers with the host gateway, the SNI gateway reads the SNI
  and dials the real host through the VPN, and the connection succeeds

#### Scenario: Image pull through the CONNECT proxy

- **WHEN** containerd pulls an image in legacy mode
- **THEN** the pull is sent to the host CONNECT proxy and egresses through the
  VPN to the registry

### Requirement: Configurable egress domains, ports, and forwards

The egress configuration SHALL accept `egress.domains` (external domains
reachable from pods), `egress.ingressDomains` (domains routed to the cluster
ingress instead of egressing), `egress.ports`, and `egress.forwards`.

#### Scenario: Route a domain to cluster ingress

- **WHEN** a domain is listed in `egress.ingressDomains` in legacy mode
- **THEN** the SNI gateway routes that domain to the cluster ingress instead of
  egressing it

### Requirement: Per-VM gvnet netstack process lifecycle

In transparent-egress mode each VM SHALL be backed by its own userspace netstack
process, spawned detached (surviving the invoking `k3c` process) before the VM's
`run`/`start` and exiting when its VM disconnects. k3c SHALL (re)spawn the
netstack when its pidfile is dead or its socket is missing, and the netstack
SHALL survive a VM restart that reconnects on a new NIC socket. Each VM's gvnet
network SHALL be allocated a distinct, non-overlapping `/24` (starting at
`192.168.127.0/24` and counting up), because the runtime rejects overlapping
subnets.

#### Scenario: Netstack respawned when its socket is gone

- **WHEN** a VM is started and its per-VM netstack pidfile is dead or the socket
  is missing
- **THEN** k3c spawns a fresh detached netstack for that VM before the VM comes
  up

#### Scenario: Two VMs get distinct gvnet subnets

- **WHEN** two clusters (or a cluster and the sidecar) run with transparent
  egress at once
- **THEN** each VM's gvnet network is assigned a distinct non-overlapping `/24`

### Requirement: Guest can still reach host daemons under transparent egress

The gvnet gateway SHALL NAT to host loopback so that — even though the netstack
re-originates outbound connections from the host — the guest can still reach the
host-side daemons (pull-cache, registry forward, admission webhook) at the
gateway address. The guest's node IP SHALL remain pinned to the vmnet NIC so the
host can always reach the kubelet/API even while gvnet owns the default route.

#### Scenario: Pull-cache reachable under transparent egress

- **WHEN** a cluster runs with transparent egress and containerd pulls via the
  configured mirror
- **THEN** the pull reaches the host pull-cache at the gateway address, because
  the gvnet gateway NATs to host loopback
