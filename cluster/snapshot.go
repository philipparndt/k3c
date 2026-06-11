package cluster

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/philipparndt/go-logger"
	"golang.org/x/sys/unix"

	"k3c/config"
)

// Snapshots capture a stopped cluster's complete state by APFS-cloning the
// VM root filesystems (copy-on-write: instant, near-zero disk cost) plus
// the bind-mounted /etc/rancher/k3s directory. Restore clones them back
// and restarts the cluster.
//
// A snapshot can only be restored into an existing cluster container (the
// container's identity and published ports are not part of the snapshot).

const serverRootfs = "server-rootfs.ext4"
const registryRootfs = "registry-rootfs.ext4"

// files written next to the rootfs by suspend-capable container builds
var suspendStateFiles = []string{"vmstate.czs", "vmstate-attachments.json", "machine-identifier.bin"}

// containerStateFilePath returns the path of a file in a container's state
// directory, erroring when the file does not exist.
func containerStateFilePath(container, name string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(home, "Library", "Application Support",
		"com.apple.container", "containers", container, name)
	if _, err := os.Stat(path); err != nil {
		return "", err
	}
	return path, nil
}

// containerRootfsPath returns the VM root filesystem backing a container.
func containerRootfsPath(name string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(home, "Library", "Application Support",
		"com.apple.container", "containers", name, "rootfs.ext4")
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("no root filesystem for container %s: %w", name, err)
	}
	return path, nil
}

// cloneFile copies src to dst using an APFS copy-on-write clone.
func cloneFile(src, dst string) error {
	_ = os.Remove(dst)
	if err := unix.Clonefile(src, dst, 0); err != nil {
		return fmt.Errorf("clonefile %s -> %s: %w (snapshots require APFS on the same volume)", src, dst, err)
	}
	return nil
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}

func snapshotDir(cfg *config.Config, name string) string {
	return filepath.Join(cfg.BaseDir, "snapshots", cfg.Cluster, name)
}

func validSnapshotName(name string) error {
	if name == "" || strings.ContainsAny(name, "/\\ ") || strings.HasPrefix(name, ".") {
		return fmt.Errorf("invalid snapshot name %q", name)
	}
	return nil
}

// SnapshotSave snapshots a cluster. A running cluster is stopped for the
// (sub-second) clone and started again afterwards.
func SnapshotSave(cfg *config.Config, name string) error {
	if name == "" {
		name = time.Now().Format("20060102-150405")
	}
	if err := validSnapshotName(name); err != nil {
		return err
	}
	if !containerExists(cfg.ServerName, false) {
		return fmt.Errorf("cluster '%s' does not exist", cfg.Cluster)
	}
	dir := snapshotDir(cfg, name)
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("snapshot '%s' already exists for cluster '%s'", name, cfg.Cluster)
	}

	resumeIfPaused(cfg)
	wasRunning := containerExists(cfg.ServerName, true)
	if wasRunning {
		logger.Info("stopping cluster for a consistent snapshot")
		_, _ = runContainer("stop", cfg.ServerName)
		_, _ = runContainer("stop", cfg.RegistryName)
	}

	if err := writeSnapshot(cfg, dir); err != nil {
		_ = os.RemoveAll(dir)
		if wasRunning {
			_, _ = runContainer("start", cfg.RegistryName)
			_, _ = runContainer("start", cfg.ServerName)
		}
		return err
	}

	if wasRunning {
		logger.Info("restarting cluster")
		_, _ = runContainer("start", cfg.RegistryName)
		if out, err := runContainer("start", cfg.ServerName); err != nil {
			return fmt.Errorf("snapshot saved, but restart failed: %s", out)
		}
	}
	logger.Info("snapshot '" + name + "' saved for cluster '" + cfg.Cluster + "'")
	return nil
}

