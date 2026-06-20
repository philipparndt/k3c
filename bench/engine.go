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
}

// allEngines is every engine that may hold host :443 (or OrbStack) — used to
// quiesce the others before an engine's phase.
var allEngines = []string{"k3c", "orb", "rd", "k3d"}

func newEngine(name string) (Engine, error) {
	switch name {
	case "k3c":
		return &k3cEngine{cluster: benchCluster, config: os.Getenv("K3C_CONFIG")}, nil
	case "orb", "orbstack":
		return &orbEngine{}, nil
	case "rd", "rancher":
		return &rdEngine{}, nil
	case "k3d":
		return &k3dEngine{cluster: benchCluster}, nil
	}
	return nil, fmt.Errorf("unknown engine %q (k3c|orb|rd|k3d)", name)
}

const benchCluster = "bench"
