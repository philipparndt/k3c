package cluster

import (
	"os"
	"path/filepath"
	"testing"

	"k3c/config"
)

// TestSnapshotTargetsDescribeEachVM pins the two adapters' descriptors: the
// engine's whole behavior is driven by these, so a drift here is a drift in what
// gets captured.
func TestSnapshotTargetsDescribeEachVM(t *testing.T) {
	cfg := &config.Config{ServerName: "demo-server", RegistryName: "demo-registry", Cluster: "demo"}

	cl := clusterSnapshotTarget(cfg)
	if cl.machine != "demo-server" || cl.statePrefix != "server-" {
		t.Errorf("cluster target = {machine:%q prefix:%q}, want demo-server/server-", cl.machine, cl.statePrefix)
	}
	if cl.rootfs.name != serverRootfs || !cl.rootfs.required {
		t.Errorf("cluster rootfs = %q required=%v, want %q required", cl.rootfs.name, cl.rootfs.required, serverRootfs)
	}
	if len(cl.extras) != 2 {
		t.Fatalf("cluster extras = %d, want 2 (registry, k3s-etc)", len(cl.extras))
	}
	if cl.extras[0].name != registryRootfs || cl.extras[0].required {
		t.Errorf("cluster extra[0] = %q required=%v, want registry optional", cl.extras[0].name, cl.extras[0].required)
	}
	if cl.extras[1].name != "k3s-etc" || !cl.extras[1].isDir || !cl.extras[1].required {
		t.Errorf("cluster extra[1] = %q dir=%v required=%v, want k3s-etc dir required", cl.extras[1].name, cl.extras[1].isDir, cl.extras[1].required)
	}

	sc := sidecarSnapshotTarget(cfg)
	if sc.machine != dockerName || sc.statePrefix != "sidecar-" {
		t.Errorf("sidecar target = {machine:%q prefix:%q}, want %s/sidecar-", sc.machine, sc.statePrefix, dockerName)
	}
	if sc.rootfs.name != dockerSnapRootfs {
		t.Errorf("sidecar rootfs = %q, want %q", sc.rootfs.name, dockerSnapRootfs)
	}
	if len(sc.extras) != 1 || sc.extras[0].name != dockerSnapVolume || !sc.extras[0].required || sc.extras[0].isDir {
		t.Errorf("sidecar extras = %+v, want one required file %q", sc.extras, dockerSnapVolume)
	}
}

// writeTestFile creates a file (making parent dirs) with string content.
func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestWriteSnapshotArtifactsCopiesRootfsAndExtras exercises the shared save core
// with injected sources: rootfs + a file extra + a dir extra land under their
// snapshot filenames, an optional-missing extra is skipped, cold writes no state.
func TestWriteSnapshotArtifactsCopiesRootfsAndExtras(t *testing.T) {
	live, err := os.MkdirTemp("/tmp", "k3c-live")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(live) })
	dir, err := os.MkdirTemp("/tmp", "k3c-snap")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	rootfsSrc := filepath.Join(live, "rootfs.ext4")
	writeTestFile(t, rootfsSrc, "ROOTFS")
	fileSrc := filepath.Join(live, "vol.img")
	writeTestFile(t, fileSrc, "VOLUME")
	dirSrc := filepath.Join(live, "etc")
	writeTestFile(t, filepath.Join(dirSrc, "config.yaml"), "CONF")

	target := snapshotTarget{
		machine:     "m",
		statePrefix: "m-",
		rootfs:      snapshotArtifact{name: "root.ext4", label: "root", src: func() (string, error) { return rootfsSrc, nil }, required: true},
		extras: []snapshotArtifact{
			{name: "vol.img", label: "vol", src: func() (string, error) { return fileSrc, nil }, required: true},
			{name: "etc", label: "etc", src: func() (string, error) { return dirSrc, nil }, required: true, isDir: true},
			{name: "absent.ext4", label: "absent", src: func() (string, error) { return "", os.ErrNotExist }, required: false},
		},
	}
	if err := writeSnapshotArtifacts(dir, target, false); err != nil {
		t.Fatalf("writeSnapshotArtifacts: %v", err)
	}
	assertFile(t, filepath.Join(dir, "root.ext4"), "ROOTFS")
	assertFile(t, filepath.Join(dir, "vol.img"), "VOLUME")
	assertFile(t, filepath.Join(dir, "etc", "config.yaml"), "CONF")
	if _, err := os.Stat(filepath.Join(dir, "absent.ext4")); err == nil {
		t.Error("optional-missing artifact should have been skipped, but it was written")
	}
}

// TestWriteSnapshotArtifactsRequiredMissingErrors: a required artifact whose
// source cannot be resolved aborts the save.
func TestWriteSnapshotArtifactsRequiredMissingErrors(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "k3c-snap")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	target := snapshotTarget{
		rootfs: snapshotArtifact{name: "root.ext4", src: func() (string, error) { return "", os.ErrNotExist }, required: true},
	}
	if err := writeSnapshotArtifacts(dir, target, false); err == nil {
		t.Fatal("expected error when a required artifact is missing, got nil")
	}
}

