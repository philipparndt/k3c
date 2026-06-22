package cluster

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/philipparndt/go-logger"

	"k3c/config"
)

// frozen.go implements the frozen snapshot tier: a logical, guest-side
// extract rather than a block-level clone (macOS cannot mount the guest
// ext4, so images cannot be carved out host-side). A frozen snapshot keeps
// every byte of non-reconstructable data — the k3s datastore and ALL
// persistent-volume data — and drops only the container image store, which
// is rehydrated from the pull-cache on thaw. See openspec/specs/snapshots.

// Frozen snapshot layout (under the snapshot dir):
const (
	frozenStateDB    = "frozen-state.db"    // sqlite online backup of k3s datastore
	frozenStorageTar = "frozen-storage.tar" // /var/lib/rancher/k3s/storage (PVC data)
	frozenCertsTar   = "frozen-certs.tar"   // k3s server TLS + token
	frozenManifestF  = "frozen-images.yaml" // image-digest closure manifest
)

// guest paths the logical extract operates on.
const (
	guestStateDB = "/var/lib/rancher/k3s/server/db/state.db"
	guestStorage = "/var/lib/rancher/k3s/storage"
	guestServer  = "/var/lib/rancher/k3s/server" // holds tls/ and token
)

// frozenScratch is a per-extract working directory inside the host-shared
// /etc/rancher/k3s bind mount, so files the guest writes are immediately
// visible on the host (the host cannot read the guest ext4 directly).
const frozenScratch = "/etc/rancher/k3s/.frozen"

// frozenManifest records the image closure a frozen snapshot depends on, so
// the closure can be pinned (pinSnapshotImages) and rehydrated on thaw.
type frozenManifest struct {
	Images  []string // image references referenced by the cluster's workloads
	Digests []string // the full closure: manifest + config + layer digests (pinned)
}

