# docker-sidecar Specification

## Purpose

Provide a real Docker Engine API on Apple `container` via a managed
`docker:dind` VM ("the sidecar"), so the `docker` CLI, Testcontainers, and k3d
work without Docker Desktop. This capability owns the `k3c docker` command
group. The sidecar's image store lives on a volume that survives recreation.
## Requirements
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

### Requirement: Expose the engine to shells and CI

`k3c docker env` SHALL print shell exports (e.g. `DOCKER_HOST`) for the sidecar
engine, suitable for `eval $(k3c docker env)`. `k3c docker activate` SHALL make
the sidecar the active target, taking ownership of host ports it shares with
the active cluster (contested ports such as :443 ingress).

The published Engine API socket SHALL behave as a standard Docker Engine
endpoint for concurrent and streaming traffic: an open streaming response body
(e.g. `GET /containers/{id}/logs`), including one that is not being drained by
the client (back-pressured), SHALL NOT head-of-line-block other Engine API
requests on the socket. Hijacked interactive streams (`docker exec`/`attach`)
and archive uploads (`docker cp`, `PUT /containers/{id}/archive`) SHALL continue
to work. As a result, standard Docker clients and Testcontainers — including
`wait.ForLog`-based readiness (the Redpanda and Kafka modules) — SHALL work
unchanged against the socket.

#### Scenario: Use the engine via an env var

- **WHEN** the user runs `eval $(k3c docker env)`
- **THEN** the shell's `docker` client targets the sidecar engine

#### Scenario: A back-pressured log stream does not block other requests

- **WHEN** a client opens a `follow=false` log stream on a container and, without
  draining it, issues another Engine API request (e.g. `ContainerInspect`) on the
  same engine socket
- **THEN** the second request completes promptly rather than stalling until timeout

#### Scenario: Testcontainers readiness works

- **WHEN** a Testcontainers module whose readiness uses `wait.ForLog` (e.g.
  Redpanda or Kafka) is started against `DOCKER_HOST` pointing at the sidecar socket
- **THEN** the module reaches readiness instead of timing out

### Requirement: Lifecycle and resource reclaim

The sidecar SHALL support the same state transitions as native clusters:
`down`/`stop` (keep image store), `pause`/`resume` (freeze in memory),
`suspend` (to disk, releasing CPU and memory), and `reclaim` (return unused
memory, `--release` for full configured memory). On container builds with
runtime memory-policy support, the sidecar SHALL be created with the
automatic memory policy (footprint follows the dind workload), converted
with one suspend/restore cycle after `up`, and re-armed on start; `reclaim`
re-arms the policy. `k3c docker rm` SHALL remove the sidecar container so
`up` re-creates it, keeping the image-store volume unless `--volume` is
given. `k3c docker status` SHALL show the sidecar state and endpoint.

#### Scenario: Remove keeps the image store

- **WHEN** the user runs `k3c docker rm`
- **THEN** the sidecar container is removed and the image-store volume is
  kept so a later `up` re-creates the sidecar with its images intact

#### Scenario: Remove with volume deletes all data

- **WHEN** the user runs `k3c docker rm --volume`
- **THEN** the sidecar container and its image-store volume are both removed

#### Scenario: Sidecar memory follows the workload

- **WHEN** a nested k3d cluster or image build finishes on a policy-capable
  runtime
- **THEN** the sidecar VM's host footprint returns to the remaining workload
  plus headroom within seconds

### Requirement: BuildKit builder that works under k3c

`k3c docker buildkit [BUILDER]` SHALL create or re-create a docker-container
buildx builder in the sidecar that trusts the cluster CA and routes egress
through the k3c proxy, so `docker buildx` builds resolve hosts and accept
registry certificates despite the sidecar's TLS-intercepted, DNS-less egress.
BUILDER SHALL default to `multi-platform`.

#### Scenario: Provision the builder

- **WHEN** the user runs `k3c docker buildkit`
- **THEN** a buildx builder named `multi-platform` is created in the sidecar
  with cluster-CA trust and proxy egress, so `docker buildx` builds succeed

### Requirement: Prepare node images for nested k3d

`k3c docker prepare-k3d` SHALL pull each image in `docker.k3sNodeImages`, bake
the corporate CA into its trust store, and rebuild it at the sidecar's native
architecture under the same tag, so nested `k3d cluster create` reuses it
without config changes. The result SHALL be cached and re-running SHALL be a
no-op until the CA or configured images change.

#### Scenario: Prepare before nested k3d

- **WHEN** the user runs `k3c docker prepare-k3d`
- **THEN** the configured k3s node images are rebuilt with the corporate CA at
  native architecture and cached for k3d to reuse

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

