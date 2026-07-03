package cluster

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
//
// It clones into a temp sibling and renames over dst, so a failed clone
// (cross-volume, out of space) leaves the existing dst untouched. This matters
// on restore, where dst is the live container rootfs — the original must
// survive a failed restore so the cluster can still boot.
func cloneFile(src, dst string) error {
	tmp := fmt.Sprintf("%s.clone-%d.tmp", dst, os.Getpid())
	_ = os.Remove(tmp) // Clonefile requires the target not to exist
	if err := unix.Clonefile(src, tmp, 0); err != nil {
		return fmt.Errorf("clonefile %s -> %s: %w (snapshots require APFS on the same volume)", src, dst, err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("commit clone to %s: %w", dst, err)
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

// SnapshotSave snapshots a cluster at the requested tier (mode). By default
// (ModeWarm) a running cluster on a suspend-capable container build is
// suspended for the (sub-second) clone and resumed afterwards — a warm
// snapshot that restores to a running cluster. ModeCold stops the cluster
// for a clean-shutdown disk image that boots fresh. ModeFrozen takes a
// logical extract (datastore + all PVC data + certs + image manifest) of the
// running cluster and drops the reconstructable image store.
//
// Two-phase save: the freeze/quiesce window covers only the consistent
// capture plus the instant clone/extract; all size-reduction work
// (pull-cache pinning, then rootfs re-sparsify) is dispatched detached after
// the cluster resumes. The snapshot is valid and restorable the instant
// phase 1 completes — see reduceSnapshot.
func SnapshotSave(cfg *config.Config, name string, mode SnapshotMode, replace bool) error {
	if name == "" {
		name = time.Now().Format("20060102-150405")
	}
	if err := validSnapshotName(name); err != nil {
		return err
	}
	if !containerExists(cfg.ServerName, false) {
		if cfg.Cluster == "docker" {
			return fmt.Errorf("the docker sidecar is not a cluster; snapshot it with: k3c docker snapshot save %s", name)
		}
		return fmt.Errorf("cluster '%s' does not exist", cfg.Cluster)
	}
	dir := snapshotDir(cfg, name)
	if err := prepareSnapshotSlot(cfg, name, replace); err != nil {
		return err
	}

	resumeIfPaused(cfg)
	if mode == ModeFrozen {
		return saveFrozen(cfg, name, dir)
	}
	cold := mode == ModeCold
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
	modeStr := "cold"
	if warm {
		modeStr = "warm"
	}
	logger.Info("snapshot '" + name + "' (" + modeStr + ") saved for cluster '" + cfg.Cluster + "'")

	// Phase 2: the snapshot is already valid and restorable. Dispatch the
	// size-reduction detached so it runs without re-pausing the resumed
	// cluster. warm/cold have no image manifest to pin; they only sparsify.
	reduceSnapshot(cfg, name, dir, nil)
	return nil
}

// saveFrozen takes a frozen snapshot of the running cluster: a logical
// guest-side extract with no stop/suspend (the freeze window is the
// crash-consistent sqlite/tar capture). The cluster keeps running
// throughout; phase 2 then pins the image closure and is idempotent.
func saveFrozen(cfg *config.Config, name, dir string) error {
	if !containerExists(cfg.ServerName, true) {
		return fmt.Errorf("a frozen snapshot needs the cluster running to extract its state; start it first")
	}
	serverIP := containerIP(cfg.ServerName)
	if err := writeFrozenSnapshot(cfg, dir, serverIP); err != nil {
		_ = os.RemoveAll(dir)
		return err
	}
	// Surface a cache shortfall now rather than only at restore. This is the
	// same presence check the thaw runs: it errors only when a genuinely
	// unrecoverable local-only image lost its bundle (which cannot happen here,
	// since writeFrozenSnapshot just wrote it), and otherwise warns that the
	// thaw will need to re-pull remote images from their upstream registries.
	if err := verifyFrozenBlobs(cfg, dir); err != nil {
		_ = os.RemoveAll(dir)
		return err
	}
	logger.Info("snapshot '" + name + "' (frozen) saved for cluster '" + cfg.Cluster + "'")

	// Phase 2: read the manifest we just wrote and hand its digest closure
	// to the background reduction so the pin commits durably before any
	// cosmetic step (frozen has no rootfs to sparsify, only the pin).
	manifest, err := readFrozenManifest(filepath.Join(dir, frozenManifestF))
	if err != nil {
		logger.Warn("frozen: could not read image manifest for pinning: " + err.Error())
		reduceSnapshot(cfg, name, dir, nil)
		return nil
	}
	reduceSnapshot(cfg, name, dir, manifest.Digests)
	return nil
}

// reducingSnapshots guards against a background reduction racing a restore
// or delete of the same snapshot, and against double-dispatching one.
var (
	reducingMu       sync.Mutex
	reducingSnapshot = map[string]bool{}
)

// reduceSnapshot runs the detached phase-2 size reduction for a just-saved
// snapshot. It is the two-phase seam: phase 1 (the consistent capture +
// clone/extract) has already produced a valid, restorable snapshot, so this
// must never make the snapshot incorrect — only smaller.
//
// Order is load-bearing: the pull-cache pin commits FIRST (a lost pin breaks
// a future thaw or fat export), THEN the cosmetic rootfs re-sparsify (a lost
// sparsify only leaves a larger snapshot). Both steps are idempotent so an
// interrupted reduction completes on a re-run. digests is the frozen image
// closure to pin, or nil for warm/cold.
func reduceSnapshot(cfg *config.Config, name, dir string, digests []string) {
	key := snapshotPinID(cfg.Cluster, name)
	reducingMu.Lock()
	if reducingSnapshot[key] {
		reducingMu.Unlock()
		return
	}
	reducingSnapshot[key] = true
	reducingMu.Unlock()

	go func() {
		defer func() {
			reducingMu.Lock()
			delete(reducingSnapshot, key)
			reducingMu.Unlock()
		}()
		runSnapshotReduction(cfg, name, dir, digests)
	}()
}

// runSnapshotReduction performs the phase-2 steps synchronously (pin first,
// then sparsify). Exposed separately so it is testable without the
// goroutine. Safe to re-run.
func runSnapshotReduction(cfg *config.Config, name, dir string, digests []string) {
	// 1. Pin the image closure durably BEFORE any cosmetic step.
	if len(digests) > 0 {
		if err := pinSnapshotImages(snapshotPinID(cfg.Cluster, name), digests); err != nil {
			logger.Warn("snapshot reduction: pinning image closure failed: " + err.Error())
			// A failed pin means the thaw guarantee is not in place; do not
			// proceed to shrink (nothing to shrink for frozen anyway).
			return
		}
	}
	// 2. Re-sparsify the rootfs clone (warm/cold only; frozen has none).
	rootfs := filepath.Join(dir, serverRootfs)
	if _, err := os.Stat(rootfs); err == nil {
		reclaimed, err := reSparsifySnapshot(rootfs)
		if err != nil {
			logger.Warn("snapshot reduction: re-sparsify failed: " + err.Error())
			return
		}
		if reclaimed > 0 {
			logger.Info(fmt.Sprintf("snapshot '%s': reclaimed %.1f GB by re-sparsify", name, float64(reclaimed)/1e9))
		}
	}
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

// runningContainerOn returns the name of a running container holding ip on
// any of its NICs, or "" when no running container uses it. Used to decide
// whether a warm restore can reclaim the snapshot-time address.
func runningContainerOn(ip string) string {
	out, _ := runContainer("ls")
	return containerHolding(out, ip)
}

// containerHolding scans `container ls` output for a container whose IP
// column (comma-separated CIDRs when a VM has several NICs) contains ip.
func containerHolding(lsOut, ip string) string {
	for _, line := range strings.Split(lsOut, "\n")[1:] {
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		for _, addr := range strings.Split(fields[5], ",") {
			if addr == ip || strings.HasPrefix(addr, ip+"/") {
				return fields[0]
			}
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

// clusterConfigFile is the embedded copy of the cluster's project config
// (k3c.yaml), captured at save time so `cluster import-run` can recreate the
// cluster with its real settings (sizing, egress, mirrors) without a separate
// --config. The host-specific CA bundle is regenerated from the host at create
// time regardless, so embedding the k3c.yaml is safe and portable.
const clusterConfigFile = "cluster-config.yaml"

// captureClusterConfig copies the cluster's project config into the snapshot.
// Best-effort: a cluster created from pure defaults has nothing to embed.
func captureClusterConfig(cfg *config.Config, dir string) {
	for _, src := range []string{filepath.Join(cfg.RunDir(), "k3c.yaml"), cfg.ConfigFile} {
		if src == "" {
			continue
		}
		if data, err := os.ReadFile(src); err == nil {
			_ = os.WriteFile(filepath.Join(dir, clusterConfigFile), data, 0o644)
			return
		}
	}
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
	if err := os.WriteFile(filepath.Join(dir, "meta.yaml"), []byte(meta), 0o644); err != nil {
		return err
	}
	captureClusterConfig(cfg, dir)
	return nil
}

// snapshotExists reports whether the snapshot dir holds a restorable
// snapshot of any tier (a block-image rootfs, or a frozen extract).
func snapshotExists(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, serverRootfs)); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(dir, frozenStateTar)); err == nil {
		return true
	}
	return false
}

// checkSnapshotCIDRs refuses a restore into a cluster with different CIDRs
// than the snapshot was taken with. The CIDRs are baked into the snapshot's
// datastore: restoring into a cluster created with different ones yields a
// subtly broken cluster (e.g. kubelet hands pods a cluster-dns address in
// the new service CIDR while the restored kube-dns service has the old one).
func checkSnapshotCIDRs(cfg *config.Config, dir, name string) error {
	for key, current := range map[string]string{"clusterCidr": cfg.ClusterCIDR, "serviceCidr": cfg.ServiceCIDR} {
		if v := snapshotMetaValue(dir, key); v != "" && v != current {
			return fmt.Errorf("snapshot '%s' was taken with %s %s, but this cluster uses %s; recreate the cluster with the snapshot's CIDRs to restore it",
				name, key, v, current)
		}
	}
	return nil
}

// SnapshotRestore restores a snapshot into the existing cluster container
// and starts the cluster, auto-detecting the tier from meta.yaml. A warm
// snapshot resumes the running cluster it captured; cold ignores its saved
// machine state and boots fresh from the snapshot's disk; a frozen snapshot
// thaws (seeds the datastore + PVC data and rehydrates images from the
// pull-cache). The CIDR check and kubeconfig re-merge apply to every tier.
func SnapshotRestore(cfg *config.Config, name string, cold bool) error {
	if err := validSnapshotName(name); err != nil {
		return err
	}
	if cfg.Cluster == "docker" && !containerExists(cfg.ServerName, false) {
		return fmt.Errorf("the docker sidecar is not a cluster; restore it with: k3c docker snapshot restore %s", name)
	}
	dir := snapshotDir(cfg, name)
	if !snapshotExists(dir) {
		return fmt.Errorf("snapshot '%s' not found for cluster '%s'", name, cfg.Cluster)
	}
	if !containerExists(cfg.ServerName, false) {
		return fmt.Errorf("cluster '%s' does not exist; create it first (the snapshot restores its state, not the container)", cfg.Cluster)
	}
	if err := checkSnapshotCIDRs(cfg, dir, name); err != nil {
		return err
	}

	// Guard against a background reduction (phase 2) of this same snapshot
	// racing the restore.
	key := snapshotPinID(cfg.Cluster, name)
	reducingMu.Lock()
	reducing := reducingSnapshot[key]
	reducingMu.Unlock()
	if reducing {
		return fmt.Errorf("snapshot '%s' is still being reduced in the background; retry in a moment", name)
	}

	// Frozen thaw is cold-equivalent: seed state + boot fresh + rehydrate.
	if snapshotModeOf(dir) == ModeFrozen {
		resumeIfPaused(cfg)
		if err := restoreFrozenSnapshot(cfg, dir); err != nil {
			return err
		}
		// the thaw boots a fresh cluster whose credentials may differ from
		// the one the kubeconfig was merged from; Start already re-merges,
		// but re-merge defensively to mirror the warm/cold path
		if err := KubeconfigMerge(cfg); err != nil {
			return err
		}
		logger.Info("note: watch-based clients (k9s, kubectl -w) keep stale caches after a restore; restart them")
		return nil
	}

	resumeIfPaused(cfg)
	if containerExists(cfg.ServerName, true) {
		logger.Info("stopping cluster")
		_, _ = runContainer("stop", cfg.ServerName)
	}
	// Stop the registry even when the server is not running: it restarts
	// under Start anyway, and stopping it releases an address it may have
	// grabbed from the snapshot's server (a recreated cluster can swap IPs).
	if containerExists(cfg.RegistryName, true) {
		_, _ = runContainer("stop", cfg.RegistryName)
	}
	// A warm snapshot resumes a memory image with the snapshot-time IP
	// configured in the guest, and the runtime re-requests that address on
	// start (the snapshot's vmstate-attachments.json becomes desiredAddress,
	// honored when free). Stopping the cluster just released its own
	// addresses — even swapped ones after a delete/recreate — so the reclaim
	// only fails when some OTHER running container sits on the snapshot IP.
	// Then the resumed guest would answer on the wrong address: boot cold
	// instead, which re-initializes the network.
	if !cold {
		if snapIP := snapshotMetaValue(dir, "ip"); snapIP != "" {
			if holder := runningContainerOn(snapIP); holder != "" && holder != cfg.ServerName {
				logger.Warn("the snapshot's IP " + snapIP + " is held by running container '" + holder +
					"'; restoring cold (stop it for a warm resume, or pass --cold to choose this)")
				cold = true
			}
		}
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
		// Resume the server before Start, which boots the registry first: a
		// fresh registry allocation could race the server's desired-address
		// reclaim and take the snapshot IP. Start skips a running server.
		if out, err := startServerVM(cfg); err != nil {
			return fmt.Errorf("resume failed: %s", out)
		}
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
	fmt.Printf("%-24s %-6s %9s  %s\n", "NAME", "MODE", "SIZE", "CREATED")
	for _, s := range snapshots {
		fmt.Printf("%-24s %-6s %9s  %s\n", s.Name, s.Mode, humanBytes(s.Size), s.Created)
	}
	return nil
}

// prepareSnapshotSlot ensures the snapshot directory for name is free to write.
// If a snapshot of that name already exists: without replace it errors (as a
// plain save does); with replace it is deleted first, so the save recreates it
// in place.
func prepareSnapshotSlot(cfg *config.Config, name string, replace bool) error {
	if _, err := os.Stat(snapshotDir(cfg, name)); err != nil {
		return nil // no existing snapshot: the slot is free
	}
	if !replace {
		return fmt.Errorf("snapshot '%s' already exists for cluster '%s'", name, cfg.Cluster)
	}
	if err := SnapshotDelete(cfg, name); err != nil {
		return fmt.Errorf("replacing snapshot '%s': %w", name, err)
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
	// Release the pull-cache pin first so its referenced blobs become
	// eligible for eviction once no other snapshot pins them. Released
	// before the dir is gone so a crash mid-delete still leaves a
	// releasable pin (releaseSnapshotPin is idempotent).
	if err := releaseSnapshotPin(snapshotPinID(cfg.Cluster, name)); err != nil {
		logger.Warn("releasing snapshot pin: " + err.Error())
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	logger.Info("snapshot '" + name + "' deleted")
	return nil
}

// SnapshotRename renames a stored snapshot, moving its directory and its
// pull-cache pin to the new name. The name is only ever a directory name and
// a CLI argument — nothing inside the snapshot (meta.yaml, the datastore)
// references it — so a rename is a directory move plus re-keying the pin.
func SnapshotRename(cfg *config.Config, oldName, newName string) error {
	if err := validSnapshotName(oldName); err != nil {
		return err
	}
	if err := validSnapshotName(newName); err != nil {
		return err
	}
	if oldName == newName {
		return fmt.Errorf("snapshot '%s' already has that name", oldName)
	}
	oldDir := snapshotDir(cfg, oldName)
	if _, err := os.Stat(oldDir); err != nil {
		return fmt.Errorf("snapshot '%s' not found for cluster '%s'", oldName, cfg.Cluster)
	}
	newDir := snapshotDir(cfg, newName)
	if _, err := os.Stat(newDir); err == nil {
		return fmt.Errorf("snapshot '%s' already exists for cluster '%s'", newName, cfg.Cluster)
	}

	// Guard against a background reduction (phase 2) of this snapshot racing
	// the rename — it still holds the old name and writes into the old dir.
	key := snapshotPinID(cfg.Cluster, oldName)
	reducingMu.Lock()
	reducing := reducingSnapshot[key]
	reducingMu.Unlock()
	if reducing {
		return fmt.Errorf("snapshot '%s' is still being reduced in the background; retry in a moment", oldName)
	}

	if err := os.Rename(oldDir, newDir); err != nil {
		return err
	}
	// Move the pull-cache pin so a frozen snapshot keeps its image closure
	// pinned under the new id. Best-effort: the dir move already succeeded
	// (the snapshot now exists under the new name), and a stale pin only
	// affects retention, never restorability.
	if err := renameSnapshotPin(snapshotPinID(cfg.Cluster, oldName), snapshotPinID(cfg.Cluster, newName)); err != nil {
		logger.Warn("renaming snapshot pin: " + err.Error())
	}
	logger.Info("snapshot '" + oldName + "' renamed to '" + newName + "'")
	return nil
}
