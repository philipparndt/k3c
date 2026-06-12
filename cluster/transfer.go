package cluster

import (
	"archive/tar"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"github.com/klauspost/compress/zstd"
	"github.com/philipparndt/go-logger"
	"golang.org/x/sys/unix"

	"k3c/config"
)

// Snapshot export/import: share a snapshot with another machine as a
// single file. Exports are always COLD — saved machine state is tied to
// the exact VM configuration (CPU count, memory, machine identifier) and
// never restores elsewhere. The host-side k3s-etc share is not exported
// either: it holds the sender's registry mirrors, CA bundle, and admin
// kubeconfig; the importer's own configuration applies instead.

const exportManifest = "export.yaml"

// exportable returns the snapshot files included in an export.
func exportable(dir string) []string {
	files := []string{"meta.yaml", serverRootfs}
	if _, err := os.Stat(filepath.Join(dir, registryRootfs)); err == nil {
		files = append(files, registryRootfs)
	}
	return files
}

// SnapshotExport writes a snapshot to a portable archive (tar+zstd).
func SnapshotExport(cfg *config.Config, name, out string) error {
	if err := validSnapshotName(name); err != nil {
		return err
	}
	dir := snapshotDir(cfg, name)
	if _, err := os.Stat(filepath.Join(dir, serverRootfs)); err != nil {
		return fmt.Errorf("snapshot '%s' not found for cluster '%s'", name, cfg.Cluster)
	}
	if out == "" {
		out = cfg.Cluster + "-" + name + ".k3csnap"
	}
	if snapshotIsWarm(dir) {
		logger.Info("machine state is machine-specific and not exported; the archive restores cold")
	}

	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()
	// skipping the holes leaves time for a better compression level
	zw, err := zstd.NewWriter(f, zstd.WithEncoderLevel(zstd.SpeedBetterCompression))
	if err != nil {
		return err
	}
	tw := tar.NewWriter(zw)

	manifest := fmt.Sprintf("cluster: %s\nsnapshot: %s\nexported: %s\n",
		cfg.Cluster, name, time.Now().Format(time.RFC3339))
	if err := writeTarBytes(tw, exportManifest, []byte(manifest)); err != nil {
		return err
	}

	for _, fileName := range exportable(dir) {
		path := filepath.Join(dir, fileName)
		data := []byte(nil)
		if fileName == "meta.yaml" {
			// the export drops the machine state, so the archive is cold
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			data = []byte(strings.ReplaceAll(string(raw), "mode: warm", "mode: cold"))
		}
		if data != nil {
			if err := writeTarBytes(tw, fileName, data); err != nil {
				return err
			}
			continue
		}
		if err := writeTarSparse(tw, fileName, path); err != nil {
			return err
		}
	}

	if err := tw.Close(); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if info, err := os.Stat(out); err == nil {
		logger.Info(fmt.Sprintf("snapshot '%s' exported to %s (%.1f GB)", name, out, float64(info.Size())/1e9))
	}
	return nil
}

func snapshotIsWarm(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "server-"+vmstateFile))
	return err == nil
}

func writeTarBytes(tw *tar.Writer, name string, data []byte) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data))}); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

// Sparse stream entries (NAME.sparse): the rootfs images are huge sparse
// files (512 GiB logical, tens of GB allocated); APFS reports the
// allocated ranges via SEEK_DATA/SEEK_HOLE, and only those are stored:
//
//	magic "K3CSPARSE1", file size (8 bytes BE),
//	then per segment: offset (8 BE), length (8 BE), data
const sparseSuffix = ".sparse"
const sparseMagic = "K3CSPARSE1"

// dataRanges enumerates a file's allocated (offset, length) ranges.
func dataRanges(f *os.File, size int64) ([][2]int64, error) {
	var ranges [][2]int64
	offset := int64(0)
	for offset < size {
		dataStart, err := unix.Seek(int(f.Fd()), offset, unix.SEEK_DATA)
		if err == unix.ENXIO {
			break // only holes remain
		}
		if err != nil {
			return nil, err
		}
		holeStart, err := unix.Seek(int(f.Fd()), dataStart, unix.SEEK_HOLE)
		if err != nil {
			return nil, err
		}
		ranges = append(ranges, [2]int64{dataStart, holeStart - dataStart})
		offset = holeStart
	}
	return ranges, nil
}

