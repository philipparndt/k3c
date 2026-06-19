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
from the host at the sidecar's vmnet IP on the configured engine port (default
2375).

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

### Requirement: Expose the engine to shells and CI

`k3c docker env` SHALL print shell exports (e.g. `DOCKER_HOST`) for the sidecar
engine, suitable for `eval $(k3c docker env)`. `k3c docker activate` SHALL make
the sidecar the active target, taking ownership of host ports it shares with
the active cluster (contested ports such as :443 ingress).

#### Scenario: Use the engine via an env var

- **WHEN** the user runs `eval $(k3c docker env)`
- **THEN** the shell's `docker` client targets the sidecar engine

### Requirement: Lifecycle and resource reclaim

The sidecar SHALL support the same state transitions as native clusters:
`down`/`stop` (keep image store), `pause`/`resume` (freeze in memory),
`suspend` (to disk, releasing CPU and memory), and `reclaim` (return unused
memory, `--release` for full configured memory). `k3c docker rm` SHALL remove
the sidecar container so `up` re-creates it, keeping the image-store volume
unless `--volume` is given. `k3c docker status` SHALL show the sidecar state
and endpoint.

#### Scenario: Remove keeps the image store

- **WHEN** the user runs `k3c docker rm`
- **THEN** the sidecar container is removed and the image-store volume is kept
  so a later `up` re-creates the sidecar with its images intact

#### Scenario: Remove with volume deletes all data

- **WHEN** the user runs `k3c docker rm --volume`
- **THEN** the sidecar container and its image-store volume are both removed

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
