# runtime-passthrough Specification

## Purpose

Make the Apple `container` CLI usable without installing it separately, by
exposing the runtime k3c already resolves (bundled by default) through a
passthrough command. This capability owns `k3c container`. It is the user-facing
entry to the same runtime every other k3c command drives, which makes it the
primary tool for inspecting VMs while tracing problems.

## Requirements

### Requirement: Pass arguments through to the resolved runtime

`k3c container [ARGS...]` SHALL run the resolved Apple `container` CLI with
k3c's runtime environment, passing all arguments through verbatim. Flag parsing
SHALL be disabled so every flag — including `--help` — belongs to the container
CLI. The exit code of the underlying CLI SHALL be propagated.

#### Scenario: Inspect VMs

- **WHEN** the user runs `k3c container ls -a`
- **THEN** the arguments are passed verbatim to the resolved container CLI and
  its output and exit code are returned unchanged

### Requirement: Use the same runtime as every other command

The passthrough SHALL resolve the runtime the same way the rest of k3c does:
the binary set via `containerBinary` in the config when present, otherwise the
bundled runtime. If the runtime cannot be resolved, the command SHALL fail.

#### Scenario: Honor a configured container binary

- **WHEN** `containerBinary` is set in the config and the user runs `k3c
  container ...`
- **THEN** the configured binary is used, matching the runtime every other k3c
  command drives