func writeTarSparse(tw *tar.Writer, name, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	ranges, err := dataRanges(f, info.Size())
	if err != nil {
		return err
	}
	var dataBytes int64
	for _, r := range ranges {
		dataBytes += r[1]
	}
	logger.Info(fmt.Sprintf("packing %s (%.1f GB data of %.1f GB)",
		name, float64(dataBytes)/1e9, float64(info.Size())/1e9))

	entrySize := int64(len(sparseMagic)) + 8 + int64(len(ranges))*16 + dataBytes
	if err := tw.WriteHeader(&tar.Header{Name: name + sparseSuffix, Mode: 0o644, Size: entrySize}); err != nil {
		return err
	}
	if _, err := tw.Write([]byte(sparseMagic)); err != nil {
		return err
	}
	if err := writeBE(tw, info.Size()); err != nil {
		return err
	}
	prog := newProgress("packing "+name, dataBytes)
	for _, r := range ranges {
		if err := writeBE(tw, r[0]); err != nil {
			return err
		}
		if err := writeBE(tw, r[1]); err != nil {
			return err
		}
		if _, err := f.Seek(r[0], io.SeekStart); err != nil {
			return err
		}
		if _, err := io.CopyN(io.MultiWriter(tw, prog), f, r[1]); err != nil {
			return err
		}
	}
	return nil
}

func writeBE(w io.Writer, v int64) error {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(v))
	_, err := w.Write(buf[:])
	return err
}

// readSparseStream reconstructs a sparse-stream entry as a sparse file.
// entrySize is the tar entry size, used for progress reporting.
func readSparseStream(path string, r io.Reader, entrySize int64) error {
	magic := make([]byte, len(sparseMagic))
	if _, err := io.ReadFull(r, magic); err != nil || string(magic) != sparseMagic {
		return fmt.Errorf("corrupt sparse entry in archive")
	}
	size, err := readBE(r)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	prog := newProgress("unpacking "+filepath.Base(path), entrySize)
	var gaps [][2]int64
	prevEnd := int64(0)
	for {
		offset, err := readBE(r)
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		length, err := readBE(r)
		if err != nil {
			return err
		}
		if offset < prevEnd || length < 0 || offset+length > size {
			return fmt.Errorf("corrupt sparse entry: segment %d+%d (size %d)", offset, length, size)
		}
		if offset > prevEnd {
			gaps = append(gaps, [2]int64{prevEnd, offset})
		}
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return err
		}
		if _, err := io.CopyN(io.MultiWriter(f, prog), r, length); err != nil {
			return err
		}
		prevEnd = offset + length
	}
	if err := f.Truncate(size); err != nil {
		return err
	}
	// punch the gaps only after the writes are flushed: APFS's delayed
	// allocation zero-fills around scattered writes at write-back time,
	// which would re-materialize holes punched earlier
	if err := f.Sync(); err != nil {
		return err
	}
	for _, g := range gaps {
		punchHole(f, g[0], g[1])
	}
	punchHole(f, prevEnd, size)
	return f.Close()
}

// punchHole deallocates [start, end) of f. APFS zero-fills generously
// around scattered writes (a quarter-MB cluster per touched block), so
// without explicit hole punching a reconstructed image occupies a
// multiple of its data. Failures are ignored — the file is then merely
// less sparse, not incorrect.
func punchHole(f *os.File, start, end int64) {
	const block = 4096
	start = (start + block - 1) &^ (block - 1)
	end = end &^ (block - 1)
	if end-start < block {
		return
	}
	// struct fpunchhole_t{fp_flags, reserved uint32; fp_offset, fp_length off_t}
	hole := struct {
		Flags    uint32
		Reserved uint32
		Offset   int64
		Length   int64
	}{Offset: start, Length: end - start}
	_, _ = unix.FcntlInt(f.Fd(), unix.F_PUNCHHOLE, int(uintptr(unsafe.Pointer(&hole))))
}

func readBE(r io.Reader) (int64, error) {
	var buf [8]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return 0, err
	}
	return int64(binary.BigEndian.Uint64(buf[:])), nil
}

// progress logs a line every 10% of total bytes passing through.
type progress struct {
	label   string
	total   int64
	done    int64
	nextPct int64
}

func newProgress(label string, total int64) *progress {
	return &progress{label: label, total: total, nextPct: 10}
}

func (p *progress) Write(b []byte) (int, error) {
	p.add(int64(len(b)))
	return len(b), nil
}

