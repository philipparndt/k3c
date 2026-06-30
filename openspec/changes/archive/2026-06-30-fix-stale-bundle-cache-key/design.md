## Context

`extractBundle()` (`runtime/bundle.go`) extracts the embedded runtime payload to
`~/.cache/k3c/runtime/<bundleVersion()>/` exactly once, guarded by a `.complete`
marker. `bundleVersion()` is derived solely from `bundledContainerVersion` (the
Apple `container` fork version, e.g. `7ed75e1`). The payload, however, also
carries k3c-owned helper binaries built by `make bundle`: `bin/gvnet` and — since
v0.18.0 — `bin/k3c-docker-fwd`.

Because the cache key ignores the k3c version, a k3c upgrade that changes only
the helper binaries (same container fork) reuses the stale `.complete` marker and
never re-extracts. Observed: bundled v0.19.0 embeds `k3c-docker-fwd` correctly
(verified by extracting the installed binary into an isolated `XDG_CACHE_HOME`),
yet a host upgraded from pre-v0.18.0 keeps a `7ed75e1/` extraction without it.
`DockerForwarderBinary()` → `extractBundle()` returns that stale dir, the
forwarder is "not found", and the docker-sidecar engine is unreachable.

The release CI runs `TestBundled` (`runtime/payload_bundled_test.go`) over the
embedded payload, but it only asserts container-runtime plugins (`requiredPlugins`
— gvnet's *plugin*, not the standalone helper binaries), so a payload missing
`k3c-docker-fwd` passes CI silently.

## Goals / Non-Goals

**Goals:**
- An extraction is reused only when it matches the running binary's payload;
  otherwise k3c re-extracts cleanly — automatically healing stale caches on the
  next run after upgrade.
- The bundled-payload test fails the release if any k3c-owned helper binary
  (`bin/gvnet`, `bin/k3c-docker-fwd`) is missing.
- Cheap: no hashing of the ~300 MB payload on the hot path (every invocation
  resolves the runtime).

**Non-Goals:**
- Changing `make bundle` / goreleaser packaging — the payload is already correct.
- Any CLI surface change.
- Multi-version cache retention / rollback of extractions.

## Decisions

### Identify the extraction by k3c build identity in the `.complete` marker

Keep the extraction directory keyed by the container version (so the layout is
unchanged and one dir is reused), but **write a payload-identity fingerprint into
the `.complete` marker and compare it on every check**. Reuse only on an exact
match; on mismatch, treat the extraction as stale → `RemoveAll` + re-extract
(the existing stale/partial branch already does this).

Fingerprint = `version.GitCommit` + `version.Version` + `len(bundlePayload)`
(decimal). Rationale:
- `GitCommit`/`Version` are injected via ldflags on release builds (`.goreleaser.yaml`),
  so every distinct k3c release produces a distinct marker — the exact case that
  broke. O(1) to read.
- `len(bundlePayload)` is a cheap content signal that also invalidates `dev`
  builds (where `Version="dev"`, `GitCommit="unknown"` are constant) whenever the
  rebuilt payload changes size.

**Alternatives considered:**
- *SHA-256 of `bundlePayload`*: fully correct, but hashing ~300 MB on every
  invocation to derive the key is too slow for the runtime-resolution hot path.
  Rejected. (A length check is the cheap stand-in; collisions require a same-size
  payload from the same commit — not a real upgrade scenario.)
- *Fold the k3c version into the directory name*: avoids any reuse race and keeps
  old extractions for rollback, but leaves multiple ~300 MB trees in the cache
  across upgrades. Rejected in favor of reusing one dir via marker compare.

### Extend `TestBundled` to assert helper binaries

Add the standalone helper binaries to the bundled-payload assertion: scan the
embedded tar for `bin/gvnet` and `bin/k3c-docker-fwd` (distinct from the existing
`requiredPlugins` plugin check) and fail if either is absent. This makes the
packaging gap a red CI build rather than a runtime surprise.

## Risks / Trade-offs

- **Length-only fingerprint for `dev` builds collides on same-size rebuilds** →
  Mitigation: acceptable for dev (developers can `rm -rf ~/.cache/k3c/runtime`);
  release builds are fully disambiguated by `GitCommit`. If stronger dev behavior
  is wanted later, hash only when `Version=="dev"`.
- **One-time extra re-extraction (~300 MB) for every already-deployed stale
  cache** on first run after this ships → Mitigation: one-time, expected, and
  strictly the correct behavior; it is what fixes the bug.
- **Marker format change** → old markers won't match the new fingerprint and will
  trigger one re-extraction. Harmless and self-correcting; no migration needed.

## Migration Plan

No manual migration. On the first invocation after upgrading to a k3c with this
fix, the marker mismatch forces a clean re-extraction that includes the correct
helper binaries. Until this ships, the manual workaround remains
`rm -rf ~/.cache/k3c/runtime/container-CLI-version-*` followed by any k3c command.

## Open Questions

- Should the marker store the full fingerprint as plain text (human-debuggable)
  or a short hash of it? Leaning plain text for diagnosability; either satisfies
  the spec.
