## 1. Payload-identity fingerprint

- [x] 1.1 Add a `payloadFingerprint()` helper in `runtime/bundle.go` returning
      `version.GitCommit` + `version.Version` + `len(bundlePayload)` as a stable
      string (import `k3c/version`).
- [x] 1.2 Decide the marker contract: the `.complete` marker stores the
      fingerprint string (plain text) instead of `bundleVersion()`.

## 2. Re-key extraction freshness

- [x] 2.1 In `extractBundle()`, replace the "marker exists → reuse" check with
      "marker exists AND its contents equal `payloadFingerprint()` → reuse";
      otherwise fall through to the clean `RemoveAll` + re-extract path.
- [x] 2.2 Write `payloadFingerprint()` into the marker on successful extraction
      (replacing the current `bundleVersion()` write).
- [x] 2.3 Confirm the directory layout is unchanged (still
      `~/.cache/k3c/runtime/<bundleVersion()>/`) so one dir is reused, not
      multiplied per upgrade.

## 3. CI guard for helper binaries

- [x] 3.1 In `runtime/payload_bundled_test.go`, add a test (or extend
      `TestBundledPayloadHasRequiredPlugins`) that scans the embedded tar for
      `bin/gvnet` and `bin/k3c-docker-fwd` and fails if either is absent.
- [x] 3.2 Ensure the new assertion runs under the existing
      `go test -tags bundled ./runtime/... -run 'TestBundled'` invocation used by
      the release workflow.

## 4. Verification

- [x] 4.1 Unit test for freshness: a marker with a non-matching fingerprint
      forces re-extraction; a matching one reuses (no re-extract).
- [x] 4.2 Manual repro check: with a stale `~/.cache/k3c/runtime/<sha>/` lacking
      `k3c-docker-fwd`, a build with this fix re-extracts and the forwarder
      appears; `k3c docker rm && k3c docker up` then stages it into the guest
      without `K3C_DOCKER_FWD_BINARY`.
- [x] 4.3 `make check` (vet + gofmt) and `go test ./...` pass.
