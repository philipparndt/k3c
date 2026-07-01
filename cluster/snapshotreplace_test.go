package cluster

import (
	"os"
	"testing"

	"k3c/config"
)

// prepareSnapshotSlot is the pre-save guard behind `snapshot save [--replace]`:
// a plain save refuses to overwrite an existing snapshot, while --replace
// deletes it first so the save recreates it in place.
func TestPrepareSnapshotSlot(t *testing.T) {
	base := t.TempDir()
	// SnapshotDelete -> releaseSnapshotPin re-resolves config from K3C_BASE_DIR.
	t.Setenv("K3C_BASE_DIR", base)
	cfg := &config.Config{BaseDir: base, Cluster: "c1"}

	// no existing snapshot: the slot is free either way
	if err := prepareSnapshotSlot(cfg, "fresh", false); err != nil {
		t.Errorf("free slot without replace errored: %v", err)
	}

	// existing snapshot, no replace: refuse to overwrite, leave it in place
	writeFakeSnapshot(t, cfg, "golden")
	if err := prepareSnapshotSlot(cfg, "golden", false); err == nil {
		t.Error("save over an existing snapshot without --replace succeeded, want error")
	}
	if _, err := os.Stat(snapshotDir(cfg, "golden")); err != nil {
		t.Errorf("rejected save disturbed the existing snapshot: %v", err)
	}

	// existing snapshot, replace: delete it so the save can recreate it
	if err := prepareSnapshotSlot(cfg, "golden", true); err != nil {
		t.Errorf("replace of an existing snapshot errored: %v", err)
	}
	if _, err := os.Stat(snapshotDir(cfg, "golden")); err == nil {
		t.Error("--replace did not delete the existing snapshot dir")
	}
}
