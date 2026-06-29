package cluster

import (
	"os"
	"path/filepath"
	"testing"

	"k3c/config"
)

// writeFakeSnapshot creates a minimal snapshot directory (just a meta.yaml) so
// the rename path has something on disk to move.
func writeFakeSnapshot(t *testing.T, cfg *config.Config, name string) string {
	t.Helper()
	dir := snapshotDir(cfg, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.yaml"), []byte("mode: cold\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// SnapshotRename moves the snapshot directory and carries its pull-cache pin
// (digests preserved) over to the new name.
func TestSnapshotRenameMovesDirAndPin(t *testing.T) {
	base := t.TempDir()
	// renameSnapshotPin re-resolves config from K3C_BASE_DIR, so point it at the
	// same temp dir the injected cfg uses, keeping the whole test hermetic.
	t.Setenv("K3C_BASE_DIR", base)
	cfg := &config.Config{BaseDir: base, Cluster: "c1"}

	writeFakeSnapshot(t, cfg, "old")
	pinned := digestOf("renamed-layer")
	if err := pinSnapshotImagesIn(cfg, snapshotPinID("c1", "old"), []string{pinned}); err != nil {
		t.Fatal(err)
	}

	if err := SnapshotRename(cfg, "old", "new"); err != nil {
		t.Fatalf("rename failed: %v", err)
	}

	if _, err := os.Stat(snapshotDir(cfg, "old")); err == nil {
		t.Error("old snapshot dir still exists after rename")
	}
	if _, err := os.Stat(snapshotDir(cfg, "new")); err != nil {
		t.Errorf("new snapshot dir missing after rename: %v", err)
	}
	// the pin file moved to the new id...
	if _, err := os.Stat(pinFilePath(cfg, snapshotPinID("c1", "old"))); err == nil {
		t.Error("old pin file still exists after rename")
	}
	if _, err := os.Stat(pinFilePath(cfg, snapshotPinID("c1", "new"))); err != nil {
		t.Errorf("new pin file missing after rename: %v", err)
	}
	// ...with its digest intact, so retention still treats the blob as live.
	union, err := pinnedDigestsIn(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := union[pinned]; !ok {
		t.Errorf("pinned digest lost across rename; union=%v", union)
	}
}

// The guard paths reject before touching the filesystem.
func TestSnapshotRenameRejects(t *testing.T) {
	base := t.TempDir()
	cfg := &config.Config{BaseDir: base, Cluster: "c1"}
	writeFakeSnapshot(t, cfg, "exists")
	writeFakeSnapshot(t, cfg, "target")

	cases := map[string]struct{ old, new string }{
		"missing source":   {"ghost", "fresh"},
		"target taken":     {"exists", "target"},
		"same name":        {"exists", "exists"},
		"invalid new name": {"exists", "bad/name"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if err := SnapshotRename(cfg, c.old, c.new); err == nil {
				t.Errorf("rename %q -> %q succeeded, want error", c.old, c.new)
			}
		})
	}
	// the rejected renames left the originals in place
	if _, err := os.Stat(snapshotDir(cfg, "exists")); err != nil {
		t.Errorf("source snapshot was disturbed by a rejected rename: %v", err)
	}
}

// renameSnapshotPinIn is a no-op (not an error) when the snapshot never pinned
// anything, so renaming a warm/cold snapshot is safe.
func TestRenameSnapshotPinAbsentIsNoop(t *testing.T) {
	cfg := newPinTestCfg(t)
	if err := renameSnapshotPinIn(cfg, snapshotPinID("c1", "old"), snapshotPinID("c1", "new")); err != nil {
		t.Errorf("renaming an absent pin returned error: %v", err)
	}
}
