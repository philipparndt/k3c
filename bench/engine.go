package main

import (
	"context"
	"fmt"
	"os"
)

// Engine is one container/cluster runtime under test. Implementations only exec
// the engine's own CLI (k3c, orb, rdctl, k3d) so the comparison is fair and
// auditable. Lifecycle methods are timed by the benchmarks, not here.
type Engine interface {
	Name() string                       // canonical label (results key + report column)
	Addons() []string                   // kube-system deployments that gate "usable"
	EnergyPatterns() []string           // host command substrings to attribute energy to
	ColdPrep(ctx context.Context) error // prepare a cold run (caches/cluster cleared)
	WarmPrep(ctx context.Context) error // prepare a warm run (caches/VM primed)
	Create(ctx context.Context) (Kube, error)
	Destroy(ctx context.Context) error
	Suspend(ctx context.Context) error // release (suspend-to-disk / k8s stop / shutdown)
	Resume(ctx context.Context) error  // restore the released cluster
	StopAll(ctx context.Context) error // free host :443 etc. for another engine

	// Docker-engine benchmarks (e.g. edx). DockerContext is the `docker
	// --context` name, or "" if the engine provides no standalone docker engine
	// (k3d runs inside OrbStack's). DockerUp ensures that engine is running.
	DockerContext() string
	DockerUp(ctx context.Context) error
}

// providers are the host runtimes that are mutually exclusive (each owns a VM /
// host ports). Before an engine's phase the OTHER providers are quiesced.
var providers = []string{"k3c", "orb", "rd", "colima"}

// providerOf maps an engine to the host provider it occupies, so k3d-on-X
// shares X's provider (and isn't quiesced out from under itself).
func providerOf(name string) string {
	switch name {
	case "k3c":
		return "k3c"
	case "orb", "orbstack", "orb-k3d", "k3d":
		return "orb"
	case "rd", "rancher", "rancher-k3d":
		return "rd"
	case "colima", "colima-k3d":
		return "colima"
	}
	return name
}

func newEngine(name string) (Engine, error) {
	switch name {
	case "k3c":
		return &k3cEngine{cluster: benchCluster, config: os.Getenv("K3C_CONFIG")}, nil
	case "orb", "orbstack":
		return &orbEngine{}, nil
	case "rd", "rancher":
		return &rdEngine{}, nil
	case "colima":
		return &colimaEngine{}, nil
	case "orb-k3d", "k3d": // k3d alias defaults to the OrbStack backend
		return &k3dEngine{cluster: benchCluster, label: "orb-k3d", backend: &orbEngine{}}, nil
	case "rancher-k3d":
		return &k3dEngine{cluster: benchCluster, label: "rancher-k3d", backend: &rdEngine{}}, nil
	case "colima-k3d":
		return &k3dEngine{cluster: benchCluster, label: "colima-k3d", backend: &colimaEngine{}}, nil
	}
	return nil, fmt.Errorf("unknown engine %q (k3c|orb|rancher|colima|orb-k3d|rancher-k3d|colima-k3d)", name)
}

const benchCluster = "bench"
