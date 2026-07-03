## MODIFIED Requirements

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
