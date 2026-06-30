## Why

A k3c upgrade that adds or changes an embedded helper binary but keeps the same
Apple `container` fork is silently masked: the bundle extraction cache is keyed
only by the container-runtime version, so the stale `.complete` marker makes
`extractBundle()` skip re-extraction and the new payload never lands on disk.
This shipped a real, hard-to-diagnose break — bundled v0.19.0 carries
`k3c-docker-fwd` in its payload, but hosts upgraded from a pre-v0.18.0 k3c keep
a stale `~/.cache/k3c/runtime/container-CLI-version-<sha>/` without it. The
docker-sidecar forwarder is then "not found", the engine is unreachable over the
host socket, and Testcontainers cannot start. The CI guard does not catch it
because `TestBundled` only asserts container plugins, not k3c's own helpers.

## What Changes

- Re-key the bundle extraction so it invalidates when the running k3c binary's
  embedded payload changes, not only when the container-runtime version changes.
  The extraction directory and/or its `.complete` marker SHALL incorporate the
  k3c version (or a payload content hash), and a marker that does not match the
  current binary SHALL force a clean re-extraction.
- Extend the bundled-payload test (`runtime/payload_bundled_test.go`,
  `-run TestBundled`) to assert that every k3c-owned helper binary the runtime
  needs — `bin/k3c-docker-fwd` and `bin/gvnet` — is present in the payload, so a
  missing forwarder fails the release CI instead of slipping through.
- No CLI surface change. Existing stale caches are healed automatically on the
  next run after upgrade.

## Capabilities

### New Capabilities
- `runtime-bundling`: how the embedded container-runtime payload (Apple
  `container`, `container-apiserver`, and k3c's helper binaries `gvnet` and
  `k3c-docker-fwd`) is packaged into the release binary, extracted to the host
  cache, and kept consistent with the running binary across upgrades.

### Modified Capabilities
<!-- none: bundle extraction freshness and payload completeness are not described by any existing spec -->

## Impact

- `runtime/bundle.go` — `extractBundle()` cache key / `.complete` marker logic
  (`cacheRoot`, `bundleVersion`).
- `runtime/payload_bundled_test.go` — `TestBundledPayloadHasRequiredPlugins`
  (or a sibling) gains assertions for `bin/k3c-docker-fwd` and `bin/gvnet`.
- No change to `make bundle` / goreleaser packaging — the payload is already
  correct; only extraction freshness and the test guard change.
- Behavioral: users on a stale extraction self-heal on next invocation; no
  manual `rm -rf ~/.cache/k3c/runtime/...` needed after this ships.
