package cluster

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/philipparndt/go-logger"

	"k3c/config"
)

// snapshotTarget describes what a snapshottable VM differs by, so one engine can
// save (and, in a later phase, restore) both a k3s cluster and the docker
// sidecar. The differences are almost entirely data — artifact filenames, the
// warm machine-state prefix, and where each artifact's live copy lives — so a
// target is a struct of values and small source closures rather than an
// interface hierarchy (see the unify-snapshot-lifecycle-engine change, D1).
//
// Meta-file writing stays in the two adapters: cluster meta carries cluster/ip/
// CIDR fields and a captureClusterConfig side effect, the sidecar meta only
// created/mode, so unifying it would add more branching than it removes.
type snapshotTarget struct {
	// machine is the container whose suspended machine state a warm snapshot
	// captures.
	machine string
	// statePrefix namespaces the warm machine-state files inside the snapshot
	// dir ("server-" for a cluster, "sidecar-" for the sidecar).
	statePrefix string
	// rootfs is the primary VM root filesystem artifact.
	rootfs snapshotArtifact
	// extras are the additional per-target artifacts (registry rootfs + k3s
	// config for a cluster; the image-store volume for the sidecar).
	extras []snapshotArtifact
	// stateFile resolves a machine-state file's live path, requiring it to
	// exist (the save source). Defaults to containerStateFilePath(machine,name).
	stateFile func(name string) (string, error)
	// statePath resolves a machine-state file's live path without requiring it
	// to exist (the restore destination / removal target). Defaults to
	// containerStateFile(machine, name). Injectable for tests.
	statePath func(name string) (string, error)
}

// snapshotArtifact is one file or directory captured into (and restored from) a
// snapshot. name is its filename inside the snapshot dir; src resolves its live
// save source, dst its live restore destination (defaults to src). They differ
// only where a save source must already exist but a restore destination may be
// (re)created — the docker image-store volume.
type snapshotArtifact struct {
	name     string                 // filename inside the snapshot dir
	label    string                 // human label for log lines
	src      func() (string, error) // live save source (may validate existence)
	dst      func() (string, error) // live restore destination; nil → same as src
	required bool                   // save: a resolve error aborts vs skips
	isDir    bool                   // copyDir vs cloneFile
}

// staleStateFiles are the machine-state files that belong to the pre-restore
// disk image and must be cleared on every restore so a cold boot starts clean;
// machine-identifier.bin is deliberately excluded — it is stable container
// identity, not state, and is preserved (a warm restore still overwrites it from
// the snapshot, which carries the whole suspendStateFiles set).
var staleStateFiles = []string{vmstateFile, "vmstate-attachments.json", "vmstate-features.json"}

// clusterSnapshotTarget describes a k3s cluster: server rootfs, optional
// registry rootfs, and the k3s config directory, with warm state under
// "server-".
func clusterSnapshotTarget(cfg *config.Config) snapshotTarget {
	return snapshotTarget{
		machine:     cfg.ServerName,
		statePrefix: "server-",
		rootfs: snapshotArtifact{
			name:  serverRootfs,
			label: "server root filesystem",
			src:   func() (string, error) { return containerRootfsPath(cfg.ServerName) },
			required: true,
		},
		extras: []snapshotArtifact{
			{
				name:  registryRootfs,
				label: "registry root filesystem",
				src:   func() (string, error) { return containerRootfsPath(cfg.RegistryName) },
				required: false, // no registry VM on this cluster → skip
			},
			{
				name:  "k3s-etc",
				label: "k3s config",
				src:   func() (string, error) { return cfg.K3sEtcDir(), nil },
				required: true,
				isDir:    true,
			},
		},
		stateFile: func(name string) (string, error) { return containerStateFilePath(cfg.ServerName, name) },
		statePath: func(name string) (string, error) { return containerStateFile(cfg.ServerName, name) },
	}
}

// sidecarSnapshotTarget describes the docker sidecar: its rootfs and the
// image-store volume (required), with warm state under "sidecar-".
func sidecarSnapshotTarget(cfg *config.Config) snapshotTarget {
	return snapshotTarget{
		machine:     dockerName,
		statePrefix: "sidecar-",
		rootfs: snapshotArtifact{
			name:  dockerSnapRootfs,
			label: "sidecar root filesystem",
			src:   func() (string, error) { return containerRootfsPath(dockerName) },
			required: true,
		},
		extras: []snapshotArtifact{
			{
				name:  dockerSnapVolume,
				label: "docker image store (the nested cluster's data)",
				src: func() (string, error) {
					vol, err := dockerVolumePath()
					if err != nil {
						return "", err
					}
					if _, err := os.Stat(vol); err != nil {
						return "", fmt.Errorf("docker image-store volume not found at %s: %w", vol, err)
					}
					return vol, nil
				},
				// restore recreates the volume image, so its destination need
				// not exist yet — resolve the path without the save-time check.
				dst:      func() (string, error) { return dockerVolumePath() },
				required: true,
			},
		},
		stateFile: func(name string) (string, error) { return containerStateFilePath(dockerName, name) },
		statePath: func(name string) (string, error) { return containerStateFile(dockerName, name) },
	}
}

