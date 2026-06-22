package cluster

// SnapshotMode is the storage tier of a snapshot. The tiers form a
// size/restore-speed dial: warm carries the VM RAM image and resumes in
// place; cold clones the full disk and boots fresh; frozen is a logical
// extract (datastore + persistent-volume data + image manifest) that drops
// the reconstructable image store and rehydrates it from the pull-cache on
// restore. See openspec/specs/snapshots for the tier model.
type SnapshotMode string

const (
	ModeWarm   SnapshotMode = "warm"
	ModeCold   SnapshotMode = "cold"
	ModeFrozen SnapshotMode = "frozen"
)

// snapshotModeOf reads the tier recorded in a snapshot's meta.yaml,
// defaulting to cold when absent (the safe, self-contained tier).
func snapshotModeOf(dir string) SnapshotMode {
	switch snapshotMetaValue(dir, "mode") {
	case string(ModeWarm):
		return ModeWarm
	case string(ModeFrozen):
		return ModeFrozen
	default:
		return ModeCold
	}
}
