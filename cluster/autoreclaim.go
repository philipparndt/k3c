package cluster

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/philipparndt/go-logger"

	"k3c/config"
)

// startAutoReclaim launches the periodic memory reclaim inside the daemon
// process. Every interval, each running cluster whose footprint drifted
// well above its workload is reclaimed (see Reclaim). Disabled with
// autoReclaim: off, or when the container CLI lacks balloon support.
func startAutoReclaim(cfg *config.Config) {
	interval, ok := autoReclaimInterval(cfg.AutoReclaim)
	if !ok {
		logger.Info("auto-reclaim disabled")
		return
	}
	if !capabilities().memory {
		logger.Info("auto-reclaim unavailable: container CLI lacks balloon support")
		return
	}
	logger.Info("auto-reclaim every " + interval.String())
	go func() {
		// The balloon never deflates on its own: a workload growing into a
		// squeezed target suffocates the guest (memory pressure, reclaim
		// churn). Check for pressure every minute and re-size immediately;
		// the footprint-drift check runs on the configured interval.
		const pressureCheck = time.Minute
		elapsed := time.Duration(0)
		for {
			time.Sleep(pressureCheck)
			elapsed += pressureCheck
			drift := elapsed >= interval
			if drift {
				elapsed = 0
			}
			autoReclaimTick(cfg, drift)
		}
	}()
}

// pressureFloorMB is the guest-available threshold below which the balloon
// is re-sized immediately, regardless of footprint drift.
const pressureFloorMB = 2048

// autoReclaimInterval parses the config value: a duration enables the
// loop, "off"/"false"/"0" disable it.
func autoReclaimInterval(value string) (time.Duration, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "off", "false", "no", "0", "disabled":
		return 0, false
	}
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 {
		logger.Warn("invalid autoReclaim value " + value + "; disabling")
		return 0, false
	}
	if d < time.Minute {
		d = time.Minute
	}
	return d, true
}

// autoReclaimDriftMB is the footprint excess over the workload's target
// that triggers a reclaim: a quarter of the target, at least 4GB. Below
// it, reclaiming is not worth dropping the guest's page caches.
func autoReclaimDriftMB(targetMB int) int {
	if d := targetMB / 4; d > 4096 {
		return d
	}
	return 4096
}

func autoReclaimTick(daemonCfg *config.Config, drift bool) {
	for name, parts := range clusterStates() {
		if parts["-server"] != "running" {
			continue
		}
		cfg := resolveClusterConfig(name)
		if cfg == nil || isPaused(cfg) {
			continue
		}
		autoReclaimVM("cluster "+name, cfg.ServerName, drift, func() error {
			return Reclaim(cfg, false)
		})
	}
	autoReclaimDocker(daemonCfg, drift)
}

// autoReclaimDocker applies the reclaim policy to the docker sidecar VM,
// which is not a cluster server and so is invisible to the loop above. Its
// dind engine, any nested k3d cluster, and image-layer page cache otherwise
// grow the host footprint to the configured ceiling and never give it back.
func autoReclaimDocker(cfg *config.Config, drift bool) {
	if cfg == nil || cfg.DockerMemory == "" {
		return
	}
	if !containerExists(dockerName, true) || isDockerPaused(cfg) {
		return
	}
	autoReclaimVM("docker sidecar", dockerName, drift, func() error {
		return DockerReclaim(cfg, false)
	})
}

// autoReclaimVM applies the pressure + drift reclaim policy to a single VM,
// keyed by its container name. reclaim performs the actual balloon reclaim.
func autoReclaimVM(label, name string, drift bool, reclaim func() error) {
	_, used, available, err := guestMemMBOf(name)
	if err != nil {
		return
	}
	// A guest under memory pressure needs the balloon re-sized NOW:
	// reclaim deflates first, giving everything back instantly, then
	// re-measures the real usage and sizes the new target from it.
	if available < pressureFloorMB {
		logger.Warn(fmt.Sprintf("auto-reclaim %s: guest memory pressure (%dMB available), re-sizing balloon", label, available))
		if err := reclaim(); err != nil {
			logger.Warn("auto-reclaim " + label + ": " + err.Error())
		}
		return
	}
	if !drift {
		return
	}
	// NOTE: used includes inflated balloon pages, overstating the
	// target after a squeeze — fine for this check: post-squeeze the
	// footprint sits near the target anyway, so drift stays low until
	// the balloon is deflated (restart) or pressure re-sizes it.
	target := used + reclaimHeadroomMB(used)
	fp := vmFootprintMB(name)
	if fp < 0 || fp-target < autoReclaimDriftMB(target) {
		return
	}
	logger.Info(fmt.Sprintf("auto-reclaim %s: footprint %dMB, workload needs %dMB", label, fp, target))
	if err := reclaim(); err != nil {
		logger.Warn("auto-reclaim " + label + ": " + err.Error())
	}
}

// resolveClusterConfig loads a cluster's config from the daemon context,
// preferring its persisted project config over any file in the daemon's
// working directory.
func resolveClusterConfig(name string) *config.Config {
	persisted := filepath.Join(config.StateDir(), "clusters", name, "k3c.yaml")
	var cfg *config.Config
	var err error
	if _, statErr := os.Stat(persisted); statErr == nil {
		cfg, err = config.Resolve(name, persisted)
	} else {
		cfg, err = config.Resolve(name, "")
	}
	if err != nil {
		logger.Warn("auto-reclaim: cannot resolve config for " + name + ": " + err.Error())
		return nil
	}
	return cfg
}
