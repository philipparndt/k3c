package cluster

import (
	"strings"

	"github.com/philipparndt/go-logger"

	"k3c/config"
)

// k3s reads this Go-template file when (re)generating its embedded containerd
// config.toml on every boot. We inherit k3s's full default config via the
// {{ template "base" . }} directive — which carries the registry mirror /
// registries.yaml wiring k3c depends on — and only append one override:
//
//	discard_unpacked_layers = true
//
// WHY: after containerd unpacks a compressed image layer into the overlayfs
// snapshot, the compressed blob is dead weight in the content store (~7 GB on
// the vehub cluster, duplicating data that is already in the host pull-cache).
// Discarding it shrinks both the live rootfs and every disk-level snapshot.
// This introduces NO internet dependency: the host pull-cache is local,
// permanent, and already the FIRST registry mirror, so any discarded layer is
// always re-servable on demand (e.g. for a frozen-snapshot thaw). Reversible:
// remove this file and k3s falls back to its default config on the next boot.
//
// The path inside the guest is k3s's documented custom-config-template
// location; k3s overlays it on the default rather than replacing it because of
// the {{ template "base" . }} include.
const containerdConfigTemplate = `{{ template "base" . }}

[plugins."io.containerd.grpc.v1.cri".containerd]
  discard_unpacked_layers = true
`

// containerdTmplPath is where k3s's embedded containerd looks for a custom
// config template (a Go template rendered into config.toml on each k3s boot).
const containerdTmplPath = "/var/lib/rancher/k3s/agent/etc/containerd/config.toml.tmpl"

// installContainerdConfig writes the discard_unpacked_layers template into the
// running server guest. It is idempotent (it overwrites with identical
// content) and best-effort: a failure only forgoes the layer-discard
// optimization, it never breaks the cluster, so the error is logged not
// returned. The setting takes effect when k3s next regenerates containerd's
// config (on restart); newly unpacked layers are discarded from then on.
func installContainerdConfig(cfg *config.Config) {
	// Write via the guest shell (the rootfs is the guest's ext4, not host-
	// mountable). Use a quoted heredoc so the Go-template braces reach the file
	// verbatim, and mkdir -p the agent dir which may not exist before first boot.
	script := "set -e\n" +
		"mkdir -p /var/lib/rancher/k3s/agent/etc/containerd\n" +
		"cat > " + containerdTmplPath + " <<'K3C_EOF'\n" +
		containerdConfigTemplate +
		"K3C_EOF\n"
	if out, err := runContainer("exec", cfg.ServerName, "sh", "-c", script); err != nil {
		logger.Debug("install containerd config template: " + strings.TrimSpace(out))
	}
}
