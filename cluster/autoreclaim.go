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
		for {
			time.Sleep(interval)
			autoReclaimTick()
		}
	}()
}

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

func autoReclaimTick() {
	for name, parts := range clusterStates() {
		if parts["-server"] != "running" {
			continue
		}
		cfg := resolveClusterConfig(name)
		if cfg == nil || isPaused(cfg) {
			continue
		}
		used, err := guestUsedMB(cfg)
		if err != nil {
			continue
		}
		target := used + reclaimHeadroomMB(used)
		fp := footprintMB(name)
		if fp < 0 || fp-target < autoReclaimDriftMB(target) {
			continue
		}
		logger.Info(fmt.Sprintf("auto-reclaim %s: footprint %dMB, workload needs %dMB", name, fp, target))
		if err := Reclaim(cfg, false); err != nil {
			logger.Warn("auto-reclaim " + name + ": " + err.Error())
		}
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
