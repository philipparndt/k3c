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

By default (`egress.transparent` unset or true, or `K3C_TRANSPARENT_EGRESS=1`),
each VM SHALL gain a second gvnet NIC backed by a per-VM userspace netstack. The
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
