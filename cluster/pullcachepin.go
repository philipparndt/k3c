package cluster

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"k3c/config"
)

// Snapshot pin/retention: a frozen snapshot depends on its image closure
// staying in the host pull-cache. A pin durably records the digests a
// snapshot needs; pull-cache retention treats the union of all pins as live
// and evicts only the unpinned, expired complement. The pin protects local
// thaw, fat export, and retention. See openspec/specs/registry-and-pull-cache.
//
// On-disk layout: one plain-text file per snapshot under
// <pull-cache>/pins/<sanitized-id>.txt, one digest ("sha256:...") per line.
// A per-snapshot file (rather than a central index) is crash-safe: writes are
// temp+rename, a release is a single unlink, and a torn write of one pin can
// never corrupt another snapshot's pin. Retention unions the files at GC time.

// pinDir is the directory holding the per-snapshot pin files.
func pinDir(cfg *config.Config) string {
	return filepath.Join(pullCacheDir(cfg), "pins")
}

// pinFileNameRe restricts a sanitized pin id to filesystem-safe characters;
// any other byte in the id is replaced with '_' so e.g. "cluster/snapshot"
// becomes "cluster_snapshot.txt".
var pinFileNameRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func pinFilePath(cfg *config.Config, pinID string) string {
	name := pinFileNameRe.ReplaceAllString(pinID, "_")
	return filepath.Join(pinDir(cfg), name+".txt")
}

// normalizeDigest trims a digest and keeps it in the same form the pull-cache
// names blobs with: the full "sha256:<hex>" string (see pullCache.blobPath and
// the prune sweep, which matches files by that exact prefix). Inputs already in
// that form pass through; a bare hex digest is given the "sha256:" prefix so
// pinned digests always key the blob store directly. An empty/unrecognized
// digest returns "" and is dropped by the callers.
var hexDigestRe = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

func normalizeDigest(d string) string {
	d = strings.TrimSpace(d)
	if d == "" {
		return ""
	}
	if strings.HasPrefix(d, "sha256:") {
		return d
	}
	if hexDigestRe.MatchString(d) {
		return "sha256:" + strings.ToLower(d)
	}
	// Unknown algorithm prefixes (e.g. "sha512:...") are kept verbatim so they
	// still match a blob named the same way; anything else is passed through
	// too rather than silently dropped, leaving the decision to the caller.
	return d
}

// snapshotPinID identifies a snapshot's pin record (stable across runs).
func snapshotPinID(clusterName, snapName string) string {
	return clusterName + "/" + snapName
}

// pinSnapshotImages durably records digests (manifest + config + layer) as
// pinned by the given snapshot. Idempotent: it fully replaces the snapshot's
// pin file (write temp + rename) so re-running a save converges. Must be
// committed before any cosmetic shrink step so an interrupted save never
// leaves a broken thaw.
func pinSnapshotImages(pinID string, digests []string) error {
	cfg, err := config.Resolve("", "")
	if err != nil {
		return err
	}
	return pinSnapshotImagesIn(cfg, pinID, digests)
}

// pinSnapshotImagesIn is the config-injected core, kept separate so tests can
// drive a temp pull-cache without touching the user's real config.
func pinSnapshotImagesIn(cfg *config.Config, pinID string, digests []string) error {
	if err := os.MkdirAll(pinDir(cfg), 0o755); err != nil {
		return err
	}
	// dedup + sort for a stable, idempotent on-disk record
	set := map[string]struct{}{}
	for _, d := range digests {
		if n := normalizeDigest(d); n != "" {
			set[n] = struct{}{}
		}
	}
	sorted := make([]string, 0, len(set))
	for d := range set {
		sorted = append(sorted, d)
	}
	sort.Strings(sorted)

	path := pinFilePath(cfg, pinID)
	tmp, err := os.CreateTemp(pinDir(cfg), ".pin-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()
	var body strings.Builder
	for _, d := range sorted {
		body.WriteString(d)
		body.WriteByte('\n')
	}
	if _, err := tmp.WriteString(body.String()); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	committed = true
	return nil
}

// releaseSnapshotPin removes a snapshot's pin (called on snapshot delete). It
// is not an error if the pin file is absent, so deleting a snapshot that never
// pinned anything (or releasing twice) is safe.
func releaseSnapshotPin(pinID string) error {
	cfg, err := config.Resolve("", "")
	if err != nil {
		return err
	}
	return releaseSnapshotPinIn(cfg, pinID)
}

func releaseSnapshotPinIn(cfg *config.Config, pinID string) error {
	if err := os.Remove(pinFilePath(cfg, pinID)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// renameSnapshotPin moves a snapshot's pin to a new id (called on snapshot
// rename), preserving its pinned digests. It is not an error if the old pin
// file is absent (a warm/cold snapshot never pinned anything), so renaming
// such a snapshot is a no-op here.
func renameSnapshotPin(oldID, newID string) error {
	cfg, err := config.Resolve("", "")
	if err != nil {
		return err
	}
	return renameSnapshotPinIn(cfg, oldID, newID)
}

func renameSnapshotPinIn(cfg *config.Config, oldID, newID string) error {
	if err := os.Rename(pinFilePath(cfg, oldID), pinFilePath(cfg, newID)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// pinnedDigests returns the union of all snapshots' pinned digests, for
// pull-cache retention to treat as live. The keys are the same "sha256:..."
// strings the pull-cache names blobs with, so a caller can test membership
// against a blob file name directly.
func pinnedDigests() (map[string]struct{}, error) {
	cfg, err := config.Resolve("", "")
	if err != nil {
		return nil, err
	}
	return pinnedDigestsIn(cfg)
}

func pinnedDigestsIn(cfg *config.Config) (map[string]struct{}, error) {
	union := map[string]struct{}{}
	entries, err := os.ReadDir(pinDir(cfg))
	if err != nil {
		if os.IsNotExist(err) {
			return union, nil
		}
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".txt") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(pinDir(cfg), e.Name()))
		if err != nil {
			if os.IsNotExist(err) {
				continue // released concurrently; fine
			}
			return nil, err
		}
		for _, line := range strings.Split(string(data), "\n") {
			if d := normalizeDigest(line); d != "" {
				union[d] = struct{}{}
			}
		}
	}
	return union, nil
}
