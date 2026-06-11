package cluster

import (
	"strings"

	"github.com/philipparndt/go-logger"

	"k3c/config"
)

// repairVirtiofs re-establishes the cluster's virtiofs shares after a
// restore from saved machine state. A virtiofs mount is a stateful session
// with the host-side share device; restoring a VM brings back the guest's
// mounted superblock but the host side is a fresh device instance, and the
// protocol has no reconnect — the mount can come back dead (every access
// fails with ENOENT) while the underlying device works fine. Containerd
// then cannot read the registry CA bundle and every image pull fails.
//
// The repair mounts a fresh session of the share tag and bind-mounts its
// directories over the dead mountpoints. No-op when the share is healthy.
func repairVirtiofs(cfg *config.Config) {
	if _, err := runContainer("exec", cfg.ServerName,
		"sh", "-c", "test -r /etc/rancher/k3s/registries.yaml"); err == nil {
		return
	}
	logger.Info("virtiofs shares did not survive the restore; remounting")
	script := `set -e
mnt=/run/k3c-vfs
mkdir -p $mnt
grep -q " $mnt " /proc/mounts || mount -t virtiofs virtiofs $mnt
etc=""; img=""
for d in $mnt/*/; do
  if [ -e "$d/registries.yaml" ]; then etc="$d"; else img="$d"; fi
done
[ -n "$etc" ] || { echo "no k3s-etc share found"; exit 1; }
umount -l /etc/rancher/k3s 2>/dev/null || true
mount --bind "$etc" /etc/rancher/k3s
if [ -n "$img" ] && [ -d /var/lib/rancher/k3s/agent/images ]; then
  umount -l /var/lib/rancher/k3s/agent/images 2>/dev/null || true
  mount --bind "$img" /var/lib/rancher/k3s/agent/images
fi`
	if out, err := runContainer("exec", cfg.ServerName, "sh", "-c", script); err != nil {
		logger.Warn("virtiofs repair failed: " + strings.TrimSpace(out))
		return
	}
	logger.Info("virtiofs shares remounted")
}
