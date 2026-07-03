package cluster

import (
	"fmt"
	"os"
	"path/filepath"

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
	// stateFile resolves a machine-state file's live path for this target;
	// defaults to containerStateFilePath(machine, name). Injectable for tests.
	stateFile func(name string) (string, error)
}

// snapshotArtifact is one file or directory captured into (and restored from) a
// snapshot. name is its filename inside the snapshot dir; src resolves its live
// location, which is also the restore destination.
type snapshotArtifact struct {
	name     string                  // filename inside the snapshot dir
	label    string                  // human label for log lines
	src      func() (string, error)  // live path (save source == restore dest)
	required bool                    // true → a resolve error aborts; false → skip
	isDir    bool                    // copyDir vs cloneFile
}

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
				required: true,
			},
		},
		stateFile: func(name string) (string, error) { return containerStateFilePath(dockerName, name) },
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