// TestWriteWarmStatePrefixesFiles: warm state files are cloned under the target
// prefix, and a missing vmstate is an error (the machine wasn't suspended).
func TestWriteWarmStatePrefixesFiles(t *testing.T) {
	live, err := os.MkdirTemp("/tmp", "k3c-state")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(live) })
	dir, err := os.MkdirTemp("/tmp", "k3c-snap")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	// stateFile resolves a name to a live file only if it exists (mirrors
	// containerStateFilePath).
	stateFile := func(name string) (string, error) {
		p := filepath.Join(live, name)
		if _, err := os.Stat(p); err != nil {
			return "", err
		}
		return p, nil
	}
	target := snapshotTarget{machine: "m", statePrefix: "sidecar-", stateFile: stateFile}

	// no vmstate yet → error
	if err := writeWarmState(dir, target); err == nil {
		t.Fatal("expected error when vmstate is absent")
	}

	// create vmstate + one more state file, leave one absent
	writeTestFile(t, filepath.Join(live, vmstateFile), "VM")
	writeTestFile(t, filepath.Join(live, "machine-identifier.bin"), "ID")
	if err := writeWarmState(dir, target); err != nil {
		t.Fatalf("writeWarmState: %v", err)
	}
	assertFile(t, filepath.Join(dir, "sidecar-"+vmstateFile), "VM")
	assertFile(t, filepath.Join(dir, "sidecar-machine-identifier.bin"), "ID")
}

// TestRestoreArtifactCopiesBackAndSkips: an artifact the snapshot carries is
// cloned to its live destination; one it lacks, or whose destination cannot be
// resolved, is skipped without error.
func TestRestoreArtifactCopiesBackAndSkips(t *testing.T) {
	dir, _ := os.MkdirTemp("/tmp", "k3c-snap")
	live, _ := os.MkdirTemp("/tmp", "k3c-live")
	t.Cleanup(func() { _ = os.RemoveAll(dir); _ = os.RemoveAll(live) })

	writeTestFile(t, filepath.Join(dir, "root.ext4"), "SNAP")
	dstPath := filepath.Join(live, "rootfs.ext4")

	// present in snapshot → cloned to dst
	if err := restoreArtifact(dir, snapshotArtifact{name: "root.ext4", src: func() (string, error) { return dstPath, nil }}); err != nil {
		t.Fatalf("restoreArtifact: %v", err)
	}
	assertFile(t, dstPath, "SNAP")

	// absent in snapshot → skipped, no error
	if err := restoreArtifact(dir, snapshotArtifact{name: "missing.ext4", src: func() (string, error) { return dstPath, nil }}); err != nil {
		t.Errorf("absent artifact should skip, got %v", err)
	}
	// dst unresolvable (e.g. absent registry VM) → skipped, no error
	if err := restoreArtifact(dir, snapshotArtifact{name: "root.ext4", src: func() (string, error) { return "", os.ErrNotExist }}); err != nil {
		t.Errorf("unresolvable dst should skip, got %v", err)
	}

	// dst override is honored over src (the volume case)
	dst2 := filepath.Join(live, "vol.img")
	if err := restoreArtifact(dir, snapshotArtifact{
		name: "root.ext4",
		src:  func() (string, error) { return "", os.ErrNotExist }, // would skip if used
		dst:  func() (string, error) { return dst2, nil },
	}); err != nil {
		t.Fatalf("restoreArtifact with dst override: %v", err)
	}
	assertFile(t, dst2, "SNAP")
}

// TestRestoreMachineStateColdPreservesIdentity pins the reconciled behavior: a
// cold restore clears stale non-identity state but KEEPS machine-identifier.bin
// (previously the sidecar path removed it), and reports not-warm.
func TestRestoreMachineStateColdPreservesIdentity(t *testing.T) {
	dir, _ := os.MkdirTemp("/tmp", "k3c-snap")   // empty snapshot (cold)
	live, _ := os.MkdirTemp("/tmp", "k3c-live")
	t.Cleanup(func() { _ = os.RemoveAll(dir); _ = os.RemoveAll(live) })

	writeTestFile(t, filepath.Join(live, vmstateFile), "VM")
	writeTestFile(t, filepath.Join(live, "vmstate-attachments.json"), "A")
	writeTestFile(t, filepath.Join(live, "machine-identifier.bin"), "ID")

	target := snapshotTarget{
		statePrefix: "sidecar-",
		statePath:   func(name string) (string, error) { return filepath.Join(live, name), nil },
	}
	warm, err := restoreMachineState(dir, target, true)
	if err != nil || warm {
		t.Fatalf("cold restore = (warm=%v, err=%v), want (false, nil)", warm, err)
	}
	for _, gone := range []string{vmstateFile, "vmstate-attachments.json"} {
		if _, err := os.Stat(filepath.Join(live, gone)); err == nil {
			t.Errorf("stale state %q should have been removed on cold restore", gone)
		}
	}
	if _, err := os.Stat(filepath.Join(live, "machine-identifier.bin")); err != nil {
		t.Error("machine-identifier.bin must be preserved on a cold restore (stable identity)")
	}
}

