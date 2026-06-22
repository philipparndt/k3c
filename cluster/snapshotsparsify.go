package cluster

// reSparsifySnapshot punches holes over zeroed blocks in a snapshot's rootfs
// clone, returning the number of bytes reclaimed. It runs in the background
// phase of a save (after the cluster resumes) and only shrinks the snapshot:
// failures leave it merely less sparse, never incorrect. Idempotent.
//
// CONTRACT STUB — real implementation is task group 2. Reuse punchHole and the
// SEEK_DATA/SEEK_HOLE machinery in transfer.go (same package).
func reSparsifySnapshot(rootfsPath string) (reclaimed int64, err error) {
	return 0, nil
}
