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
	frozenStateTar       = "frozen-state.tar"        // kine SQLite datastore: db + WAL/SHM, crash-consistent
	frozenStorageTar     = "frozen-storage.tar"      // /var/lib/rancher/k3s/storage (PVC data)
	frozenCertsTar       = "frozen-certs.tar"        // k3s server TLS + token
	frozenManifestF      = "frozen-images.yaml"      // image-digest closure manifest
	frozenLocalImagesTar = "frozen-local-images.tar" // OCI archive of local-only images (not in any remote registry)
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
	// LocalImages are the references whose blobs are not recoverable from a
	// remote registry (pushed to the local registry or `k3c image import`ed).
	// They are captured into frozenLocalImagesTar at save time so a slim/fat
	// bundle and a local thaw can restore them; recoverable images are re-pulled.
	LocalImages []string
}

// isLocalRegistryRef reports whether an image reference points at the local
// registry rather than a remote one. Locally pushed images use a localhost
// reference (k3c forwards `localhost:<registry-port>/…` into the cluster), so
// their blobs live only on this machine and must be bundled, never re-pulled.
//
// Limitation: an image `k3c image import`ed under a remote-looking tag (e.g.
// docker.io/me/app:dev that was never pushed) is not detected here and would be
// treated as recoverable — export such clusters fat, or push to the local
// registry.
func isLocalRegistryRef(ref string) bool {
	host := ref
	if i := strings.IndexByte(ref, '/'); i >= 0 {
		host = ref[:i]
	} else {
		return false // bare name → normalizes to docker.io (remote)
	}
	h := host
	if i := strings.IndexByte(h, ':'); i >= 0 {
		h = h[:i]
	}
	return h == "localhost" || h == "127.0.0.1"
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

	// 1. Crash-consistent copy of the kine SQLite datastore. The minimal k3s
	// guest image has no sqlite3 CLI, so rather than an online .backup we copy
	// the database together with its WAL/SHM sidecars in a single tar pass;
	// SQLite replays the WAL on first open, recovering the latest committed
	// state. This keeps the freeze window minimal (no stop) and matches the
	// crash-consistency a cold/power-loss restore already gives.
	logger.Info("frozen: copying k3s datastore (crash-consistent)")
	backupScript := fmt.Sprintf(`set -e
db=%[2]s; dir=$(dirname "$db"); base=$(basename "$db")
cd "$dir"
files="$base"
[ -f "$base-wal" ] && files="$files $base-wal"
[ -f "$base-shm" ] && files="$files $base-shm"
tar -cf %[1]s/state.tar $files`,
		frozenScratch, guestStateDB)
	if out, err := runContainer("exec", cfg.ServerName, "sh", "-c", backupScript); err != nil {
		return fmt.Errorf("copying k3s datastore failed: %s", strings.TrimSpace(out))
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
	// Capture the full bootstrap set k3s reconciles against the datastore —
	// token, tls/, AND cred/ (passwd, ipsec.psk). Omitting cred/ leaves the
	// live cluster's newer cred files in place on restore, which k3s refuses
	// to boot against ("newer than datastore"). tar preserves their mtimes, so
	// the restored files are not seen as newer than the restored datastore.
	certsScript := fmt.Sprintf(`set -e
cd %[2]s
items="token"
[ -d tls ] && items="$items tls"
[ -d cred ] && items="$items cred"
tar -cf %[1]s/certs.tar $items`,
		frozenScratch, guestServer)
	if out, err := runContainer("exec", cfg.ServerName, "sh", "-c", certsScript); err != nil {
		return fmt.Errorf("archiving server certs failed: %s", strings.TrimSpace(out))
	}

	// 4. Enumerate referenced images + their digest closure.
	manifest, err := enumerateFrozenImages(cfg)
	if err != nil {
		return err
	}

	// 4b. Capture local-only images (not recoverable from a remote registry)
	// as an OCI archive while the cluster is still running, so a thaw and a
	// slim/fat export can restore them without a re-pull.
	localTar, err := captureLocalImages(cfg, &manifest)
	if err != nil {
		return err
	}

	// Move the guest-written extract from the shared scratch into the
	// snapshot dir on the host.
	moves := []struct{ src, dst string }{
		{"state.tar", frozenStateTar},
		{"storage.tar", frozenStorageTar},
		{"certs.tar", frozenCertsTar},
	}
	if localTar {
		moves = append(moves, struct{ src, dst string }{"local-images.tar", frozenLocalImagesTar})
	}
	for _, m := range moves {
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
	if info, err := os.Stat(filepath.Join(dir, frozenStateTar)); err != nil || info.Size() == 0 {
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

// captureLocalImages exports the references in m.Images that point at the
// local registry into an OCI archive (scratch/local-images.tar) inside the
// guest and records them in m.LocalImages. It reports whether a tar was
// written. These images are not recoverable from a remote registry, so they
// travel in every non-thin bundle and are re-imported on thaw.
func captureLocalImages(cfg *config.Config, m *frozenManifest) (bool, error) {
	var local []string
	for _, ref := range m.Images {
		if isLocalRegistryRef(ref) {
			local = append(local, ref)
		}
	}
	if len(local) == 0 {
		return false, nil
	}
	logger.Info(fmt.Sprintf("frozen: archiving %d local-only image(s) not in any remote registry", len(local)))
	// Image references contain no shell metacharacters, so a plain join is safe.
	script := fmt.Sprintf("set -e; ctr -n k8s.io images export %s/local-images.tar %s",
		frozenScratch, strings.Join(local, " "))
	if out, err := runContainer("exec", cfg.ServerName, "sh", "-c", script); err != nil {
		return false, fmt.Errorf("exporting local-only images failed: %s", strings.TrimSpace(out))
	}
	m.LocalImages = local
	return true, nil
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
	b.WriteString("localImages:\n")
	for _, img := range m.LocalImages {
		b.WriteString("  - " + img + "\n")
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
		case trimmed == "localImages:":
			section = "localImages"
		case strings.HasPrefix(trimmed, "- "):
			v := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
			switch section {
			case "images":
				m.Images = append(m.Images, v)
			case "digests":
				m.Digests = append(m.Digests, v)
			case "localImages":
				m.LocalImages = append(m.LocalImages, v)
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

	// The guest ext4 is not host-mountable, so seed the extract guest-side.
	// Boot the server in SEED MODE (config.SeedModeMarker in the host-writable
	// /etc/rancher/k3s bind mount): the entrypoint idles instead of starting
	// k3s, so we can replace the datastore/PVC/creds while nothing holds them
	// (exec into a running k3s hangs, and k3s is PID 1 so it can't be stopped
	// from inside without killing the VM). Removed before the real boot.
	seedMarker := filepath.Join(cfg.K3sEtcDir(), config.SeedModeMarker)
	if err := os.WriteFile(seedMarker, nil, 0o644); err != nil {
		return err
	}
	defer os.Remove(seedMarker)

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
	seeds := []struct{ src, dst string }{
		{frozenStateTar, "state.tar"},
		{frozenStorageTar, "storage.tar"},
		{frozenCertsTar, "certs.tar"},
	}
	_, hasLocal := os.Stat(filepath.Join(dir, frozenLocalImagesTar))
	if hasLocal == nil {
		seeds = append(seeds, struct{ src, dst string }{frozenLocalImagesTar, "local-images.tar"})
	}
	for _, m := range seeds {
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
	// k3s is not running (seed mode), so we can replace its files directly.
	seedScript := fmt.Sprintf(`set -e
mkdir -p %[2]s %[3]s %[4]s
rm -rf %[3]s/*
tar -xf %[1]s/storage.tar -C %[3]s
mkdir -p $(dirname %[5]s)
rm -f %[5]s %[5]s-wal %[5]s-shm
tar -xf %[1]s/state.tar -C $(dirname %[5]s)
# Drop the live cluster's bootstrap files before restoring the snapshot's:
# any left in place would be newer than the restored datastore and k3s would
# refuse to boot. cred/ is regenerated from the datastore when absent; tls/ is
# restored from the snapshot (or likewise regenerated).
rm -rf %[4]s/cred %[4]s/tls
tar -xf %[1]s/certs.tar -C %[4]s
`, frozenScratch, guestServer, guestStorage, guestServer, guestStateDB)
	if out, err := runContainer("exec", cfg.ServerName, "sh", "-c", seedScript); err != nil {
		return fmt.Errorf("seeding frozen state into guest failed: %s", strings.TrimSpace(out))
	}

	// Reboot the cluster cleanly so k3s comes up against the seeded
	// datastore; the kubelet then asks containerd for the workloads' images,
	// which containerd pulls from the pull-cache mirror and unpacks.
	logger.Info("frozen: rebooting cluster to rehydrate from the seeded state")
	// Leave seed mode so the real boot starts k3s against the seeded datastore.
	_ = os.Remove(seedMarker)
	if containerExists(cfg.ServerName, true) {
		_, _ = runContainer("stop", cfg.ServerName)
	}
	logger.Info("snapshot restored (frozen), thawing — image rehydration takes a few minutes")
	if err := Start(cfg); err != nil {
		return err
	}

	// Local-only images are not in any registry to re-pull from, so import the
	// bundled OCI archive straight into containerd once it is back up.
	if hasLocal == nil {
		if err := waitGuestExec(cfg); err != nil {
			return err
		}
		logger.Info("frozen: importing local-only images into containerd")
		script := fmt.Sprintf("set -e; ctr -n k8s.io images import %s/local-images.tar", frozenScratch)
		if out, err := runContainer("exec", cfg.ServerName, "sh", "-c", script); err != nil {
			return fmt.Errorf("importing local-only images failed: %s", strings.TrimSpace(out))
		}
	}
	return nil
}

// cachedContentPath resolves a content digest to its on-disk path in the host
// pull-cache, reporting whether it exists. A frozen manifest enumerates every
// content-store digest (layers and configs AND image manifests), and the
// pull-cache stores manifests by digest under "types" while layer/config blobs
// live under "blobs" — so a digest must be looked up in both stores.
func cachedContentPath(cfg *config.Config, digest string) (string, bool) {
	for _, sub := range []string{"blobs", "types"} {
		p := filepath.Join(pullCacheDir(cfg), sub, digest)
		if _, err := os.Stat(p); err == nil {
			return p, true
		}
	}
	return "", false
}

// verifyFrozenBlobs checks that every digest the manifest pins is present in
// the host pull-cache (blob or manifest store), so a thaw never silently
// starts an incomplete cluster. Missing digests are reported by name.
func verifyFrozenBlobs(cfg *config.Config, dir string) error {
	m, err := readFrozenManifest(filepath.Join(dir, frozenManifestF))
	if err != nil {
		// No manifest: a thin import (re-pull on boot) or an old snapshot.
		// Nothing to verify; the normal pull path applies.
		return nil
	}
	var missing []string
	for _, d := range m.Digests {
		if _, ok := cachedContentPath(cfg, d); !ok {
			missing = append(missing, d)
		}
	}
	if len(missing) > 0 {
		// A bundled local-images archive carries images that are intentionally
		// absent from the pull-cache (locally pushed / imported); remote images
		// re-pull on boot. So missing digests are not fatal when it is present.
		if _, err := os.Stat(filepath.Join(dir, frozenLocalImagesTar)); err == nil {
			logger.Warn(fmt.Sprintf("frozen thaw: %d image digest(s) not in the pull-cache; relying on the bundled local images and a re-pull for the rest", len(missing)))
			return nil
		}
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
