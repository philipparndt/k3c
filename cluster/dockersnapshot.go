package cluster

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/philipparndt/go-logger"

	"k3c/config"
)

// Docker sidecar snapshots. Unlike a cluster (whose k3s data lives in the VM
// rootfs), the sidecar's state — every nested k3d cluster, its images, and all
// container data — lives in the docker image-store volume (volume.img, mounted
// at /var/lib/docker). So a sidecar snapshot clones BOTH the VM rootfs and that
// volume image (APFS copy-on-write: instant, near-zero disk), optionally with
// the suspended machine state for a warm (instant-resume) snapshot.
//
// This snapshots the WHOLE sidecar engine, not a single nested cluster — the
// headline use is a golden "fully provisioned" state to reset to. Restore
// stops the sidecar, replaces the rootfs + image store from the snapshot, and
// brings it back up.

const (
	dockerSnapRootfs = "sidecar-rootfs.ext4"
	dockerSnapVolume = "docker-data.img"
)

func dockerSnapshotsRoot(cfg *config.Config) string {
	return filepath.Join(cfg.BaseDir, "docker-snapshots")
}

func dockerSnapshotDir(cfg *config.Config, name string) string {
	return filepath.Join(dockerSnapshotsRoot(cfg), name)
}

// dockerVolumePath is the backing image of the sidecar's image-store volume.
func dockerVolumePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Application Support",
		"com.apple.container", "volumes", dockerVolume, "volume.img"), nil
}

// DockerSnapshotSave clones the sidecar's rootfs + image-store volume into a
// named snapshot. Warm (the default, when suspend is supported) saves the
// suspended machine state too, so a restore resumes instantly; cold quiesces
// with a stop. The sidecar is returned to its prior running state afterward.
func DockerSnapshotSave(cfg *config.Config, name string, cold, replace bool) error {
	if !containerExists(dockerName, false) {
		return fmt.Errorf("docker sidecar does not exist (k3c docker up)")
	}
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("snapshot name required")
	}
	dir := dockerSnapshotDir(cfg, name)
	if _, err := os.Stat(dir); err == nil {
		if !replace {
			return fmt.Errorf("docker snapshot '%s' already exists", name)
		}
		// --replace: recreate in place — drop the existing snapshot first.
		if err := DockerSnapshotDelete(cfg, name); err != nil {
			return fmt.Errorf("replacing docker snapshot '%s': %w", name, err)
		}
	}
	dockerResumeIfPaused(cfg)
	wasRunning := containerExists(dockerName, true)
	warm := !cold && wasRunning && capabilities().suspend

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	// quiesce the VM so the rootfs and volume image are consistent on disk
	if warm {
		logger.Info("suspending docker sidecar for a warm snapshot")
		if cfg.TransparentEgress {
			stopGvnet(cfg, dockerName)
		}
		if out, err := runContainer("suspend", dockerName); err != nil {
			_ = os.RemoveAll(dir)
			return fmt.Errorf("suspend failed: %s", out)
		}
	} else if wasRunning {
		logger.Info("stopping docker sidecar for a cold snapshot")
		if cfg.TransparentEgress {
			stopGvnet(cfg, dockerName)
		}
		if out, err := runContainer("stop", dockerName); err != nil {
			_ = os.RemoveAll(dir)
			return fmt.Errorf("stopping docker sidecar: %s", out)
		}
	}

	if err := writeDockerSnapshot(cfg, dir, warm); err != nil {
		_ = os.RemoveAll(dir)
		if wasRunning {
			_ = DockerUp(cfg, false) // best-effort bring-back
		}
		return err
	}

	if wasRunning {
		logger.Info("restoring docker sidecar to its running state")
		if err := DockerUp(cfg, false); err != nil {
			return err
		}
	}
	mode := "cold"
	if warm {
		mode = "warm"
	}
	logger.Info(fmt.Sprintf("%s snapshot '%s' of docker saved", mode, name))
	return nil
}

func writeDockerSnapshot(cfg *config.Config, dir string, warm bool) error {
	if src, err := containerRootfsPath(dockerName); err == nil {
		logger.Info("cloning sidecar root filesystem")
		if err := cloneFile(src, filepath.Join(dir, dockerSnapRootfs)); err != nil {
			return err
		}
	} else {
		return err
	}
	vol, err := dockerVolumePath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(vol); err != nil {
		return fmt.Errorf("docker image-store volume not found at %s: %w", vol, err)
	}
	logger.Info("cloning docker image store (the nested cluster's data)")
	if err := cloneFile(vol, filepath.Join(dir, dockerSnapVolume)); err != nil {
		return err
	}
	if warm {
		if _, err := containerStateFilePath(dockerName, vmstateFile); err != nil {
			return fmt.Errorf("no saved machine state after suspend: %w", err)
		}
		for _, n := range suspendStateFiles {
			if src, err := containerStateFilePath(dockerName, n); err == nil {
				if err := cloneFile(src, filepath.Join(dir, "sidecar-"+n)); err != nil {
					return err
				}
			}
		}
	}
	mode := "cold"
	if warm {
		mode = "warm"
	}
	meta := fmt.Sprintf("created: %s\nmode: %s\n", time.Now().Format(time.RFC3339), mode)
	return os.WriteFile(filepath.Join(dir, "meta"), []byte(meta), 0o644)
}

