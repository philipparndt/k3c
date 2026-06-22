package cluster

import "k3c/config"

// installContainerdConfig previously installed a k3s containerd config template
// that enabled discard_unpacked_layers by appending:
//
//	[plugins."io.containerd.grpc.v1.cri".containerd]
//	  discard_unpacked_layers = true
//
// after {{ template "base" . }}. But k3s's base config already declares that
// table, so the rendered config.toml had a DUPLICATE table — which containerd
// rejects, exiting with status 1 on startup. That killed k3s before the node
// could go Ready and blocked every cluster create/start.
//
// The setting has to be MERGED into the existing CRI-containerd table (and the
// exact placement differs for the containerd 2.x config k3s v1.36 ships), not
// appended as a second table. Until that is implemented and tested against the
// live guest, this is a deliberate no-op so the layer-discard optimization can
// never break cluster startup. The frozen/cold tiers do not depend on it.
func installContainerdConfig(_ *config.Config) {}
