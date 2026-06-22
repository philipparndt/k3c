package cluster

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"k3c/config"
)

// newPinTestCfg builds a config backed by a temp pull-cache with the standard
// subdirectories created.
func newPinTestCfg(t *testing.T) *config.Config {
	t.Helper()
	cfg := &config.Config{BaseDir: t.TempDir()}
	for _, dir := range []string{"blobs", "types", "tags"} {
		if err := os.MkdirAll(filepath.Join(pullCacheDir(cfg), dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return cfg
}

// writeBlob drops a fake blob into the content store keyed by digest and backdates
// its mtime so prune treats it as expired.
func writeBlob(t *testing.T, cfg *config.Config, digest string, old bool) {
	t.Helper()
	path := filepath.Join(pullCacheDir(cfg), "blobs", digest)
	if err := os.WriteFile(path, []byte("blob:"+digest), 0o644); err != nil {
		t.Fatal(err)
	}
	if old {
		past := time.Now().Add(-90 * 24 * time.Hour)
		if err := os.Chtimes(path, past, past); err != nil {
			t.Fatal(err)
		}
	}
}

func digestOf(s string) string { return sha256Digest([]byte(s)) }

// A pinned blob survives prune even when it is well past the retention cutoff
// and is anchored by no tag; an unpinned, equally-old blob is swept.
func TestPrunePinnedBlobSurvives(t *testing.T) {
	cfg := newPinTestCfg(t)
	pinnedD := digestOf("pinned-layer")
	looseD := digestOf("loose-layer")
	writeBlob(t, cfg, pinnedD, true)
	writeBlob(t, cfg, looseD, true)

	if err := pinSnapshotImagesIn(cfg, snapshotPinID("c1", "snapA"), []string{pinnedD}); err != nil {
		t.Fatal(err)
	}

	if err := PullCachePrune(cfg, 14*24*time.Hour); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(pullCacheDir(cfg), "blobs", pinnedD)); err != nil {
		t.Errorf("pinned blob was pruned, want retained: %v", err)
	}
	if _, err := os.Stat(filepath.Join(pullCacheDir(cfg), "blobs", looseD)); err == nil {
		t.Error("unpinned expired blob survived prune, want swept")
	}
}

// Releasing the only pin holding a digest makes it eligible for retention again,
// so a subsequent prune sweeps it.
func TestReleasePinFreesBlob(t *testing.T) {
	cfg := newPinTestCfg(t)
	d := digestOf("freed-layer")
	writeBlob(t, cfg, d, true)
	pinID := snapshotPinID("c1", "snapA")

	if err := pinSnapshotImagesIn(cfg, pinID, []string{d}); err != nil {
		t.Fatal(err)
	}
	if err := PullCachePrune(cfg, 14*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(pullCacheDir(cfg), "blobs", d)); err != nil {
		t.Fatalf("blob pruned while still pinned: %v", err)
	}

	if err := releaseSnapshotPinIn(cfg, pinID); err != nil {
		t.Fatal(err)
	}
	// releasing again is a no-op, not an error
	if err := releaseSnapshotPinIn(cfg, pinID); err != nil {
		t.Errorf("releasing an absent pin returned error: %v", err)
	}

	if err := PullCachePrune(cfg, 14*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(pullCacheDir(cfg), "blobs", d)); err == nil {
		t.Error("blob survived prune after its pin was released, want swept")
	}
}

// pinnedDigests returns the union across multiple pin files, deduping a digest
// shared by two snapshots.
func TestPinnedDigestsUnion(t *testing.T) {
	cfg := newPinTestCfg(t)
	shared := digestOf("shared")
	onlyA := digestOf("onlyA")
	onlyB := digestOf("onlyB")

	if err := pinSnapshotImagesIn(cfg, snapshotPinID("c1", "snapA"), []string{shared, onlyA}); err != nil {
		t.Fatal(err)
	}
	if err := pinSnapshotImagesIn(cfg, snapshotPinID("c2", "snapB"), []string{shared, onlyB}); err != nil {
		t.Fatal(err)
	}

	union, err := pinnedDigestsIn(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{shared, onlyA, onlyB} {
		if _, ok := union[d]; !ok {
			t.Errorf("union missing digest %s", d)
		}
	}
	if len(union) != 3 {
		t.Errorf("union size = %d, want 3 (shared digest deduped)", len(union))
	}

	// Releasing snapA leaves snapB's digests (incl. the shared one) pinned.
	if err := releaseSnapshotPinIn(cfg, snapshotPinID("c1", "snapA")); err != nil {
		t.Fatal(err)
	}
	union, err = pinnedDigestsIn(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := union[onlyA]; ok {
		t.Error("onlyA still pinned after releasing snapA")
	}
	if _, ok := union[shared]; !ok {
		t.Error("shared digest dropped though snapB still pins it")
	}
}

// A bare hex digest is normalized to the sha256: form the pull-cache names
// blobs with, so pins written that way still match the blob store.
func TestPinNormalizesBareHexDigest(t *testing.T) {
	cfg := newPinTestCfg(t)
	full := digestOf("normalize-me")
	bare := full[len("sha256:"):] // 64 hex chars, no prefix

	if err := pinSnapshotImagesIn(cfg, snapshotPinID("c1", "snapA"), []string{bare}); err != nil {
		t.Fatal(err)
	}
	union, err := pinnedDigestsIn(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := union[full]; !ok {
		t.Errorf("bare hex digest not normalized to %s; union=%v", full, union)
	}
}

// Clear without force retains pinned blobs (and drops the rest); clear with
// force removes everything.
func TestClearForceVsPinnedRetention(t *testing.T) {
	cfg := newPinTestCfg(t)
	pinnedD := digestOf("clear-pinned")
	looseD := digestOf("clear-loose")
	writeBlob(t, cfg, pinnedD, false)
	writeBlob(t, cfg, looseD, false)
	if err := pinSnapshotImagesIn(cfg, snapshotPinID("c1", "snapA"), []string{pinnedD}); err != nil {
		t.Fatal(err)
	}

	if err := PullCacheClear(cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(pullCacheDir(cfg), "blobs", pinnedD)); err != nil {
		t.Errorf("non-forced clear removed a pinned blob: %v", err)
	}
	if _, err := os.Stat(filepath.Join(pullCacheDir(cfg), "blobs", looseD)); err == nil {
		t.Error("non-forced clear kept an unpinned blob")
	}

	if err := PullCacheClearForce(cfg, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(pullCacheDir(cfg), "blobs", pinnedD)); err == nil {
		t.Error("forced clear kept a pinned blob, want removed")
	}
}