// DockerSnapshotRestore replaces the sidecar's rootfs + image store from a
// snapshot, then brings the sidecar back up.
func DockerSnapshotRestore(cfg *config.Config, name string, cold bool) error {
	dir := dockerSnapshotDir(cfg, name)
	if _, err := os.Stat(filepath.Join(dir, dockerSnapVolume)); err != nil {
		return fmt.Errorf("no snapshot '%s' of docker", name)
	}
	dockerResumeIfPaused(cfg)
	if containerExists(dockerName, true) {
		logger.Info("stopping docker sidecar to restore its image store")
		if cfg.TransparentEgress {
			stopGvnet(cfg, dockerName)
		}
		_, _ = runContainer("stop", dockerName)
	}

	if dst, err := containerRootfsPath(dockerName); err == nil {
		if _, serr := os.Stat(filepath.Join(dir, dockerSnapRootfs)); serr == nil {
			logger.Info("restoring sidecar root filesystem")
			if err := cloneFile(filepath.Join(dir, dockerSnapRootfs), dst); err != nil {
				return err
			}
		}
	}
	vol, err := dockerVolumePath()
	if err != nil {
		return err
	}
	logger.Info("restoring docker image store")
	if err := cloneFile(filepath.Join(dir, dockerSnapVolume), vol); err != nil {
		return err
	}

	// warm restore: put the suspended machine state back so the VM resumes;
	// cold restore: drop any saved state so it boots fresh
	warm := !cold
	if _, err := os.Stat(filepath.Join(dir, "sidecar-"+vmstateFile)); err != nil {
		warm = false
	}
	for _, n := range suspendStateFiles {
		dst, derr := containerStateFile(dockerName, n)
		if derr != nil {
			continue
		}
		src := filepath.Join(dir, "sidecar-"+n)
		if warm {
			if _, serr := os.Stat(src); serr == nil {
				_ = cloneFile(src, dst)
			}
		} else {
			_ = os.Remove(dst) // ensure a cold boot
		}
	}

	logger.Info("bringing the docker sidecar back up")
	if err := DockerUp(cfg, false); err != nil {
		return err
	}
	logger.Info("snapshot '" + name + "' of docker restored")
	return nil
}

// DockerSnapshots lists saved sidecar snapshots (newest first).
func DockerSnapshots(cfg *config.Config) []SnapshotInfo {
	entries, err := os.ReadDir(dockerSnapshotsRoot(cfg))
	if err != nil {
		return nil
	}
	var snaps []SnapshotInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info := SnapshotInfo{Name: e.Name(), Mode: "cold"}
		info.Size = dirDiskUsage(dockerSnapshotDir(cfg, e.Name()))
		if data, err := os.ReadFile(filepath.Join(dockerSnapshotDir(cfg, e.Name()), "meta")); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(line, "mode: ") {
					info.Mode = strings.TrimSpace(strings.TrimPrefix(line, "mode: "))
				}
				if strings.HasPrefix(line, "created: ") {
					info.Created = strings.TrimSpace(strings.TrimPrefix(line, "created: "))
				}
			}
		}
		snaps = append(snaps, info)
	}
	sort.Slice(snaps, func(i, j int) bool { return snaps[i].Created > snaps[j].Created })
	return snaps
}

// DockerSnapshotDelete removes a saved sidecar snapshot.
func DockerSnapshotDelete(cfg *config.Config, name string) error {
	dir := dockerSnapshotDir(cfg, name)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("no snapshot '%s' of docker", name)
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	logger.Info("snapshot '" + name + "' of docker deleted")
	return nil
}

// DockerSnapshotRename renames a saved sidecar snapshot. The name is only the
// directory name, so a rename is a directory move (the sidecar uses no
// pull-cache pins).
func DockerSnapshotRename(cfg *config.Config, oldName, newName string) error {
	if err := validSnapshotName(newName); err != nil {
		return err
	}
	if oldName == newName {
		return fmt.Errorf("snapshot '%s' already has that name", oldName)
	}
	oldDir := dockerSnapshotDir(cfg, oldName)
	if _, err := os.Stat(oldDir); err != nil {
		return fmt.Errorf("no snapshot '%s' of docker", oldName)
	}
	newDir := dockerSnapshotDir(cfg, newName)
	if _, err := os.Stat(newDir); err == nil {
		return fmt.Errorf("snapshot '%s' of docker already exists", newName)
	}
	if err := os.Rename(oldDir, newDir); err != nil {
		return err
	}
	logger.Info("snapshot '" + oldName + "' of docker renamed to '" + newName + "'")
	return nil
}