// writeFrozenSnapshot performs the guest-side logical extract into dir:
// sqlite online backup of state.db, tar of the persistent-volume storage,
// the k3s server certs/token, and the image-digest manifest. It MUST refuse
// to produce a snapshot missing the persistent-volume data (the correctness
// invariant: never drop non-reconstructable data).
//
// The cluster must be running for the extract (it reads from the live guest
// via exec); SnapshotSave keeps a frozen cluster running through this call.
func writeFrozenSnapshot(cfg *config.Config, dir string, serverIP string) error {
	if !containerExists(cfg.ServerName, true) {
		return fmt.Errorf("a frozen snapshot needs the cluster running to extract its state; start it first")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	// Scratch lives under the host-shared /etc/rancher/k3s; the guest writes
	// the extract there and the host reads it back over the same share.
	hostScratch := filepath.Join(cfg.K3sEtcDir(), ".frozen")
	_ = os.RemoveAll(hostScratch)
	if err := os.MkdirAll(hostScratch, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(hostScratch)

	// 1. Consistent sqlite online backup of the k3s datastore. ".backup"
	// takes a read lock and copies a transactionally consistent image, so
	// the live server keeps serving — this is the crash-consistent capture
	// that keeps the freeze window minimal (no stop).
	logger.Info("frozen: backing up k3s datastore")
	backupScript := fmt.Sprintf(
		"set -e; rm -f %[1]s/state.db; sqlite3 %[2]s \".backup '%[1]s/state.db'\"",
		frozenScratch, guestStateDB)
	if out, err := runContainer("exec", cfg.ServerName, "sh", "-c", backupScript); err != nil {
		return fmt.Errorf("sqlite backup of state.db failed: %s", strings.TrimSpace(out))
	}

	// 2. Tar of ALL persistent-volume data. This is the correctness
	// invariant: PVC data is not reconstructable, so it MUST be captured.
	logger.Info("frozen: archiving persistent-volume data")
	storageScript := fmt.Sprintf(
		"set -e; if [ -d %[2]s ]; then tar -cf %[1]s/storage.tar -C %[2]s .; else : > %[1]s/storage.tar; fi",
		frozenScratch, guestStorage)
	if out, err := runContainer("exec", cfg.ServerName, "sh", "-c", storageScript); err != nil {
		return fmt.Errorf("archiving persistent-volume data failed: %s", strings.TrimSpace(out))
	}

	// 3. Tar of the k3s server TLS material + token (cluster identity).
	logger.Info("frozen: archiving server certs and token")
	certsScript := fmt.Sprintf(
		"set -e; tar -cf %[1]s/certs.tar -C %[2]s tls token",
		frozenScratch, guestServer)
	if out, err := runContainer("exec", cfg.ServerName, "sh", "-c", certsScript); err != nil {
		return fmt.Errorf("archiving server certs failed: %s", strings.TrimSpace(out))
	}

	// 4. Enumerate referenced images + their digest closure.
	manifest, err := enumerateFrozenImages(cfg)
	if err != nil {
		return err
	}

	// Move the guest-written extract from the shared scratch into the
	// snapshot dir on the host.
	for _, m := range []struct{ src, dst string }{
		{"state.db", frozenStateDB},
		{"storage.tar", frozenStorageTar},
		{"certs.tar", frozenCertsTar},
	} {
		if err := os.Rename(filepath.Join(hostScratch, m.src), filepath.Join(dir, m.dst)); err != nil {
			return fmt.Errorf("collecting %s from guest: %w", m.src, err)
		}
	}

	// Enforce the invariant in code: a frozen snapshot missing PVC data is
	// not a valid snapshot — refuse to write it (a restore would silently
	// destroy stateful workloads).
	if info, err := os.Stat(filepath.Join(dir, frozenStorageTar)); err != nil || info.Size() == 0 {
		return fmt.Errorf("frozen snapshot would omit persistent-volume data (%s missing or empty); refusing to write a snapshot that drops non-reconstructable data", guestStorage)
	}
	if info, err := os.Stat(filepath.Join(dir, frozenStateDB)); err != nil || info.Size() == 0 {
		return fmt.Errorf("frozen snapshot would omit the cluster datastore (state.db missing or empty); refusing")
	}

	// 5. Write the image-digest manifest and meta.yaml.
	if err := writeFrozenManifest(filepath.Join(dir, frozenManifestF), manifest); err != nil {
		return err
	}
	return writeFrozenMeta(cfg, dir, serverIP)
}

// enumerateFrozenImages lists the images referenced in the guest's
// containerd image store and computes their content-addressed closure
// (manifest + config + layer digests) so the closure can be pinned in the
// pull-cache and rehydrated on thaw.
func enumerateFrozenImages(cfg *config.Config) (frozenManifest, error) {
	logger.Info("frozen: enumerating referenced images")
	var m frozenManifest

	// Image references, from crictl (the kubelet's CRI view).
	if out, err := runContainer("exec", cfg.ServerName, "sh", "-c",
		"crictl images -o json 2>/dev/null"); err == nil && strings.TrimSpace(out) != "" {
		var images struct {
			Images []struct {
				RepoTags []string `json:"repoTags"`
			} `json:"images"`
		}
		if err := json.Unmarshal([]byte(out), &images); err == nil {
			seen := map[string]struct{}{}
			for _, img := range images.Images {
				for _, tag := range img.RepoTags {
					if tag == "" || tag == "<none>:<none>" {
						continue
					}
					if _, ok := seen[tag]; !ok {
						seen[tag] = struct{}{}
						m.Images = append(m.Images, tag)
					}
				}
			}
		}
	}

	// Digest closure: every blob in the k8s.io containerd content store the
	// images depend on (manifests, configs, layers are all content entries).
	// `ctr content ls` lists them content-addressed; this is the set the
	// pull-cache must retain so a thaw can rehydrate offline.
	out, err := runContainer("exec", cfg.ServerName, "sh", "-c",
		"ctr -n k8s.io content ls -q 2>/dev/null")
	if err != nil {
		return m, fmt.Errorf("listing image content digests failed: %s", strings.TrimSpace(out))
	}
	seen := map[string]struct{}{}
	for _, line := range strings.Split(out, "\n") {
		d := strings.TrimSpace(line)
		if !strings.HasPrefix(d, "sha256:") {
			continue
		}
		if _, ok := seen[d]; !ok {
			seen[d] = struct{}{}
			m.Digests = append(m.Digests, d)
		}
	}
	if len(m.Digests) == 0 {
		logger.Warn("frozen: no image digests enumerated; thaw will rely on a re-pull")
	}
	return m, nil
}

// writeFrozenManifest serializes the image manifest as YAML (a flat list
// schema kept deliberately simple and human-readable).
func writeFrozenManifest(path string, m frozenManifest) error {
	var b strings.Builder
	b.WriteString("# images referenced by the cluster (rehydrated on thaw)\n")
	b.WriteString("images:\n")
	for _, img := range m.Images {
		b.WriteString("  - " + img + "\n")
	}
	b.WriteString("digests:\n")
	for _, d := range m.Digests {
		b.WriteString("  - " + d + "\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// readFrozenManifest parses a frozen-images.yaml back into a manifest.
func readFrozenManifest(path string) (frozenManifest, error) {
	var m frozenManifest
	data, err := os.ReadFile(path)
	if err != nil {
		return m, err
	}
	section := ""
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		switch {
		case trimmed == "images:":
			section = "images"
		case trimmed == "digests:":
			section = "digests"
		case strings.HasPrefix(trimmed, "- "):
			v := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
			if section == "images" {
				m.Images = append(m.Images, v)
			} else if section == "digests" {
				m.Digests = append(m.Digests, v)
			}
		}
	}
	return m, nil
}

// writeFrozenMeta writes meta.yaml for a frozen snapshot, mirroring the
// fields writeSnapshot records (cluster/created/mode/ip/CIDRs) so restore,
// list, and the CIDR compatibility check behave uniformly across tiers.
func writeFrozenMeta(cfg *config.Config, dir, serverIP string) error {
	meta := fmt.Sprintf("cluster: %s\ncreated: %s\nmode: %s\n",
		cfg.Cluster, time.Now().Format(time.RFC3339), ModeFrozen)
	if serverIP != "" {
		meta += "ip: " + serverIP + "\n"
	}
	meta += "clusterCidr: " + cfg.ClusterCIDR + "\nserviceCidr: " + cfg.ServiceCIDR + "\n"
	return os.WriteFile(filepath.Join(dir, "meta.yaml"), []byte(meta), 0o644)
}

// restoreFrozenSnapshot thaws a frozen snapshot: re-create the datastore and
// persistent-volume data into the (existing) cluster's guest, boot it
// cold-equivalent, and let containerd rehydrate images from the pull-cache
// mirror. Fails clearly if a referenced digest is absent from the
// pull-cache. The CIDR compatibility check and kubeconfig re-merge are
// applied by the shared SnapshotRestore wrapper, not here.
func restoreFrozenSnapshot(cfg *config.Config, dir string) error {
	// Fail before touching the cluster if a referenced digest is missing
	// from the pull-cache: a partial thaw would silently start an
	// incomplete cluster (pods stuck ImagePullBackOff).
	if err := verifyFrozenBlobs(cfg, dir); err != nil {
		return err
	}

	if containerExists(cfg.ServerName, true) {
		logger.Info("stopping cluster")
		_, _ = runContainer("stop", cfg.ServerName)
		_, _ = runContainer("stop", cfg.RegistryName)
	}

	// The guest ext4 is not host-mountable, so seed the extract guest-side:
	// boot a bare server, push state.db + PVC data + certs into place over
	// the shared scratch, then let the normal boot path bring k3s up. We
	// start the registry + server VM, wait for exec to work, then seed.
	_, _ = runContainer("start", cfg.RegistryName)
	if out, err := startServerVM(cfg); err != nil {
		return fmt.Errorf("booting cluster for thaw failed: %s", out)
	}
	repairVirtiofs(cfg)
	if err := waitGuestExec(cfg); err != nil {
		return err
	}

	hostScratch := filepath.Join(cfg.K3sEtcDir(), ".frozen")
	_ = os.RemoveAll(hostScratch)
	if err := os.MkdirAll(hostScratch, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(hostScratch)
	for _, m := range []struct{ src, dst string }{
		{frozenStateDB, "state.db"},
		{frozenStorageTar, "storage.tar"},
		{frozenCertsTar, "certs.tar"},
	} {
		if err := cloneFile(filepath.Join(dir, m.src), filepath.Join(hostScratch, m.dst)); err != nil {
			// cloneFile needs the same APFS volume; fall back to a copy.
			data, rerr := os.ReadFile(filepath.Join(dir, m.src))
			if rerr != nil {
				return rerr
			}
			if werr := os.WriteFile(filepath.Join(hostScratch, m.dst), data, 0o644); werr != nil {
				return werr
			}
		}
	}

	// Seed guest-side: stop any half-started k3s, drop the datastore + PVC
	// data + certs into place, then a fresh boot picks them up.
	logger.Info("frozen: seeding datastore, persistent-volume data, and certs")
	seedScript := fmt.Sprintf(`set -e
# stop k3s if the boot already started it; we are replacing its datastore
(systemctl stop k3s 2>/dev/null || rc-service k3s stop 2>/dev/null || pkill -x k3s 2>/dev/null) || true
sleep 1
mkdir -p %[2]s %[3]s %[4]s
rm -rf %[3]s/*
tar -xf %[1]s/storage.tar -C %[3]s
mkdir -p $(dirname %[5]s)
cp -f %[1]s/state.db %[5]s
tar -xf %[1]s/certs.tar -C %[4]s
`, frozenScratch, guestServer, guestStorage, guestServer, guestStateDB)
	if out, err := runContainer("exec", cfg.ServerName, "sh", "-c", seedScript); err != nil {
		return fmt.Errorf("seeding frozen state into guest failed: %s", strings.TrimSpace(out))
	}

	// Reboot the cluster cleanly so k3s comes up against the seeded
	// datastore; the kubelet then asks containerd for the workloads' images,
	// which containerd pulls from the pull-cache mirror and unpacks.
	logger.Info("frozen: rebooting cluster to rehydrate from the seeded state")
	if containerExists(cfg.ServerName, true) {
		_, _ = runContainer("stop", cfg.ServerName)
	}
	logger.Info("snapshot restored (frozen), thawing — image rehydration takes a few minutes")
	if err := Start(cfg); err != nil {
		return err
	}
	return nil
}

// verifyFrozenBlobs checks that every digest the manifest pins is present in
// the host pull-cache blob store, so a thaw never silently starts an
// incomplete cluster. Missing digests are reported by name.
func verifyFrozenBlobs(cfg *config.Config, dir string) error {
	m, err := readFrozenManifest(filepath.Join(dir, frozenManifestF))
	if err != nil {
		// No manifest: a thin import (re-pull on boot) or an old snapshot.
		// Nothing to verify; the normal pull path applies.
		return nil
	}
	blobs := filepath.Join(pullCacheDir(cfg), "blobs")
	var missing []string
	for _, d := range m.Digests {
		if _, err := os.Stat(filepath.Join(blobs, d)); err != nil {
			missing = append(missing, d)
		}
	}
	if len(missing) > 0 {
		preview := missing
		if len(preview) > 5 {
			preview = preview[:5]
		}
		return fmt.Errorf("cannot thaw: %d image digest(s) referenced by this snapshot are absent from the pull-cache (e.g. %s); the images cannot be rehydrated offline — re-pull them or import a fat bundle",
			len(missing), strings.Join(preview, ", "))
	}
	return nil
}

// waitGuestExec polls until `container exec` works on the freshly started
// server VM (the runtime needs a moment after start before exec attaches).
func waitGuestExec(cfg *config.Config) error {
	for i := 0; i < 60; i++ {
		if _, err := runContainer("exec", cfg.ServerName, "true"); err == nil {
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("cluster VM did not become reachable for the frozen thaw")
}
