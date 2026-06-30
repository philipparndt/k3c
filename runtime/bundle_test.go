package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

// TestExtractionIsFresh covers the cache-freshness decision that heals a stale
// extraction left by an older k3c: an extraction is reused only when its
// `.complete` marker matches the running binary's payload fingerprint.
func TestExtractionIsFresh(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, ".complete")
	const fp = "abc123 v1.2.3 314159"

	t.Run("missing marker is not fresh", func(t *testing.T) {
		if extractionIsFresh(marker, fp) {
			t.Fatal("missing marker should not be reused")
		}
	})

	t.Run("matching marker is fresh", func(t *testing.T) {
		if err := os.WriteFile(marker, []byte(fp+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if !extractionIsFresh(marker, fp) {
			t.Fatal("marker matching the fingerprint should be reused")
		}
	})

	t.Run("mismatched marker is not fresh", func(t *testing.T) {
		if err := os.WriteFile(marker, []byte("OTHER-COMMIT v0.9.0 271828\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if extractionIsFresh(marker, fp) {
			t.Fatal("marker from a different binary must force re-extraction")
		}
	})
}

// TestPayloadFingerprintStable guards two properties the fix relies on: the
// fingerprint is never empty (so the marker always carries an identity) and it
// is stable within one binary (so an unchanged binary reuses its extraction).
func TestPayloadFingerprintStable(t *testing.T) {
	first := payloadFingerprint()
	if first == "" {
		t.Fatal("payloadFingerprint must never be empty")
	}
	// Idempotent within a single binary.
	if second := payloadFingerprint(); first != second {
		t.Fatalf("payloadFingerprint must be stable across calls: %q != %q", first, second)
	}
}
