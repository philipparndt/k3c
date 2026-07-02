## MODIFIED Requirements

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
