package cluster

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"syscall"

	"github.com/philipparndt/go-logger"
)

// sparsifyBlock is the granularity at which we scan for and punch zeroed
// regions. It matches punchHole's alignment (4 KiB), the ext4 block size, and
// APFS's allocation unit, so a punched run actually frees backing store.
const sparsifyBlock = 4096

// reSparsifySnapshot punches holes over zeroed blocks in a snapshot's rootfs
// clone, returning the number of bytes reclaimed. It runs in the background
// phase of a save (after the cluster resumes) and only shrinks the snapshot:
// failures leave it merely less sparse, never incorrect. Idempotent.
//
// It reuses the SEEK_DATA/SEEK_HOLE machinery (dataRanges) and punchHole from
// transfer.go. Only already-allocated extents are scanned — holes are skipped
// for free — and within those extents each maximal run of 4 KiB-aligned
// all-zero blocks is deallocated. Re-running finds the previously punched runs
// already as holes (outside dataRanges), so it reclaims nothing more.
func reSparsifySnapshot(rootfsPath string) (reclaimed int64, err error) {
	f, err := os.OpenFile(rootfsPath, os.O_RDWR, 0)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	before := allocatedBytes(info)

	ranges, err := dataRanges(f, info.Size())
	if err != nil {
		return 0, err
	}

	// Reuse one zero comparison buffer and one read buffer for the whole scan.
	const chunk = 1 << 20 // 1 MiB, a multiple of sparsifyBlock
	buf := make([]byte, chunk)
	zero := make([]byte, chunk)

	var reclaimedRanges int64
	for _, r := range ranges {
		rc, err := sparsifyRange(f, r[0], r[1], buf, zero)
		if err != nil {
			// A read error on one extent must not abort the pass — the file is
			// only ever made more sparse, never corrupted, so log and continue.
			logger.Debug(fmt.Sprintf("re-sparsify: skipping extent %d+%d: %v", r[0], r[1], err))
			continue
		}
		reclaimedRanges += rc
	}

	// Prefer the kernel's own accounting (allocated-block delta) when it is
	// available and sane; fall back to the byte count of the punched runs.
	if err := f.Sync(); err == nil {
		if info2, err := f.Stat(); err == nil {
			if delta := before - allocatedBytes(info2); delta > 0 {
				reclaimed = delta
			}
		}
	}
	if reclaimed == 0 {
		reclaimed = reclaimedRanges
	}

	if reclaimed > 0 {
		logger.Info(fmt.Sprintf("re-sparsified %s: reclaimed %.2f GB",
			rootfsPath, float64(reclaimed)/1e9))
	} else {
		logger.Debug("re-sparsify " + rootfsPath + ": nothing to reclaim")
	}
	return reclaimed, nil
}

// sparsifyRange scans the allocated extent [start, start+length) for maximal
// 4 KiB-aligned all-zero runs and punches each one out. It returns the number
// of bytes punched. buf/zero are caller-provided scratch buffers (same length).
func sparsifyRange(f *os.File, start, length int64, buf, zero []byte) (int64, error) {
	var reclaimed int64
	end := start + length
	// runStart tracks the beginning of the current contiguous zero run (-1 = none).
	runStart := int64(-1)
	offset := start

	flush := func(runEnd int64) {
		if runStart < 0 {
			return
		}
		// Align inward to whole blocks; punchHole re-applies the same alignment
		// but doing it here lets us account the bytes accurately.
		s := (runStart + sparsifyBlock - 1) &^ (sparsifyBlock - 1)
		e := runEnd &^ (sparsifyBlock - 1)
		if e-s >= sparsifyBlock {
			punchHole(f, s, e)
			reclaimed += e - s
		}
		runStart = -1
	}

	for offset < end {
		n := int64(len(buf))
		if rem := end - offset; rem < n {
			n = rem
		}
		read, err := f.ReadAt(buf[:n], offset)
		if read > 0 {
			scanZeroBlocks(buf[:read], zero, offset, &runStart, flush)
			offset += int64(read)
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			flush(offset)
			return reclaimed, err
		}
	}
	flush(end)
	return reclaimed, nil
}

// scanZeroBlocks walks data (covering file offsets [base, base+len(data))) in
// 4 KiB blocks. It extends/terminates the current zero run, calling flush(end)
// at the file offset where a run ends (i.e. the first non-zero block start).
func scanZeroBlocks(data, zero []byte, base int64, runStart *int64, flush func(int64)) {
	for i := 0; i < len(data); i += sparsifyBlock {
		blkEnd := i + sparsifyBlock
		if blkEnd > len(data) {
			blkEnd = len(data) // trailing partial block (only at EOF)
		}
		blk := data[i:blkEnd]
		blockOffset := base + int64(i)
		// bytes.Equal against a same-length zero slice is SIMD-accelerated,
		// the same fast path writeSparseFile uses for its zero check.
		isZero := bytes.Equal(blk, zero[:len(blk)])
		switch {
		case isZero && *runStart < 0:
			*runStart = blockOffset
		case !isZero && *runStart >= 0:
			flush(blockOffset)
		}
	}
}

// allocatedBytes reports the on-disk allocated size of a stat result, measured
// in 512-byte units (st_blocks, the POSIX convention). Returns 0 if the
// platform stat is unavailable, so callers fall back to their own accounting.
func allocatedBytes(info os.FileInfo) int64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return int64(st.Blocks) * 512
	}
	return 0
}
