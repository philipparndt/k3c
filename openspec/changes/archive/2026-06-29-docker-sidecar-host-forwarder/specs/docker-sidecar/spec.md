## MODIFIED Requirements

### Requirement: Start the sidecar

`k3c docker up` (alias `start`) SHALL start the sidecar, creating it on first
use, and activate the `k3c` docker context so the `docker` CLI and
Testcontainers target it automatically. The engine endpoint SHALL be reachable
from the host over a stable host-local endpoint — a host Unix socket and/or a
loopback (`127.0.0.1`) port published by the Apple `container` runtime — and that
reachability SHALL NOT depend on the host being able to reach the sidecar's guest
vmnet IP at L2. The forwarder backing the engine endpoint SHALL NOT dial the
guest vmnet IP when a runtime-published loopback endpoint is available.

Because a VM's resources are fixed at creation, passing `--cpus` or `--memory`
SHALL re-create an existing sidecar while preserving the image-store volume.

#### Scenario: First start creates the sidecar

- **WHEN** the user runs `k3c docker up` with no existing sidecar
- **THEN** the sidecar VM is created and started, the `k3c` docker context is
  activated, and the engine API is reachable on the host

#### Scenario: Changing resources re-creates the sidecar

- **WHEN** the user runs `k3c docker up --memory 32G` on an existing sidecar
- **THEN** the sidecar is re-created with the new memory and the image-store
  volume is preserved

#### Scenario: Engine reachable without guest vmnet L2

- **WHEN** the sidecar is running but the host cannot reach the sidecar's guest
  vmnet IP at L2 (ARP for the guest IP is incomplete / the guest vmnet NIC is
  inert)
- **THEN** `docker` and Testcontainers still reach the engine, because the engine
  endpoint is served over the runtime-published loopback port and/or host socket
  rather than the guest vmnet IP

## ADDED Requirements

### Requirement: Reach nested published ports from the host

k3c SHALL make a port published by a container started through the sidecar —
including a runtime-chosen ephemeral port, as Testcontainers does — reachable
from the host without depending on the host reaching the sidecar's guest vmnet IP
at L2. k3c SHALL forward such ports to the sidecar VM over a
stable control channel (vsock, or a statically published control port multiplexed
by an in-guest forwarding agent), and SHALL configure Testcontainers so mapped
ports resolve to a host-reachable address — surfacing them on host loopback where
possible, and setting `DOCKER_HOST` (and `TESTCONTAINERS_HOST_OVERRIDE` only when
a non-loopback host is unavoidable) accordingly.

#### Scenario: Testcontainers reaches a mapped port

- **WHEN** a Testcontainers test starts a container with an exposed port through
  the sidecar
- **THEN** the test reaches the mapped port from the host on the address
  Testcontainers resolves, even when the guest vmnet IP is not host-routable

#### Scenario: Dynamic port appears after container start

- **WHEN** a container publishes a new port at runtime after the sidecar is
  already up
- **THEN** k3c opens a host-side forward for that port over the stable control
  channel without recreating or restarting the sidecar
