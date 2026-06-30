## ADDED Requirements

### Requirement: Extraction matches the running binary's payload

The bundled runtime extracted to the host cache SHALL correspond to the payload
embedded in the currently running k3c binary. The extraction's identity — its
cache directory and/or its `.complete` marker — SHALL incorporate the k3c
version (or a content hash of the embedded payload), not only the bundled
container-runtime version. When a previously completed extraction does not match
the running binary's payload identity, k3c SHALL discard it and perform a clean
re-extraction before resolving any runtime binary.

#### Scenario: Upgrade that changes only helper binaries

- **WHEN** a host has a completed extraction from an older k3c whose payload
  lacked a helper binary, and k3c is upgraded to a version that embeds that
  helper while keeping the same container-runtime version
- **THEN** the next k3c invocation detects the payload-identity mismatch,
  re-extracts cleanly, and the new helper binary is present on disk

#### Scenario: Unchanged binary reuses the extraction

- **WHEN** the running k3c binary's embedded payload is unchanged from the last
  completed extraction
- **THEN** k3c reuses the existing extraction without re-extracting

### Requirement: Release payload ships all required helper binaries

The bundled-payload guard that runs in release CI SHALL verify that every
k3c-owned helper binary the runtime depends on is present in the embedded
payload, in addition to the Apple `container` runtime plugins it already checks.
At minimum this SHALL include `bin/gvnet` (transparent-egress netstack helper)
and `bin/k3c-docker-fwd` (in-guest docker nested-port forwarder). A payload
missing any of these SHALL fail the test.

#### Scenario: Forwarder missing from the payload

- **WHEN** the bundled payload does not contain `bin/k3c-docker-fwd`
- **THEN** the bundled-payload test fails, blocking the release

#### Scenario: Complete payload passes the guard

- **WHEN** the bundled payload contains `bin/gvnet` and `bin/k3c-docker-fwd`
  alongside the required container-runtime plugins
- **THEN** the bundled-payload test passes
