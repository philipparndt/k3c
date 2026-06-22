package cluster

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"k3c/config"
)

func TestFrozenManifestRoundTrip(t *testing.T) {
	m := frozenManifest{
		Images:  []string{"docker.io/library/nginx:1.27", "ghcr.io/foo/bar:latest"},
		Digests: []string{"sha256:aaaa", "sha256:bbbb", "sha256:cccc"},
	}
	path := filepath.Join(t.TempDir(), frozenManifestF)
	if err := writeFrozenManifest(path, m); err != nil {
		t.Fatal(err)
	}
	got, err := readFrozenManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Images) != len(m.Images) {
		t.Fatalf("images: got %v want %v", got.Images, m.Images)
	}
	for i, img := range m.Images {
		if got.Images[i] != img {
			t.Fatalf("image %d: got %q want %q", i, got.Images[i], img)
		}
	}
	if len(got.Digests) != len(m.Digests) {
		t.Fatalf("digests: got %v want %v", got.Digests, m.Digests)
	}
	for i, d := range m.Digests {
		if got.Digests[i] != d {
			t.Fatalf("digest %d: got %q want %q", i, got.Digests[i], d)
		}
	}
}

func TestVerifyFrozenBlobsReportsMissing(t *testing.T) {
	cfg := &config.Config{BaseDir: t.TempDir()}
	blobs := filepath.Join(pullCacheDir(cfg), "blobs")
	if err := os.MkdirAll(blobs, 0o755); err != nil {
		t.Fatal(err)
	}
	present := sha256Digest([]byte("present-blob"))
	if err := os.WriteFile(filepath.Join(blobs, present), []byte("present-blob"), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := sha256Digest([]byte("missing-blob"))

	dir := t.TempDir()
	m := frozenManifest{Digests: []string{present, missing}}
	if err := writeFrozenManifest(filepath.Join(dir, frozenManifestF), m); err != nil {
		t.Fatal(err)
	}

	err := verifyFrozenBlobs(cfg, dir)
	if err == nil {
		t.Fatal("expected verifyFrozenBlobs to fail on a missing digest")
	}
	// the missing digest must be named; the present one must not be the cause
	if !bytes.Contains([]byte(err.Error()), []byte(missing)) {
		t.Fatalf("error should name the missing digest: %v", err)
	}
}

func TestVerifyFrozenBlobsAllPresent(t *testing.T) {
	cfg := &config.Config{BaseDir: t.TempDir()}
	blobs := filepath.Join(pullCacheDir(cfg), "blobs")
	if err := os.MkdirAll(blobs, 0o755); err != nil {
		t.Fatal(err)
	}
	var digests []string
	for _, s := range []string{"a", "b", "c"} {
		d := sha256Digest([]byte(s))
		digests = append(digests, d)
		if err := os.WriteFile(filepath.Join(blobs, d), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	if err := writeFrozenManifest(filepath.Join(dir, frozenManifestF), frozenManifest{Digests: digests}); err != nil {
		t.Fatal(err)
	}
	if err := verifyFrozenBlobs(cfg, dir); err != nil {
		t.Fatalf("all blobs present, want nil: %v", err)
	}
}

func TestVerifyFrozenBlobsNoManifest(t *testing.T) {
	cfg := &config.Config{BaseDir: t.TempDir()}
	// no manifest at all (thin import / legacy): nothing to verify
	if err := verifyFrozenBlobs(cfg, t.TempDir()); err != nil {
		t.Fatalf("missing manifest should be a no-op, got %v", err)
	}
}

func TestSeedPullCacheBlobContentAddressed(t *testing.T) {
	cfg := &config.Config{BaseDir: t.TempDir()}
	blob := []byte("some-image-layer-bytes")
	digest := sha256Digest(blob)

	// first seed writes the blob
	seeded, err := seedPullCacheBlob(cfg, digest, bytes.NewReader(blob), int64(len(blob)))
	if err != nil {
		t.Fatal(err)
	}
	if !seeded {
		t.Fatal("first seed should report a new blob")
	}
	got, err := os.ReadFile(filepath.Join(pullCacheDir(cfg), "blobs", digest))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, blob) {
		t.Fatal("seeded blob content mismatch")
	}

	// second seed of the same digest dedups (skips)
	seeded, err = seedPullCacheBlob(cfg, digest, bytes.NewReader(blob), int64(len(blob)))
	if err != nil {
		t.Fatal(err)
	}
	if seeded {
		t.Fatal("re-seeding an existing blob should skip (dedup)")
	}
}

func TestSeedPullCacheBlobRejectsCorrupt(t *testing.T) {
	cfg := &config.Config{BaseDir: t.TempDir()}
	claimed := sha256Digest([]byte("the-real-bytes"))
	// stream different bytes than the digest claims
	_, err := seedPullCacheBlob(cfg, claimed, bytes.NewReader([]byte("tampered")), 8)
	if err == nil {
		t.Fatal("expected a corruption error on digest mismatch")
	}
	// the bad blob must not be committed
	if _, err := os.Stat(filepath.Join(pullCacheDir(cfg), "blobs", claimed)); err == nil {
		t.Fatal("corrupt blob must not be committed to the cache")
	}
}

// TestExportImportFrozenRoundTrip exports a fabricated frozen snapshot whose
// blob closure lives in a source pull-cache, then imports it into a target
// whose pull-cache is missing those blobs, and asserts the blobs are seeded
// and the logical files land in a new snapshot dir.
func TestExportImportFrozenRoundTrip(t *testing.T) {
	// --- source: a frozen snapshot dir + a pull-cache holding its blobs ---
	srcBase := t.TempDir()
	srcCfg := &config.Config{
		BaseDir:      srcBase,
		Cluster:      "src",
		ClusterCIDR:  "10.42.0.0/16",
		ServiceCIDR:  "10.43.0.0/16",
		ServerName:   "src-server",
		RegistryName: "src-registry",
	}
	srcBlobs := filepath.Join(pullCacheDir(srcCfg), "blobs")
	if err := os.MkdirAll(srcBlobs, 0o755); err != nil {
		t.Fatal(err)
	}
	blobA := []byte("layer-a-bytes")
	blobB := []byte("config-b-bytes")
	digestA, digestB := sha256Digest(blobA), sha256Digest(blobB)
	for d, b := range map[string][]byte{digestA: blobA, digestB: blobB} {
		if err := os.WriteFile(filepath.Join(srcBlobs, d), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	snapDir := snapshotDir(srcCfg, "snap1")
	if err := os.MkdirAll(snapDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(snapDir, frozenStateDB), []byte("fake-sqlite"))
	writeFile(t, filepath.Join(snapDir, frozenStorageTar), []byte("fake-pvc-tar"))
	writeFile(t, filepath.Join(snapDir, frozenCertsTar), []byte("fake-certs-tar"))
	writeFile(t, filepath.Join(snapDir, "meta.yaml"),
		[]byte("cluster: src\nmode: frozen\nclusterCidr: 10.42.0.0/16\nserviceCidr: 10.43.0.0/16\n"))
	if err := writeFrozenManifest(filepath.Join(snapDir, frozenManifestF),
		frozenManifest{Images: []string{"nginx:1"}, Digests: []string{digestA, digestB}}); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(t.TempDir(), "snap1.k3csnap")
	if err := exportFrozen(srcCfg, "snap1", out, false); err != nil {
		t.Fatalf("exportFrozen (fat): %v", err)
	}

	// --- target: empty pull-cache + a created cluster (k3s-etc present) ---
	tgtBase := t.TempDir()
	tgtCfg := &config.Config{
		BaseDir:      tgtBase,
		Cluster:      "tgt",
		ClusterCIDR:  "10.42.0.0/16",
		ServiceCIDR:  "10.43.0.0/16",
		ServerName:   "tgt-server",
		RegistryName: "tgt-registry",
	}
	if err := os.MkdirAll(tgtCfg.K3sEtcDir(), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := SnapshotImport(tgtCfg, out, "imported"); err != nil {
		t.Fatalf("SnapshotImport (fat frozen): %v", err)
	}

	// blobs seeded into the target pull-cache
	tgtBlobs := filepath.Join(pullCacheDir(tgtCfg), "blobs")
	for _, d := range []string{digestA, digestB} {
		if _, err := os.Stat(filepath.Join(tgtBlobs, d)); err != nil {
			t.Fatalf("blob %s not seeded into target pull-cache: %v", d, err)
		}
	}
	// logical files landed in the new snapshot dir
	impDir := snapshotDir(tgtCfg, "imported")
	for _, f := range []string{frozenStateDB, frozenStorageTar, frozenCertsTar, frozenManifestF, "meta.yaml"} {
		if _, err := os.Stat(filepath.Join(impDir, f)); err != nil {
			t.Fatalf("frozen file %s missing after import: %v", f, err)
		}
	}
	// and it is recognized as a frozen snapshot
	if snapshotModeOf(impDir) != ModeFrozen {
		t.Fatalf("imported snapshot mode = %q, want frozen", snapshotModeOf(impDir))
	}
}

// TestExportImportFrozenThinSkipsBlobs verifies a thin export carries no
// blobs and import does not seed the cache.
func TestExportImportFrozenThinSkipsBlobs(t *testing.T) {
	srcCfg := &config.Config{
		BaseDir: t.TempDir(), Cluster: "src",
		ClusterCIDR: "10.42.0.0/16", ServiceCIDR: "10.43.0.0/16",
	}
	snapDir := snapshotDir(srcCfg, "snap1")
	if err := os.MkdirAll(snapDir, 0o755); err != nil {
		t.Fatal(err)
	}
	d := sha256Digest([]byte("x"))
	writeFile(t, filepath.Join(snapDir, frozenStateDB), []byte("db"))
	writeFile(t, filepath.Join(snapDir, frozenStorageTar), []byte("pvc"))
	writeFile(t, filepath.Join(snapDir, frozenCertsTar), []byte("certs"))
	writeFile(t, filepath.Join(snapDir, "meta.yaml"), []byte("cluster: src\nmode: frozen\n"))
	if err := writeFrozenManifest(filepath.Join(snapDir, frozenManifestF),
		frozenManifest{Digests: []string{d}}); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(t.TempDir(), "thin.k3csnap")
	// thin export must succeed even though the blob is NOT in the pull-cache
	if err := exportFrozen(srcCfg, "snap1", out, true); err != nil {
		t.Fatalf("exportFrozen (thin): %v", err)
	}

	tgtCfg := &config.Config{BaseDir: t.TempDir(), Cluster: "tgt"}
	if err := os.MkdirAll(tgtCfg.K3sEtcDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := SnapshotImport(tgtCfg, out, "imp"); err != nil {
		t.Fatalf("SnapshotImport (thin): %v", err)
	}
	tgtBlobs := filepath.Join(pullCacheDir(tgtCfg), "blobs")
	if entries, _ := os.ReadDir(tgtBlobs); len(entries) != 0 {
		t.Fatalf("thin import should seed no blobs, found %d", len(entries))
	}
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
