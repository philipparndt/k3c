package cluster

// Snapshot pin/retention: a frozen snapshot depends on its image closure
// staying in the host pull-cache. A pin durably records the digests a
// snapshot needs; pull-cache retention treats the union of all pins as live
// and evicts only the unpinned, expired complement. The pin protects local
// thaw, fat export, and retention. See openspec/specs/registry-and-pull-cache.

// snapshotPinID identifies a snapshot's pin record (stable across runs).
func snapshotPinID(clusterName, snapName string) string {
	return clusterName + "/" + snapName
}

// pinSnapshotImages durably records digests (manifest + config + layer) as
// pinned by the given snapshot. Idempotent. Must be committed before any
// cosmetic shrink step so an interrupted save never leaves a broken thaw.
//
// CONTRACT STUB — real implementation is task group 3.
func pinSnapshotImages(pinID string, digests []string) error { return nil }

// releaseSnapshotPin removes a snapshot's pin (called on snapshot delete).
//
// CONTRACT STUB — real implementation is task group 3.
func releaseSnapshotPin(pinID string) error { return nil }

// pinnedDigests returns the union of all snapshots' pinned digests, for
// pull-cache retention to treat as live.
//
// CONTRACT STUB — real implementation is task group 3.
func pinnedDigests() (map[string]struct{}, error) { return map[string]struct{}{}, nil }
