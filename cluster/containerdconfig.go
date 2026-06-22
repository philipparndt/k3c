package cluster

import (
	"strings"

	"github.com/philipparndt/go-logger"

	"k3c/config"
)

// k3s's generated containerd config (v3 / containerd 2.x) ends with
//
//	imports = [".../config-v3.toml.d/*.toml"]
//
// so a fragment dropped into that directory is MERGED into the base config by
// containerd. We use that to enable discard_unpacked_layers on the CRI image
// service without redeclaring k3s's [plugins.'io.containerd.cri.v1.images']
// table. (Appending the table to a config.toml.tmpl instead produced a DUPLICATE
// table, which containerd rejects with exit status 1 — that crash blocked every
// cluster start. An imports drop-in merges, so it can never duplicate-crash.)
//
// WHY discard_unpacked_layers: once containerd unpacks a compressed layer into
// the overlayfs snapshot, the compressed blob is dead weight in the content
// store (~7 GB on vehub, duplicating the host pull-cache). Discarding it shrinks
// the live rootfs and every disk-level snapshot; the pull-cache (the first
// registry mirror) re-serves any discarded layer on demand, so this adds no
// internet dependency. Reversible: delete the drop-in and restart.
const containerdDropInDir = "/var/lib/rancher/k3s/agent/etc/containerd/config-v3.toml.d"
const containerdDropInName = "10-k3c-discard-unpacked-layers.toml"
const containerdDropIn = `version = 3

[plugins.'io.containerd.cri.v1.images']
  discard_unpacked_layers = true
`

// installContainerdConfig drops the discard_unpacked_layers fragment into k3s's
// containerd imports directory. Best-effort (a failure only forgoes the
// optimization, never breaks the cluster — hence logged, not returned),
// idempotent, and merged by containerd's imports so it cannot cause the
// duplicate-table crash the old append-template did. Takes effect on the next
// containerd start (k3s restart); newly unpacked layers are discarded from then.
func installContainerdConfig(cfg *config.Config) {
	// Written via the guest shell (the rootfs is the guest's ext4, not host-
	// mountable). mkdir -p the imports dir, which may not exist before first boot.
	script := "set -e\n" +
		"mkdir -p " + containerdDropInDir + "\n" +
		"cat > " + containerdDropInDir + "/" + containerdDropInName + " <<'K3C_EOF'\n" +
		containerdDropIn +
		"K3C_EOF\n"
	if out, err := runContainer("exec", cfg.ServerName, "sh", "-c", script); err != nil {
		logger.Debug("install containerd drop-in: " + strings.TrimSpace(out))
	}
}
