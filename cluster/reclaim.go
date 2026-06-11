package cluster

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/philipparndt/go-logger"

	"k3c/config"
)

// reclaimHeadroom returns the memory left on top of the guest's used
// memory when sizing the balloon target: a quarter of the used memory, at
// least 2GB. Kubernetes and JVM-heavy workloads need real breathing room -
// the balloon has no deflate-on-OOM escape hatch, and a too-tight target
// starves the guest into an unresponsive cluster.
func reclaimHeadroomMB(usedMB int) int {
	if h := usedMB / 4; h > 2048 {
		return h
	}
	return 2048
}

// Reclaim returns memory the cluster no longer uses to the host. The VM's
// footprint only ever grows (every page the guest touches stays resident),
// so after image-heavy operations it can far exceed what the workload
// needs.
//
// Reclaim drops the guest's page caches and inflates the virtio memory
// balloon to the guest's used memory plus headroom; the host frees the
// ballooned pages. The balloon STAYS inflated — deflating immediately
// re-commits the memory — so the cluster keeps running within the smaller
// target. Run reclaim again after memory-hungry operations (it re-sizes to
// current usage), or release the full configured memory with release=true.
//
// Requires a container CLI with memory balloon support.
func Reclaim(cfg *config.Config, release bool) error {
	if !capabilities().memory {
		return fmt.Errorf("the configured container CLI does not support memory reclaim (set containerBinary to a build with balloon support)")
	}
	if !containerExists(cfg.ServerName, true) {
		return fmt.Errorf("cluster '%s' is not running", cfg.Cluster)
	}

	if release {
		if out, err := runContainer("memory", "target", cfg.ServerName, cfg.Memory); err != nil {
			return fmt.Errorf("releasing memory failed: %s", out)
		}
		logger.Info("balloon released: the cluster has its full " + cfg.Memory + " again")
		return nil
	}

	before := footprintMB(cfg.Cluster)

	// Deflate first: guest memory accounting must not include balloon
	// pages, and the host only acts on a fresh inflate.
	if out, err := runContainer("memory", "target", cfg.ServerName, cfg.Memory); err != nil {
		return fmt.Errorf("deflating balloon failed: %s", out)
	}
	time.Sleep(2 * time.Second)

	logger.Info("dropping guest page caches")
	if out, err := runContainer("exec", cfg.ServerName,
		"sh", "-c", "sync; echo 3 > /proc/sys/vm/drop_caches"); err != nil {
		return fmt.Errorf("dropping caches failed: %s", out)
	}

	usedMB, err := guestUsedMB(cfg)
	if err != nil {
		return err
	}
	target := usedMB + reclaimHeadroomMB(usedMB)
	logger.Info(fmt.Sprintf("reclaiming (guest uses %dMB, balloon target %dMB)", usedMB, target))
	if out, err := runContainer("memory", "target", cfg.ServerName,
		fmt.Sprintf("%dm", target)); err != nil {
		return fmt.Errorf("setting memory target failed: %s", out)
	}

	// The host frees the ballooned pages within seconds; wait until the
	// footprint settles.
	after := -1
	for i := 0; i < 12; i++ {
		time.Sleep(5 * time.Second)
		mb := footprintMB(cfg.Cluster)
		if mb < 0 {
			break
		}
		if after >= 0 && after-mb < 64 {
			after = mb
			break
		}
		after = mb
	}

	if before > 0 && after > 0 && before-after < 256 {
		logger.Warn(fmt.Sprintf("footprint barely moved (%dMB -> %dMB)", before, after))
		logger.Warn("memory of a freshly booted VM resists reclaim; one suspend/restore cycle")
		logger.Warn("(k3c cluster suspend && k3c cluster start) converts it, then reclaim works")
		return nil
	}
	logger.Info(fmt.Sprintf("reclaimed: %dMB -> %dMB (balloon stays at %dMB; rerun reclaim to re-size, --release for full memory)", before, after, target))
	return nil
}

// guestUsedMB returns the used memory inside the cluster's VM in MiB.
func guestUsedMB(cfg *config.Config) (int, error) {
	out, err := runContainer("exec", cfg.ServerName,
		"sh", "-c", "free -m | awk '/^Mem:/{print $3}'")
	if err != nil {
		return 0, fmt.Errorf("reading guest memory usage failed: %s", out)
	}
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) == 0 {
		return 0, fmt.Errorf("unexpected free output: %q", out)
	}
	used, err := strconv.Atoi(fields[len(fields)-1])
	if err != nil {
		return 0, fmt.Errorf("unexpected free output: %q", out)
	}
	return used, nil
}

// footprintMB returns the cluster VM's physical footprint in MiB, or -1.
func footprintMB(cluster string) int {
	ram := clusterRAM(cluster)
	if ram == "-" {
		return -1
	}
	val := strings.TrimRight(ram, "KMGTB")
	unit := strings.TrimPrefix(ram, val)
	f, err := strconv.ParseFloat(val, 64)
	if err != nil {
		return -1
	}
	switch {
	case strings.HasPrefix(unit, "G"):
		return int(f * 1024)
	case strings.HasPrefix(unit, "K"):
		return int(f / 1024)
	case strings.HasPrefix(unit, "T"):
		return int(f * 1024 * 1024)
	default:
		return int(f)
	}
}
