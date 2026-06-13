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

// Snapshots capture a cluster's complete state by APFS-cloning the VM root
// filesystems (copy-on-write: instant, near-zero disk cost) plus the
// bind-mounted /etc/rancher/k3s directory. Restore clones them back and
// restarts the cluster.
//
// Two flavors exist. A warm snapshot (the default on suspend-capable
// container builds) suspends the running VM, so the snapshot additionally
// holds the saved machine state and restores to a RUNNING cluster with all
// workload state intact. A cold snapshot (--cold, or when suspend is
// unavailable) stops the cluster for a clean-shutdown disk image that
// boots fresh on restore. A warm snapshot is a superset: it can also be
// restored cold (--cold on restore), which boots from its disk like after
// a power cut.
//
// A snapshot can only be restored into an existing cluster container (the
// container's identity and published ports are not part of the snapshot).

const serverRootfs = "server-rootfs.ext4"
const registryRootfs = "registry-rootfs.ext4"

// files written next to the rootfs by suspend-capable container builds;
// vmstateFile marks (and is required for) a warm restore
const vmstateFile = "vmstate.czs"

var suspendStateFiles = []string{vmstateFile, "vmstate-attachments.json", "vmstate-features.json", "machine-identifier.bin"}

// containerStateFile returns the path of a file in a container's state
// directory.
func containerStateFile(container, name string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Application Support",
		"com.apple.container", "containers", container, name), nil
}