// TestRestoreMachineStateWarmClonesBack: a warm restore clones the snapshot's
// prefixed state (including the identifier) back and reports warm.
func TestRestoreMachineStateWarmClonesBack(t *testing.T) {
	dir, _ := os.MkdirTemp("/tmp", "k3c-snap")
	live, _ := os.MkdirTemp("/tmp", "k3c-live")
	t.Cleanup(func() { _ = os.RemoveAll(dir); _ = os.RemoveAll(live) })

	writeTestFile(t, filepath.Join(dir, "sidecar-"+vmstateFile), "VM")
	writeTestFile(t, filepath.Join(dir, "sidecar-machine-identifier.bin"), "ID")

	target := snapshotTarget{
		statePrefix: "sidecar-",
		statePath:   func(name string) (string, error) { return filepath.Join(live, name), nil },
	}
	warm, err := restoreMachineState(dir, target, false)
	if err != nil || !warm {
		t.Fatalf("warm restore = (warm=%v, err=%v), want (true, nil)", warm, err)
	}
	assertFile(t, filepath.Join(live, vmstateFile), "VM")
	assertFile(t, filepath.Join(live, "machine-identifier.bin"), "ID")
}

// TestRestoreMachineStateNoSnapshotStateBootsCold: !cold requested but the
// snapshot carries no machine state → not warm, stale cleared.
func TestRestoreMachineStateNoSnapshotStateBootsCold(t *testing.T) {
	dir, _ := os.MkdirTemp("/tmp", "k3c-snap")   // no prefixed state
	live, _ := os.MkdirTemp("/tmp", "k3c-live")
	t.Cleanup(func() { _ = os.RemoveAll(dir); _ = os.RemoveAll(live) })
	writeTestFile(t, filepath.Join(live, vmstateFile), "VM")

	target := snapshotTarget{
		statePrefix: "server-",
		statePath:   func(name string) (string, error) { return filepath.Join(live, name), nil },
	}
	warm, err := restoreMachineState(dir, target, false)
	if err != nil || warm {
		t.Fatalf("no-state restore = (warm=%v, err=%v), want (false, nil)", warm, err)
	}
	if _, err := os.Stat(filepath.Join(live, vmstateFile)); err == nil {
		t.Error("stale vmstate should have been removed")
	}
}

// TestScanSnapshotsParsesMetaAndDefaults pins the shared list scan: it reads the
// per-target meta filename for mode/created, applies the caller's Created
// default when a snapshot has no meta, and ignores non-directory entries.
func TestScanSnapshotsParsesMetaAndDefaults(t *testing.T) {
	root, _ := os.MkdirTemp("/tmp", "k3c-snaproot")
	t.Cleanup(func() { _ = os.RemoveAll(root) })

	writeTestFile(t, filepath.Join(root, "golden", "meta.yaml"), "cluster: c\ncreated: 2026-07-03T10:00:00+02:00\nmode: warm\n")
	writeTestFile(t, filepath.Join(root, "plain", "meta.yaml"), "created: 2026-07-01T09:00:00+02:00\n") // no mode → cold
	if err := os.MkdirAll(filepath.Join(root, "nometa"), 0o755); err != nil {                          // no meta → defaults
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(root, "loose-file"), "ignored") // not a dir → skipped

	got := map[string]SnapshotInfo{}
	for _, s := range scanSnapshots(root, "meta.yaml", "?") {
		got[s.Name] = s
	}
	if len(got) != 3 {
		t.Fatalf("scanSnapshots found %d snapshots, want 3 (golden, plain, nometa)", len(got))
	}
	if got["golden"].Mode != "warm" || got["golden"].Created != "2026-07-03T10:00:00+02:00" {
		t.Errorf("golden = %+v, want warm @ 2026-07-03T10:00:00+02:00", got["golden"])
	}
	if got["plain"].Mode != "cold" {
		t.Errorf("plain mode = %q, want cold (default)", got["plain"].Mode)
	}
	if got["nometa"].Mode != "cold" || got["nometa"].Created != "?" {
		t.Errorf("nometa = %+v, want cold @ '?' (defaults)", got["nometa"])
	}
	if _, ok := got["loose-file"]; ok {
		t.Error("non-directory entry should be ignored")
	}
}

func assertFile(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if string(got) != want {
		t.Errorf("%s = %q, want %q", path, got, want)
	}
}
