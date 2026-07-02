package cluster

import (
	"fmt"

	"github.com/philipparndt/go-logger"

	"k3c/config"
)

// Runtime memory policy: container builds with memory-policy support run a
// per-VM controller that sizes the virtio balloon continuously — the balloon
// target follows the guest's workload plus headroom, returning unused memory
// to the host within seconds and deflating promptly when the guest runs low.
// k3c enables the policy at VM creation (memoryPolicyCreateArgs), re-arms it
// on VMs created before policy support (applyMemoryPolicy), and retires its
// own coarse reclaim loop when the runtime manages memory (see autoreclaim).

// memoryPolicyEnabled reports whether the runtime's automatic memory policy
// is configured and the container CLI supports it.
func memoryPolicyEnabled(cfg *config.Config) bool {
	return cfg.MemoryPolicy != "off" && capabilities().memoryPolicy
}

// memoryPolicyCreateArgs returns the `container run` flags that enable the
// runtime's automatic memory policy for a new VM.
func memoryPolicyCreateArgs(cfg *config.Config) []string {
	if !memoryPolicyEnabled(cfg) {
		return nil
	}
	args := []string{"--memory-policy", "auto"}
	if cfg.MemoryHeadroom != "" {
		args = append(args, "--memory-headroom", cfg.MemoryHeadroom)
	}
	return args
}

// applyMemoryPolicy arms the runtime's automatic memory policy on a running
// VM. VMs created before policy support carry no policy in their persisted
// configuration; the runtime route enables it for the current run, so
// existing clusters benefit without being recreated. Best-effort: an old
// runtime helper (started before the upgrade) lacks the route.
func applyMemoryPolicy(cfg *config.Config, name string) {
	if !memoryPolicyEnabled(cfg) {
		return
	}
	args := []string{"memory", "policy", name, "auto"}
	if cfg.MemoryHeadroom != "" {
		args = append(args, "--headroom", cfg.MemoryHeadroom)
	}
	if out, err := runContainer(args...); err != nil {
		logger.Debug("arming memory policy on " + name + " failed: " + firstLine(out))
	}
}

// memoryConvertEnabled reports whether freshly created VMs are converted
// with a suspend/restore cycle right after creation. Off by default: the
// cycle adds a failure surface to create (a wedged runtime suspend leaves
// the cluster suspended), and the first regular suspend or snapshot
// converts the VM anyway.
func memoryConvertEnabled(cfg *config.Config) bool {
	return cfg.MemoryConvert == "on-create" && memoryPolicyEnabled(cfg) && capabilities().suspend
}

// convertClusterMemory converts a freshly created cluster's VMs with one
// suspend/restore cycle: the hypervisor does not free ballooned pages of a
// freshly booted virtual machine — only of one restored from saved state.
// Without the cycle, memory touched during the k3s boot storm stays resident
// on the host until the first snapshot or suspend. A failed suspend is a
// harmless skip; a failed restore is fatal — the cluster must not be left
// suspended behind a "cluster is up".
func convertClusterMemory(cfg *config.Config) error {
	if !memoryConvertEnabled(cfg) {
		return nil
	}
	logger.Info("converting VM memory (one suspend/restore cycle enables host page reclaim)")
	if out, err := runContainer("suspend", cfg.ServerName); err != nil {
		logger.Warn("memory conversion skipped: " + firstLine(out))
		return nil
	}
	if out, err := startServerVM(cfg); err != nil {
		return fmt.Errorf("restoring the server after memory conversion failed (recover with: k3c cluster start %s): %s", cfg.Cluster, firstLine(out))
	}
	applyCPUPriority(cfg)
	repairVirtiofs(cfg)
	if cfg.RegistryEnabled && containerExists(cfg.RegistryName, true) {
		if _, err := runContainer("suspend", cfg.RegistryName); err == nil {
			_, _ = runContainer("start", cfg.RegistryName)
		}
	}
	return waitReady(cfg)
}

// convertDockerMemory converts the freshly created docker sidecar with one
// suspend/restore cycle (see convertClusterMemory) and brings it back up via
// the regular start path, which re-wires the engine socket, forwarder, and
// published ports.
func convertDockerMemory(cfg *config.Config) error {
	if !memoryConvertEnabled(cfg) {
		return nil
	}
	logger.Info("converting sidecar memory (one suspend/restore cycle enables host page reclaim)")
	if out, err := runContainer("suspend", dockerName); err != nil {
		logger.Warn("memory conversion skipped: " + firstLine(out))
		return nil
	}
	return DockerUp(cfg, false)
}
