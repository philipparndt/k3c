package cluster

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
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

// FrozenExportMode selects how much of a frozen snapshot's images travel in
// its export archive — a size vs. self-containment dial:
//
//   - fat:  self-contained — bundles the local-only images AND the recoverable
//     image-blob closure (loose files from the host pull-cache). Imports offline.
//   - slim: bundles only the local-only images (pushed to the local registry or
//     `image import`ed); recoverable images re-pull from the target's registries.
//   - thin: bundles no images at all; only safe when the cluster has no
//     local-only images (every image re-pulls on import).
type FrozenExportMode string

const (
	FrozenFat  FrozenExportMode = "fat"
	FrozenSlim FrozenExportMode = "slim"
	FrozenThin FrozenExportMode = "thin"
)

// SnapshotExport writes a snapshot to a portable archive (tar+zstd). A warm
// or cold snapshot exports its disk image (always restoring cold). A frozen
// snapshot exports as a logical bundle in the requested FrozenExportMode
// (default fat).
func SnapshotExport(cfg *config.Config, name, out string, mode FrozenExportMode) error {
	if err := validSnapshotName(name); err != nil {
		return err
	}
	dir := snapshotDir(cfg, name)
	if snapshotModeOf(dir) == ModeFrozen {
		return exportFrozen(cfg, name, out, mode)
	}
	if _, err := os.Stat(filepath.Join(dir, serverRootfs)); err != nil {
		return fmt.Errorf("snapshot '%s' not found for cluster '%s'", name, cfg.Cluster)
	}
	if mode == FrozenThin || mode == FrozenSlim {
		logger.Info("--thin/--slim only apply to frozen snapshots; ignoring for this disk-image export")
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

// frozenBlobPrefix names the loose pull-cache blobs bundled in a fat frozen
// export. On import they are content-addressed into the target pull-cache.
const frozenBlobPrefix = "blobs/"

// exportFrozen writes a frozen snapshot as a portable logical bundle in the
// requested mode. All modes ship the datastore + PVC data + certs + manifest.
// Fat and slim additionally bundle the local-only image archive (images not
// recoverable from a remote registry); fat also bundles the recoverable
// blob closure as loose files from the host pull-cache. Thin ships no images.
func exportFrozen(cfg *config.Config, name, out string, mode FrozenExportMode) error {
	if mode == "" {
		mode = FrozenFat
	}
	dir := snapshotDir(cfg, name)
	if _, err := os.Stat(filepath.Join(dir, frozenStateTar)); err != nil {
		return fmt.Errorf("frozen snapshot '%s' is incomplete (no datastore) for cluster '%s'", name, cfg.Cluster)
	}
	if out == "" {
		out = cfg.Cluster + "-" + name + ".k3csnap"
	}
	manifest, err := readFrozenManifest(filepath.Join(dir, frozenManifestF))
	if err != nil {
		return fmt.Errorf("reading frozen image manifest: %w", err)
	}
	switch mode {
	case FrozenThin:
		logger.Info("exporting thin frozen bundle (no images; everything re-pulls on import)")
		if len(manifest.LocalImages) > 0 {
			logger.Warn(fmt.Sprintf("thin bundle omits %d local-only image(s) that cannot be re-pulled; use --slim to include them", len(manifest.LocalImages)))
		}
	case FrozenSlim:
		logger.Info(fmt.Sprintf("exporting slim frozen bundle (%d local-only image(s); remote images re-pull on import)", len(manifest.LocalImages)))
	default:
		logger.Info(fmt.Sprintf("exporting fat frozen bundle (%d local-only image(s) + %d recoverable blob(s) from the pull-cache)", len(manifest.LocalImages), len(manifest.Digests)))
	}

	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()
	zw, err := zstd.NewWriter(f, zstd.WithEncoderLevel(zstd.SpeedBetterCompression))
	if err != nil {
		return err
	}
	tw := tar.NewWriter(zw)

	mfst := fmt.Sprintf("cluster: %s\nsnapshot: %s\nexported: %s\ntier: %s\n",
		cfg.Cluster, name, time.Now().Format(time.RFC3339), "frozen-"+string(mode))
	if err := writeTarBytes(tw, exportManifest, []byte(mfst)); err != nil {
		return err
	}

	// The logical extract files (small relative to the blobs).
	for _, fileName := range []string{"meta.yaml", frozenStateTar, frozenStorageTar, frozenCertsTar, frozenManifestF} {
		data, err := os.ReadFile(filepath.Join(dir, fileName))
		if err != nil {
			return fmt.Errorf("reading %s for export: %w", fileName, err)
		}
		if err := writeTarBytes(tw, fileName, data); err != nil {
			return err
		}
	}

	// Fat and slim ship the local-only images (not recoverable from any
	// remote registry); only thin omits them.
	localBundled := false
	if mode != FrozenThin {
		localPath := filepath.Join(dir, frozenLocalImagesTar)
		if _, err := os.Stat(localPath); err == nil {
			if err := writeTarFile(tw, frozenLocalImagesTar, localPath); err != nil {
				return err
			}
			localBundled = true
		}
	}

	// Fat additionally ships the recoverable blob closure as loose files from
	// the pull-cache, so the bundle imports fully offline.
	if mode == FrozenFat {
		blobs := filepath.Join(pullCacheDir(cfg), "blobs")
		var missing []string
		for _, d := range manifest.Digests {
			path := filepath.Join(blobs, d)
			if _, err := os.Stat(path); err != nil {
				missing = append(missing, d)
				continue
			}
			if err := writeTarFile(tw, frozenBlobPrefix+d, path); err != nil {
				return err
			}
		}
		if len(missing) > 0 {
			// Digests carried in the bundled local-images archive are
			// intentionally absent from the pull-cache — not an error.
			if localBundled {
				logger.Warn(fmt.Sprintf("fat export: %d digest(s) not in the pull-cache; assuming they belong to the bundled local images", len(missing)))
			} else {
				preview := missing
				if len(preview) > 5 {
					preview = preview[:5]
				}
				return fmt.Errorf("fat export incomplete: %d pinned blob(s) are missing from the pull-cache (e.g. %s); the pin should keep them — re-pull, or export --slim",
					len(missing), strings.Join(preview, ", "))
			}
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
		logger.Info(fmt.Sprintf("snapshot '%s' exported to %s (%.2f GB)", name, out, float64(info.Size())/1e9))
	}
	return nil
}

// writeTarFile streams a regular file into the tar as a plain entry.
func writeTarFile(tw *tar.Writer, name, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: info.Size()}); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
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

// ArchiveInfo is the metadata read from an export archive without unpacking
// it: enough to create a matching cluster and locate the snapshot to restore.
type ArchiveInfo struct {
	Cluster     string
	Snapshot    string
	Mode        string
	ClusterCIDR string
	ServiceCIDR string
}

// SnapshotArchiveInfo peeks an export archive for its export.yaml + meta.yaml
// (written first, so it stops before streaming the large blobs) and returns
// the originating cluster name, snapshot name, tier, and CIDRs.
func SnapshotArchiveInfo(file string) (ArchiveInfo, error) {
	var info ArchiveInfo
	f, err := os.Open(file)
	if err != nil {
		return info, err
	}
	defer f.Close()
	zr, err := zstd.NewReader(f)
	if err != nil {
		return info, fmt.Errorf("%s is not a k3c snapshot export: %w", file, err)
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	gotExport, gotMeta := false, false
	for !(gotExport && gotMeta) {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return info, err
		}
		switch hdr.Name {
		case exportManifest:
			data, _ := io.ReadAll(io.LimitReader(tr, 1<<16))
			for _, line := range strings.Split(string(data), "\n") {
				if v, ok := strings.CutPrefix(line, "cluster: "); ok {
					info.Cluster = strings.TrimSpace(v)
				}
				if v, ok := strings.CutPrefix(line, "snapshot: "); ok {
					info.Snapshot = strings.TrimSpace(v)
				}
			}
			gotExport = true
		case "meta.yaml":
			data, _ := io.ReadAll(io.LimitReader(tr, 1<<16))
			for _, line := range strings.Split(string(data), "\n") {
				if v, ok := strings.CutPrefix(line, "mode: "); ok {
					info.Mode = strings.TrimSpace(v)
				}
				if v, ok := strings.CutPrefix(line, "clusterCidr: "); ok {
					info.ClusterCIDR = strings.TrimSpace(v)
				}
				if v, ok := strings.CutPrefix(line, "serviceCidr: "); ok {
					info.ServiceCIDR = strings.TrimSpace(v)
				}
			}
			gotMeta = true
		}
	}
	return info, nil
}

// ClusterImportRun creates a fresh cluster sized to the archive (CIDRs already
// applied to cfg by the caller), imports the archive into it, and restores it —
// the one-step path to bring up a cluster from an exported snapshot on a
// machine that does not have it yet.
func ClusterImportRun(cfg *config.Config, file string) error {
	if containerExists(cfg.ServerName, false) {
		return fmt.Errorf("cluster '%s' already exists; import into it with 'k3c snapshot import' + 'restore', or delete it first", cfg.Cluster)
	}
	info, err := SnapshotArchiveInfo(file)
	if err != nil {
		return err
	}
	snap := info.Snapshot
	if snap == "" {
		snap = strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
	}
	if err := validSnapshotName(snap); err != nil {
		return err
	}
	logger.Info(fmt.Sprintf("creating cluster '%s' (cidrs %s / %s) to import %s",
		cfg.Cluster, cfg.ClusterCIDR, cfg.ServiceCIDR, filepath.Base(file)))
	if err := Create(cfg); err != nil {
		return err
	}
	if err := SnapshotImport(cfg, file, snap); err != nil {
		return err
	}
	logger.Info("restoring imported snapshot '" + snap + "'")
	return SnapshotRestore(cfg, snap, false)
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

	snapBase := filepath.Join(cfg.BaseDir, "snapshots")
	if err := os.MkdirAll(snapBase, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(snapBase, ".import-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	manifestName := ""
	frozen := false
	seededBlobs := 0
	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		// Fat frozen bundles ship the image-blob closure as loose,
		// content-addressed files; seed them straight into the target
		// pull-cache, skipping any the cache already has (dedup).
		if strings.HasPrefix(hdr.Name, frozenBlobPrefix) {
			frozen = true
			digest := strings.TrimPrefix(hdr.Name, frozenBlobPrefix)
			seeded, err := seedPullCacheBlob(cfg, digest, tr, hdr.Size)
			if err != nil {
				return err
			}
			if seeded {
				seededBlobs++
			}
			continue
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
				if v, ok := strings.CutPrefix(line, "tier: "); ok {
					if strings.HasPrefix(strings.TrimSpace(v), "frozen") {
						frozen = true
					}
				}
			}
		case frozenStateTar, frozenStorageTar, frozenCertsTar, frozenManifestF, frozenLocalImagesTar:
			frozen = true
			logger.Info(fmt.Sprintf("unpacking %s (%.2f GB)", hdr.Name, float64(hdr.Size)/1e9))
			if err := writeRegularFile(filepath.Join(tmp, hdr.Name), tr); err != nil {
				return err
			}
		case "meta.yaml":
			if err := writeRegularFile(filepath.Join(tmp, hdr.Name), tr); err != nil {
				return err
			}
		case serverRootfs, registryRootfs:
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
	if frozen {
		if _, err := os.Stat(filepath.Join(tmp, frozenStateTar)); err != nil {
			return fmt.Errorf("%s is a frozen bundle but contains no datastore", file)
		}
		if seededBlobs > 0 {
			logger.Info(fmt.Sprintf("seeded %d new image blob(s) into the pull-cache", seededBlobs))
		}
	} else if _, err := os.Stat(filepath.Join(tmp, serverRootfs)); err != nil {
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

	// A frozen bundle carries no rootfs and seeds k3s-etc from the importing
	// cluster on restore (thaw), so the import only stages the logical files.
	if !frozen {
		// the restore copies k3s-etc onto the share: use this cluster's own
		// node config; k3s.yaml is omitted so k3s writes a fresh kubeconfig
		// matching the restored cluster's CA on the first boot
		if err := copyDir(cfg.K3sEtcDir(), filepath.Join(tmp, "k3s-etc")); err != nil {
			return err
		}
		_ = os.Remove(filepath.Join(tmp, "k3s-etc", "k3s.yaml"))
	}

	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmp, dir); err != nil {
		return err
	}
	logger.Info("snapshot '" + name + "' imported for cluster '" + cfg.Cluster + "' (restore with: k3c snapshot restore " + name + ")")
	return nil
}

// writeRegularFile writes r verbatim to path (used for the small frozen
// logical files, which are not sparse).
func writeRegularFile(path string, r io.Reader) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// seedPullCacheBlob writes one fat-bundle blob into the target pull-cache,
// content-addressed: it verifies the bytes hash to the claimed digest and
// skips a blob the cache already holds (dedup). Returns whether it seeded a
// new blob.
func seedPullCacheBlob(cfg *config.Config, digest string, r io.Reader, size int64) (bool, error) {
	if !strings.HasPrefix(digest, "sha256:") {
		return false, fmt.Errorf("bundle blob has a non-sha256 digest: %q", digest)
	}
	blobs := filepath.Join(pullCacheDir(cfg), "blobs")
	if err := os.MkdirAll(blobs, 0o755); err != nil {
		return false, err
	}
	dst := filepath.Join(blobs, digest)
	if _, err := os.Stat(dst); err == nil {
		// already present (content-addressed): drain and skip
		if _, err := io.Copy(io.Discard, r); err != nil {
			return false, err
		}
		return false, nil
	}
	tmp, err := os.CreateTemp(blobs, ".seed-*")
	if err != nil {
		return false, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	hasher := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, hasher), r); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	if got := "sha256:" + hex.EncodeToString(hasher.Sum(nil)); got != digest {
		return false, fmt.Errorf("bundle blob %s is corrupt (hashes to %s)", digest, got)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return false, err
	}
	return true, nil
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
