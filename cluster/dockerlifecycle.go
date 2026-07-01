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

// Docker sidecar lifecycle: pause/resume/suspend. The sidecar is an Apple
// `container` VM like a cluster server, so the same VM primitives apply — these
// mirror cluster.Pause/Resume/Suspend but target the single sidecar VM (no
// registry, no kube context, no public routing). Pausing the sidecar freezes
// the whole nested k3d cluster (every pod) in one operation, with instant
// resume; suspend releases its CPU/memory to disk and survives reboots.

func dockerPausedMarker(cfg *config.Config) string {
	return filepath.Join(cfg.BaseDir, "docker.paused")
}

func isDockerPaused(cfg *config.Config) bool {
	_, err := os.Stat(dockerPausedMarker(cfg))
	return err == nil
}

// DockerPause freezes the running sidecar VM in memory. Idempotent.
func DockerPause(cfg *config.Config) error {
	if !containerExists(dockerName, true) {
		return fmt.Errorf("docker sidecar is not running")
	}
	if capabilities().pause {
		if out, err := runContainer("pause", dockerName); err != nil {
			if strings.Contains(out, "not running") {
				logger.Info("docker sidecar is already paused")
				return nil
			}
			return fmt.Errorf("pause failed: %s", out)
		}
		if err := writeDockerPausedMarker(cfg, "native"); err != nil {
			return err
		}
		logger.Info("docker sidecar paused (in memory); resume with: k3c docker resume")
		logger.Info("note: a paused sidecar does not survive a host reboot")
		return nil
	}
	pids, err := vmProcessPIDs(dockerName)
	if err != nil {
		return err
	}
	alreadyFrozen := true
	for _, pid := range pids {
		if !processStopped(pid) {
			alreadyFrozen = false
		}
		if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
			return fmt.Errorf("freezing pid %d: %w", pid, err)
		}
	}
	fields := make([]string, len(pids))
	for i, pid := range pids {
		fields[i] = strconv.Itoa(pid)
	}
	if err := writeDockerPausedMarker(cfg, strings.Join(fields, " ")); err != nil {
		return err
	}
	if alreadyFrozen {
		logger.Info("docker sidecar is already paused")
		return nil
	}
	logger.Info("docker sidecar paused (in memory); resume with: k3c docker resume")
	logger.Info("note: a paused sidecar does not survive a host reboot")
	return nil
}

func writeDockerPausedMarker(cfg *config.Config, content string) error {
	if err := os.MkdirAll(cfg.BaseDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(dockerPausedMarker(cfg), []byte(content), 0o644)
}

// DockerResume unfreezes a paused sidecar and reactivates its docker context.
func DockerResume(cfg *config.Config) error {
	data, err := os.ReadFile(dockerPausedMarker(cfg))
	if err != nil {
		return fmt.Errorf("docker sidecar is not paused")
	}
	if strings.TrimSpace(string(data)) == "native" {
		if out, err := runContainer("resume", dockerName); err != nil {
			return fmt.Errorf("resume failed: %s", out)
		}
	} else {
		for _, field := range strings.Fields(string(data)) {
			pid, err := strconv.Atoi(field)
			if err != nil {
				continue
			}
			if err := syscall.Kill(pid, syscall.SIGCONT); err != nil {
				return fmt.Errorf("resuming pid %d: %w (host rebooted? use: k3c docker up)", pid, err)
			}
		}
	}
	_ = os.Remove(dockerPausedMarker(cfg))
	logger.Info("docker sidecar resumed")
	return dockerReady(cfg)
}

// dockerResumeIfPaused lifts a freeze before operations that need a responsive
// or stoppable sidecar VM (its engine cannot answer while frozen).
func dockerResumeIfPaused(cfg *config.Config) {
	data, err := os.ReadFile(dockerPausedMarker(cfg))
	if err != nil {
		return
	}
	logger.Info("docker sidecar is paused, resuming first")
	if strings.TrimSpace(string(data)) == "native" {
		_, _ = runContainer("resume", dockerName)
	} else {
		for _, field := range strings.Fields(string(data)) {
			if pid, err := strconv.Atoi(field); err == nil {
				_ = syscall.Kill(pid, syscall.SIGCONT)
			}
		}
	}
	_ = os.Remove(dockerPausedMarker(cfg))
}

// DockerSuspend saves the sidecar VM to disk and releases its CPU and memory.
// `k3c docker up` restores it, also after a reboot. Requires suspend support.
func DockerSuspend(cfg *config.Config) error {
	if !capabilities().suspend {
		return fmt.Errorf("the configured container CLI does not support suspend; use 'k3c docker down' (or set containerBinary to a build with suspend support)")
	}
	dockerResumeIfPaused(cfg)
	if !containerExists(dockerName, true) {
		return fmt.Errorf("docker sidecar is not running")
	}
	logger.Info("suspending docker sidecar to disk")
	if out, err := runContainer("suspend", dockerName); err != nil {
		return fmt.Errorf("suspend failed: %s", out)
	}
	if cfg.TransparentEgress {
		stopGvnet(cfg, dockerName)
	}
	logger.Info("docker sidecar suspended (CPU and memory released, survives reboots)")
	logger.Info("restore with: k3c docker up")
	return nil
}

// dockerSidecarState returns the sidecar's lifecycle state (running, paused,
// suspended, stopped) and whether the sidecar exists at all.
func dockerSidecarState(cfg *config.Config) (string, bool) {
	if !containerExists(dockerName, false) {
		return "", false
	}
	if containerExists(dockerName, true) {
		if isDockerPaused(cfg) {
			return "paused", true
		}
		return "running", true
	}
	if _, err := containerStateFilePath(dockerName, vmstateFile); err == nil {
		return "suspended", true
	}
	return "stopped", true
}

// DockerSidecarInfo returns the sidecar as a ClusterInfo row (Kind "docker")
// for listings/the TUI, and whether it should be shown (it exists).
func DockerSidecarInfo(cfg *config.Config) (ClusterInfo, bool) {
	state, ok := dockerSidecarState(cfg)
	if !ok {
		return ClusterInfo{}, false
	}
	cpuPrio := ""
	if state == "running" {
		cpuPrio = cpuPrioState(cfg.CPUPriority, dockerName)
	}
	return ClusterInfo{
		Name:     "docker",
		Server:   state,
		Registry: "-",
		// Measured live footprint via humanBytes, exactly like cluster servers
		// ("-" when stopped) — not the raw configured DockerMemory, which
		// rendered differently (e.g. "48G" vs "25.8 GB").
		RAM:     vmRAM(dockerName),
		Context: cfg.DockerContext,
		Kind:    "docker",
		Active:  readActive(cfg).Sidecar,
		CPUPrio: cpuPrio,
	}, true
}

// ActivateSidecar makes the docker sidecar the active target: it owns the host
// ports it shares with the active cluster (contested ports). The sidecar is
// brought up (resumed if paused) first, mirroring cluster activation.
func ActivateSidecar(cfg *config.Config) error {
	if isDockerPaused(cfg) {
		if err := DockerResume(cfg); err != nil {
			return err
		}
	}
	if err := DockerUp(cfg, false); err != nil {
		return err
	}
	return setActiveSidecar(cfg)
}