func writeSnapshot(cfg *config.Config, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	src, err := containerRootfsPath(cfg.ServerName)
	if err != nil {
		return err
	}
	logger.Info("cloning server root filesystem")
	if err := cloneFile(src, filepath.Join(dir, serverRootfs)); err != nil {
		return err
	}
	if registrySrc, err := containerRootfsPath(cfg.RegistryName); err == nil {
		logger.Info("cloning registry root filesystem")
		if err := cloneFile(registrySrc, filepath.Join(dir, registryRootfs)); err != nil {
			return err
		}
	}
	if err := copyDir(cfg.K3sEtcDir(), filepath.Join(dir, "k3s-etc")); err != nil {
		return err
	}
	// suspended-state companions (suspend-capable container builds)
	for _, name := range suspendStateFiles {
		if src, err := containerStateFilePath(cfg.ServerName, name); err == nil {
			if err := cloneFile(src, filepath.Join(dir, "server-"+name)); err != nil {
				return err
			}
		}
	}
	meta := fmt.Sprintf("cluster: %s\ncreated: %s\n", cfg.Cluster, time.Now().Format(time.RFC3339))
	return os.WriteFile(filepath.Join(dir, "meta.yaml"), []byte(meta), 0o644)
}

// SnapshotRestore restores a snapshot into the existing cluster container
// and starts the cluster.
func SnapshotRestore(cfg *config.Config, name string) error {
	if err := validSnapshotName(name); err != nil {
		return err
	}
	dir := snapshotDir(cfg, name)
	if _, err := os.Stat(filepath.Join(dir, serverRootfs)); err != nil {
		return fmt.Errorf("snapshot '%s' not found for cluster '%s'", name, cfg.Cluster)
	}
	if !containerExists(cfg.ServerName, false) {
		return fmt.Errorf("cluster '%s' does not exist; create it first (the snapshot restores its state, not the container)", cfg.Cluster)
	}

	resumeIfPaused(cfg)
	if containerExists(cfg.ServerName, true) {
		logger.Info("stopping cluster")
		_, _ = runContainer("stop", cfg.ServerName)
		_, _ = runContainer("stop", cfg.RegistryName)
	}

	dst, err := containerRootfsPath(cfg.ServerName)
	if err != nil {
		return err
	}
	logger.Info("restoring server root filesystem")
	if err := cloneFile(filepath.Join(dir, serverRootfs), dst); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, registryRootfs)); err == nil {
		if registryDst, err := containerRootfsPath(cfg.RegistryName); err == nil {
			logger.Info("restoring registry root filesystem")
			if err := cloneFile(filepath.Join(dir, registryRootfs), registryDst); err != nil {
				return err
			}
		}
	}
	if err := copyDir(filepath.Join(dir, "k3s-etc"), cfg.K3sEtcDir()); err != nil {
		return err
	}
	for _, fileName := range suspendStateFiles {
		snapshotFile := filepath.Join(dir, "server-"+fileName)
		if _, err := os.Stat(snapshotFile); err != nil {
			continue
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		dst := filepath.Join(home, "Library", "Application Support",
			"com.apple.container", "containers", cfg.ServerName, fileName)
		if err := cloneFile(snapshotFile, dst); err != nil {
			return err
		}
	}

	logger.Info("snapshot '" + name + "' restored, starting cluster")
	return Start(cfg)
}

// SnapshotList prints the snapshots of a cluster.
func SnapshotList(cfg *config.Config) error {
	base := filepath.Join(cfg.BaseDir, "snapshots", cfg.Cluster)
	entries, err := os.ReadDir(base)
	if os.IsNotExist(err) {
		fmt.Printf("no snapshots for cluster '%s'\n", cfg.Cluster)
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Printf("%-24s %s\n", "NAME", "CREATED")
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		created := "?"
		if meta, err := os.ReadFile(filepath.Join(base, e.Name(), "meta.yaml")); err == nil {
			for _, line := range strings.Split(string(meta), "\n") {
				if v, ok := strings.CutPrefix(line, "created: "); ok {
					created = v
				}
			}
		}
		fmt.Printf("%-24s %s\n", e.Name(), created)
	}
	return nil
}

// SnapshotDelete removes a snapshot.
func SnapshotDelete(cfg *config.Config, name string) error {
	if err := validSnapshotName(name); err != nil {
		return err
	}
	dir := snapshotDir(cfg, name)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("snapshot '%s' not found for cluster '%s'", name, cfg.Cluster)
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	logger.Info("snapshot '" + name + "' deleted")
	return nil
}