// containerStateFilePath returns the path of a file in a container's state
// directory, erroring when the file does not exist.
func containerStateFilePath(container, name string) (string, error) {
	path, err := containerStateFile(container, name)
	if err != nil {
		return "", err
	}
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

// SnapshotSave snapshots a cluster. By default a running cluster on a
// suspend-capable container build is suspended for the (sub-second) clone
// and resumed afterwards — a warm snapshot that restores to a running
// cluster. With cold (or without suspend support) the cluster is stopped
// for a clean-shutdown snapshot and started again.
func SnapshotSave(cfg *config.Config, name string, cold bool) error {
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
	// captured before the suspend: a warm snapshot only resumes correctly
	// into a container with this address
	serverIP := containerIP(cfg.ServerName)
	// A suspended cluster already has its machine state on disk: snapshot
	// it warm as-is, without touching the cluster.
	suspended := false
	if !wasRunning {
		if _, err := containerStateFilePath(cfg.ServerName, vmstateFile); err == nil {
			suspended = true
		}
	}
	warm := !cold && (wasRunning || suspended)
	if warm && wasRunning && !capabilities().suspend {
		logger.Info("container CLI lacks suspend support; taking a cold snapshot")
		warm = false
	}
	if warm && wasRunning {
		logger.Info("suspending cluster for a warm snapshot")
		if out, err := runContainer("suspend", cfg.ServerName); err != nil {
			logger.Warn("suspend failed, falling back to a cold snapshot: " + out)
			warm = false
		}
	}
	if wasRunning {
		if !warm {
			logger.Info("stopping cluster for a consistent snapshot")
			_, _ = runContainer("stop", cfg.ServerName)
		}
		_, _ = runContainer("stop", cfg.RegistryName)
	}

	if err := writeSnapshot(cfg, dir, warm, serverIP); err != nil {
		_ = os.RemoveAll(dir)
		if wasRunning {
			_, _ = runContainer("start", cfg.RegistryName)
			_, _ = startServerVM(cfg)
			repairVirtiofs(cfg)
		}
		return err
	}

	if wasRunning {
		if warm {
			logger.Info("resuming cluster")
		} else {
			logger.Info("restarting cluster")
		}
		_, _ = runContainer("start", cfg.RegistryName)
		if out, err := startServerVM(cfg); err != nil {
			return fmt.Errorf("snapshot saved, but restart failed: %s", out)
		}
		applyCPUPriority(cfg)
		// a warm resume is a restore from saved machine state, which kills
		// the stateful virtiofs sessions — without the repair every new
		// image pull fails on the unreadable registry CA bundle
		repairVirtiofs(cfg)
	}
	mode := "cold"
	if warm {
		mode = "warm"
	}
	logger.Info("snapshot '" + name + "' (" + mode + ") saved for cluster '" + cfg.Cluster + "'")
	return nil
}

// snapshotMetaValue reads one "key: value" line from a snapshot's
// meta.yaml, or "".
func snapshotMetaValue(dir, key string) string {
	meta, err := os.ReadFile(filepath.Join(dir, "meta.yaml"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(meta), "\n") {
		if v, ok := strings.CutPrefix(line, key+": "); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// containerIP returns a container's vmnet address (without the CIDR
// suffix), or "" when unknown.
func containerIP(name string) string {
	out, _ := runContainer("ls", "-a")
	for _, line := range strings.Split(out, "\n")[1:] {
		fields := strings.Fields(line)
		if len(fields) >= 6 && fields[0] == name && strings.Contains(fields[5], ".") {
			return hostReachableIP(fields[5])
		}
	}
	return ""
}

// hostReachableIP picks the host-routable address from container ls's IP column,
// which lists several (comma-separated) when a VM has multiple NICs — e.g. a
// transparent-egress gvnet NIC plus the vmnet NIC. The gvnet range lives in a
// userspace netstack the host cannot route to, so prefer the vmnet address
// (192.168.64.x, the VmnetGateway subnet); fall back to the first listed.
func hostReachableIP(col string) string {
	ips := strings.Split(col, ",")
	for _, ip := range ips {
		if addr := strings.SplitN(strings.TrimSpace(ip), "/", 2)[0]; strings.HasPrefix(addr, "192.168.64.") {
			return addr
		}
	}
	return strings.SplitN(strings.TrimSpace(ips[0]), "/", 2)[0]
}

func writeSnapshot(cfg *config.Config, dir string, warm bool, serverIP string) error {
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
	if warm {
		// the suspended machine state, making the snapshot warm
		if _, err := containerStateFilePath(cfg.ServerName, vmstateFile); err != nil {
			return fmt.Errorf("no saved machine state after suspend: %w", err)
		}
		for _, name := range suspendStateFiles {
			if src, err := containerStateFilePath(cfg.ServerName, name); err == nil {
				if err := cloneFile(src, filepath.Join(dir, "server-"+name)); err != nil {
					return err
				}
			}
		}
	}
	mode := "cold"
	if warm {
		mode = "warm"
	}
	meta := fmt.Sprintf("cluster: %s\ncreated: %s\nmode: %s\n",
		cfg.Cluster, time.Now().Format(time.RFC3339), mode)
	if serverIP != "" {
		meta += "ip: " + serverIP + "\n"
	}
	// the CIDRs are baked into the datastore (service IPs, pod IPs, the
	// cluster-dns address); a restore into a cluster with different CIDRs
	// must be refused
	meta += "clusterCidr: " + cfg.ClusterCIDR + "\nserviceCidr: " + cfg.ServiceCIDR + "\n"
	return os.WriteFile(filepath.Join(dir, "meta.yaml"), []byte(meta), 0o644)
}

// SnapshotRestore restores a snapshot into the existing cluster container
// and starts the cluster. A warm snapshot resumes the running cluster it
// captured; with cold its saved machine state is ignored and the cluster
// boots fresh from the snapshot's disk.
func SnapshotRestore(cfg *config.Config, name string, cold bool) error {
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
	// The CIDRs are baked into the snapshot's datastore: restoring into a
	// cluster created with different ones yields a subtly broken cluster
	// (e.g. kubelet hands pods a cluster-dns address in the new service
	// CIDR while the restored kube-dns service has the old one).
	for key, current := range map[string]string{"clusterCidr": cfg.ClusterCIDR, "serviceCidr": cfg.ServiceCIDR} {
		if v := snapshotMetaValue(dir, key); v != "" && v != current {
			return fmt.Errorf("snapshot '%s' was taken with %s %s, but this cluster uses %s; recreate the cluster with the snapshot's CIDRs to restore it",
				name, key, v, current)
		}
	}

	resumeIfPaused(cfg)
	// A warm snapshot resumes a memory image with the snapshot-time IP
	// configured in the guest. If the container's address changed (deleted
	// and recreated cluster), the resumed guest would answer on the wrong
	// IP — boot cold instead, which re-initializes the network.
	if !cold {
		snapIP := snapshotMetaValue(dir, "ip")
		if currentIP := containerIP(cfg.ServerName); snapIP != "" && currentIP != "" && snapIP != currentIP {
			logger.Warn("the cluster's IP changed since the snapshot (" + snapIP + " -> " + currentIP + "); restoring cold")
			cold = true
		}
	}
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

	// Stale suspended state on the container belongs to its previous disk
	// image and must never be applied to the restored one. The machine
	// identifier is stable container identity, not state, and stays.
	for _, fileName := range []string{vmstateFile, "vmstate-attachments.json", "vmstate-features.json"} {
		if path, err := containerStateFile(cfg.ServerName, fileName); err == nil {
			_ = os.Remove(path)
		}
	}

	warm := false
	if !cold {
		for _, fileName := range suspendStateFiles {
			snapshotFile := filepath.Join(dir, "server-"+fileName)
			if _, err := os.Stat(snapshotFile); err != nil {
				continue
			}
			dst, err := containerStateFile(cfg.ServerName, fileName)
			if err != nil {
				return err
			}
			if err := cloneFile(snapshotFile, dst); err != nil {
				return err
			}
			if fileName == vmstateFile {
				warm = true
			}
		}
	}

	if warm {
		logger.Info("snapshot '" + name + "' restored (warm), resuming cluster")
	} else {
		// no machine state applied: either none in the snapshot, or --cold
		logger.Info("snapshot '" + name + "' restored (cold), booting cluster")
	}
	if err := Start(cfg); err != nil {
		return err
	}
	// the restored cluster may have different credentials than the one the
	// kubeconfig was merged from (a recreated cluster, an imported
	// snapshot): always re-merge
	if err := KubeconfigMerge(cfg); err != nil {
		return err
	}
	// the restore rolled resourceVersions backward; watches resumed from a
	// now-future version hang silently instead of erroring
	logger.Info("note: watch-based clients (k9s, kubectl -w) keep stale caches after a restore; restart them")
	return nil
}

// SnapshotList prints the snapshots of a cluster.
func SnapshotList(cfg *config.Config) error {
	snapshots := Snapshots(cfg, cfg.Cluster)
	if len(snapshots) == 0 {
		fmt.Printf("no snapshots for cluster '%s'\n", cfg.Cluster)
		return nil
	}
	fmt.Printf("%-24s %-6s %s\n", "NAME", "MODE", "CREATED")
	for _, s := range snapshots {
		fmt.Printf("%-24s %-6s %s\n", s.Name, s.Mode, s.Created)
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
