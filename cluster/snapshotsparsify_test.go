package cluster

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// punchHoleSupported reports whether F_PUNCHHOLE actually deallocates on this
// filesystem (it is darwin-only and only on APFS/HFS+); tests skip otherwise.
func punchHoleSupported(t *testing.T, dir string) bool {
	t.Helper()
	p := filepath.Join(dir, ".punchprobe")
	f, err := os.Create(p)
	if err != nil {
		t.Fatalf("probe create: %v", err)
	}
	defer f.Close()
	defer os.Remove(p)
	// allocate 64 KiB, then punch it and see if st_blocks drops.
	if err := f.Truncate(0); err != nil {
		t.Fatalf("probe truncate: %v", err)
	}
	if _, err := f.WriteAt(bytes.Repeat([]byte{1}, 64*1024), 0); err != nil {
		t.Fatalf("probe write: %v", err)
	}
	_ = f.Sync()
	info1, _ := f.Stat()
	punchHole(f, 0, 64*1024)
	_ = f.Sync()
	info2, _ := f.Stat()
	return allocatedBytes(info1) > 0 && allocatedBytes(info2) < allocatedBytes(info1)
}

func TestReSparsifySnapshot(t *testing.T) {
	dir := t.TempDir()
	if !punchHoleSupported(t, dir) {
		t.Skip("F_PUNCHHOLE not supported / no allocated-block accounting on this filesystem")
	}

	path := filepath.Join(dir, "rootfs.ext4")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}

	// Layout: a non-zero block, then a large zero region, then another
	// non-zero block, all inside a single allocated extent (written
	// contiguously so SEEK_DATA reports one range covering the zeros too).
	const blk = sparsifyBlock
	header := bytes.Repeat([]byte{0xAB}, blk)
	zeros := make([]byte, 256*blk) // 1 MiB of zeros to reclaim
	footer := bytes.Repeat([]byte{0xCD}, blk)

	var data []byte
	data = append(data, header...)
	data = append(data, zeros...)
	data = append(data, footer...)
	if _, err := f.WriteAt(data, 0); err != nil {
		t.Fatal(err)
	}
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	reclaimed, err := reSparsifySnapshot(path)
	if err != nil {
		t.Fatalf("reSparsifySnapshot: %v", err)
	}
	if reclaimed <= 0 {
		t.Fatalf("expected reclaimed > 0, got %d", reclaimed)
	}

	// The non-zero data must survive byte-for-byte and the logical size unchanged.
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(got)) != int64(len(data)) {
		t.Fatalf("logical size changed: got %d want %d", len(got), len(data))
	}
	if !bytes.Equal(got[:blk], header) {
		t.Error("header block corrupted")
	}
	if !bytes.Equal(got[len(got)-blk:], footer) {
		t.Error("footer block corrupted")
	}
	if !bytes.Equal(got[blk:blk+len(zeros)], zeros) {
		t.Error("zero region not read back as zeros after punching")
	}

	// Idempotence: a second pass reclaims nothing more and does not error.
	again, err := reSparsifySnapshot(path)
	if err != nil {
		t.Fatalf("second reSparsifySnapshot: %v", err)
	}
	if again != 0 {
		t.Errorf("expected second pass to reclaim 0, got %d", again)
	}
}