func (p *progress) add(n int64) {
	p.done += n
	if p.total <= 0 {
		return
	}
	if pct := p.done * 100 / p.total; pct >= p.nextPct {
		logger.Info(fmt.Sprintf("%s: %d%%", p.label, pct))
		for p.nextPct <= pct {
			p.nextPct += 10
		}
	}
}

// SnapshotImport unpacks an exported snapshot archive for the cluster.
// The snapshot's host-side share content (registries, CA bundle) is taken
// from the importing cluster's configuration, not from the archive; the
// k3s admin kubeconfig is regenerated by k3s on the first boot.
func SnapshotImport(cfg *config.Config, file, name string) error {
	if _, err := os.Stat(cfg.K3sEtcDir()); err != nil {
		return fmt.Errorf("cluster '%s' has no node config yet; create the cluster first", cfg.Cluster)
	}

	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	zr, err := zstd.NewReader(f)
	if err != nil {
		return fmt.Errorf("%s is not a k3c snapshot export: %w", file, err)
	}
	defer zr.Close()

	tmp, err := os.MkdirTemp(filepath.Join(cfg.BaseDir, "snapshots"), ".import-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	manifestName := ""
	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		switch hdr.Name {
		case exportManifest:
			data, err := io.ReadAll(io.LimitReader(tr, 1<<16))
			if err != nil {
				return err
			}
			for _, line := range strings.Split(string(data), "\n") {
				if v, ok := strings.CutPrefix(line, "snapshot: "); ok {
					manifestName = strings.TrimSpace(v)
				}
			}
		case "meta.yaml", serverRootfs, registryRootfs:
			logger.Info(fmt.Sprintf("unpacking %s (%.1f GB)", hdr.Name, float64(hdr.Size)/1e9))
			if err := writeSparseFile(filepath.Join(tmp, hdr.Name), tr, hdr.Size); err != nil {
				return err
			}
		case serverRootfs + sparseSuffix, registryRootfs + sparseSuffix:
			base := strings.TrimSuffix(hdr.Name, sparseSuffix)
			logger.Info(fmt.Sprintf("unpacking %s (%.1f GB data)", base, float64(hdr.Size)/1e9))
			if err := readSparseStream(filepath.Join(tmp, base), tr, hdr.Size); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unexpected entry in archive: %s", hdr.Name)
		}
	}
	if _, err := os.Stat(filepath.Join(tmp, serverRootfs)); err != nil {
		return fmt.Errorf("%s contains no server root filesystem", file)
	}

	if name == "" {
		name = manifestName
	}
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
	}
	if err := validSnapshotName(name); err != nil {
		return err
	}
	dir := snapshotDir(cfg, name)
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("snapshot '%s' already exists for cluster '%s'", name, cfg.Cluster)
	}

	// the restore copies k3s-etc onto the share: use this cluster's own
	// node config; k3s.yaml is omitted so k3s writes a fresh kubeconfig
	// matching the restored cluster's CA on the first boot
	if err := copyDir(cfg.K3sEtcDir(), filepath.Join(tmp, "k3s-etc")); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(tmp, "k3s-etc", "k3s.yaml"))

	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmp, dir); err != nil {
		return err
	}
	logger.Info("snapshot '" + name + "' imported for cluster '" + cfg.Cluster + "' (restore with: k3c snapshot restore " + name + ")")
	return nil
}

// writeSparseFile writes r to path, seeking over zero blocks so large
// mostly-empty disk images do not materialize on disk.
func writeSparseFile(path string, r io.Reader, size int64) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	buf := make([]byte, 1<<20)
	zero := make([]byte, 1<<20)
	prog := newProgress("unpacking "+filepath.Base(path), size)
	var offset int64
	for offset < size {
		n, err := io.ReadFull(r, buf)
		if err == io.ErrUnexpectedEOF || err == io.EOF {
			if n == 0 {
				break
			}
		} else if err != nil {
			return err
		}
		chunk := buf[:n]
		if bytes.Equal(chunk, zero[:n]) {
			if _, err := f.Seek(int64(n), io.SeekCurrent); err != nil {
				return err
			}
		} else {
			if _, err := f.Write(chunk); err != nil {
				return err
			}
		}
		offset += int64(n)
		prog.add(int64(n))
	}
	if err := f.Truncate(size); err != nil {
		return err
	}
	return f.Close()
}
