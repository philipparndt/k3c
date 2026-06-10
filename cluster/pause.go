package cluster

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/philipparndt/go-logger"

	"k3c/config"
)

// Pause/resume freeze the cluster's VM processes with SIGSTOP/SIGCONT:
// resuming is instant and every pod keeps running — no restart cascade,
// no crash-loop backoffs. The frozen state lives in memory only: it does
// not survive a host reboot (use stop/start or snapshots for that), and
// the paused VM keeps its memory allocated.

func pausedMarker(cfg *config.Config) string {
	return filepath.Join(cfg.RunDir(), "paused")
}

// vmProcessPIDs finds the processes backing a container: the runtime
// supervisor (which also forwards the published ports) and the
// Virtualization.framework process hosting the guest CPUs and memory.
// Both must be frozen — freezing only the supervisor leaves the guest
// running (and burning CPU) with its network cut off.
func vmProcessPIDs(name string) ([]int, error) {
	out, err := runOut("pgrep", "-f", "container-runtime-linux .*--uuid "+name+"$")
	if err != nil || out == "" {
		return nil, fmt.Errorf("no VM process found for container %s", name)
	}
	helper, err := strconv.Atoi(strings.Fields(out)[0])
	if err != nil {
		return nil, err
	}
	pids := []int{helper}
	if vz := vzProcessPID(name); vz != 0 {
		pids = append(pids, vz)
	}
	return pids, nil
}

// vzProcessPID finds the Virtualization.framework process of a container
// via its open root filesystem image (0 if not found).
func vzProcessPID(name string) int {
	path, err := containerRootfsPath(name)
	if err != nil {
		return 0
	}
	out, err := runOut("lsof", "-t", path)
	if err != nil || out == "" {
		return 0
	}
	pid, err := strconv.Atoi(strings.Fields(out)[0])
	if err != nil {
		return 0
	}
	return pid
}

// Pause freezes a running cluster in memory.
func Pause(cfg *config.Config) error {
	if _, err := os.Stat(pausedMarker(cfg)); err == nil {
		return fmt.Errorf("cluster '%s' is already paused", cfg.Cluster)
	}
	if !containerExists(cfg.ServerName, true) {
		return fmt.Errorf("cluster '%s' is not running", cfg.Cluster)
	}
	pids, err := vmProcessPIDs(cfg.ServerName)
	if err != nil {
		return err
	}
	if registryPids, err := vmProcessPIDs(cfg.RegistryName); err == nil {
		pids = append(pids, registryPids...)
	}
	for _, pid := range pids {
		if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
			return fmt.Errorf("freezing pid %d: %w", pid, err)
		}
	}
	if err := os.MkdirAll(cfg.RunDir(), 0o755); err != nil {
		return err
	}
	fields := make([]string, len(pids))
	for i, pid := range pids {
		fields[i] = strconv.Itoa(pid)
	}
	if err := os.WriteFile(pausedMarker(cfg), []byte(strings.Join(fields, " ")), 0o644); err != nil {
		return err
	}
	logger.Info("cluster '" + cfg.Cluster + "' paused (in memory); resume with: k3c cluster resume " + cfg.Cluster)
	logger.Info("note: a paused cluster does not survive a host reboot")
	return nil
}

// Resume unfreezes a paused cluster.
func Resume(cfg *config.Config) error {
	data, err := os.ReadFile(pausedMarker(cfg))
	if err != nil {
		return fmt.Errorf("cluster '%s' is not paused", cfg.Cluster)
	}
	for _, field := range strings.Fields(string(data)) {
		pid, err := strconv.Atoi(field)
		if err != nil {
			continue
		}
		if err := syscall.Kill(pid, syscall.SIGCONT); err != nil {
			return fmt.Errorf("resuming pid %d: %w (host rebooted? use: k3c cluster start)", pid, err)
		}
	}
	_ = os.Remove(pausedMarker(cfg))
	_ = loadPorts(cfg)
	if err := waitReady(cfg); err != nil {
		return err
	}
	if err := setActive(cfg); err != nil {
		return err
	}
	logger.Info("cluster '" + cfg.Cluster + "' resumed (public ingress/registry now route here)")
	return nil
}

// resumeIfPaused lifts a freeze before operations that need a responsive
// or stoppable VM.
func resumeIfPaused(cfg *config.Config) {
	if data, err := os.ReadFile(pausedMarker(cfg)); err == nil {
		logger.Info("cluster is paused, resuming first")
		for _, field := range strings.Fields(string(data)) {
			if pid, err := strconv.Atoi(field); err == nil {
				_ = syscall.Kill(pid, syscall.SIGCONT)
			}
		}
		_ = os.Remove(pausedMarker(cfg))
	}
}

// isPaused reports whether the cluster is currently frozen.
func isPaused(cfg *config.Config) bool {
	_, err := os.Stat(pausedMarker(cfg))
	return err == nil
}