// writeSnapshotArtifacts clones a target's rootfs and extras into dir, then (for
// a warm snapshot) the suspended machine-state files. It is the shared core of
// what were the near-identical writeSnapshot / writeDockerSnapshot bodies; the
// adapters add their target-specific meta file afterward.
func writeSnapshotArtifacts(dir string, t snapshotTarget, warm bool) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := writeArtifact(dir, t.rootfs); err != nil {
		return err
	}
	for _, a := range t.extras {
		if err := writeArtifact(dir, a); err != nil {
			return err
		}
	}
	if warm {
		return writeWarmState(dir, t)
	}
	return nil
}

// writeArtifact copies one artifact's live copy into the snapshot dir. A missing
// required artifact is an error; a missing optional one is skipped.
func writeArtifact(dir string, a snapshotArtifact) error {
	src, err := a.src()
	if err != nil {
		if a.required {
			return err
		}
		return nil
	}
	logger.Info("cloning " + a.label)
	dst := filepath.Join(dir, a.name)
	if a.isDir {
		return copyDir(src, dst)
	}
	return cloneFile(src, dst)
}

// writeWarmState clones the suspended machine-state files into the snapshot dir
// under the target's prefix, making the snapshot warm. It requires the machine
// to actually be suspended (a present vmstate file).
func writeWarmState(dir string, t snapshotTarget) error {
	if _, err := t.stateFile(vmstateFile); err != nil {
		return fmt.Errorf("no saved machine state after suspend: %w", err)
	}
	for _, name := range suspendStateFiles {
		src, err := t.stateFile(name)
		if err != nil {
			continue
		}
		if err := cloneFile(src, filepath.Join(dir, t.statePrefix+name)); err != nil {
			return err
		}
	}
	return nil
}

// scanSnapshots reads the snapshot directories under root, parsing each meta
// file (metaFile — "meta.yaml" for clusters, "meta" for the sidecar) for its
// mode and created time. It is the shared scan behind Snapshots and
// DockerSnapshots; callers apply their own default Created value and any
// sorting. Order is ReadDir order (callers sort if they need to).
func scanSnapshots(root, metaFile, createdDefault string) []SnapshotInfo {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	infos := make([]SnapshotInfo, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info := SnapshotInfo{Name: e.Name(), Mode: "cold", Created: createdDefault}
		info.Size = dirDiskUsage(filepath.Join(root, e.Name()))
		if meta, err := os.ReadFile(filepath.Join(root, e.Name(), metaFile)); err == nil {
			for _, line := range strings.Split(string(meta), "\n") {
				if v, ok := strings.CutPrefix(line, "created: "); ok {
					info.Created = strings.TrimSpace(v)
				}
				if v, ok := strings.CutPrefix(line, "mode: "); ok {
					info.Mode = strings.TrimSpace(v)
				}
			}
		}
		infos = append(infos, info)
	}
	return infos
}

// restoreSnapshotArtifacts clones a target's rootfs and extras from the snapshot
// dir back onto the live VM, then reconciles machine state, returning whether
// the machine will resume warm. It is the shared core of what were the diverged
// artifact/state blocks in SnapshotRestore and DockerSnapshotRestore; the
// callers keep their target-specific orchestration (stop, IP reclaim, CIDR
// checks, virtiofs repair, bring-up) around it.
func restoreSnapshotArtifacts(dir string, t snapshotTarget, cold bool) (warm bool, err error) {
	if err := restoreArtifact(dir, t.rootfs); err != nil {
		return false, err
	}
	for _, a := range t.extras {
		if err := restoreArtifact(dir, a); err != nil {
			return false, err
		}
	}
	return restoreMachineState(dir, t, cold)
}

// restoreArtifact clones one artifact from the snapshot back to its live
// location. Artifacts the snapshot does not carry, and destinations that cannot
// be resolved (e.g. an absent registry VM), are skipped — matching each path's
// prior restore behavior. Clone/copy errors propagate.
func restoreArtifact(dir string, a snapshotArtifact) error {
	snap := filepath.Join(dir, a.name)
	if _, err := os.Stat(snap); err != nil {
		return nil // snapshot doesn't carry this artifact
	}
	resolve := a.dst
	if resolve == nil {
		resolve = a.src
	}
	dst, err := resolve()
	if err != nil {
		return nil // live destination unavailable → skip
	}
	logger.Info("restoring " + a.label)
	if a.isDir {
		return copyDir(snap, dst)
	}
	return cloneFile(snap, dst)
}

// restoreMachineState clears the stale pre-restore machine state and, for a warm
// restore, clones the snapshot's suspended state back into place. Returns
// whether the machine has warm state to resume from. This unifies the two
// previously-diverged blocks: both targets now preserve machine-identifier.bin
// on a cold restore and propagate clone errors (previously only the cluster did
// both; the sidecar removed the identifier and ignored clone errors).
func restoreMachineState(dir string, t snapshotTarget, cold bool) (bool, error) {
	for _, name := range staleStateFiles {
		if path, err := t.statePath(name); err == nil {
			_ = os.Remove(path)
		}
	}
	if cold {
		return false, nil
	}
	// warm only if the snapshot actually carries machine state
	if _, err := os.Stat(filepath.Join(dir, t.statePrefix+vmstateFile)); err != nil {
		return false, nil
	}
	warm := false
	for _, name := range suspendStateFiles {
		snap := filepath.Join(dir, t.statePrefix+name)
		if _, err := os.Stat(snap); err != nil {
			continue
		}
		dst, err := t.statePath(name)
		if err != nil {
			return warm, err
		}
		if err := cloneFile(snap, dst); err != nil {
			return warm, err
		}
		if name == vmstateFile {
			warm = true
		}
	}
	return warm, nil
}
