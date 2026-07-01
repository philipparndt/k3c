package cluster

import (
	"strconv"
	"strings"
	"time"

	"github.com/philipparndt/go-logger"

	"k3c/config"
)

// deprioritizeNice is the BSD nice value applied to the cluster's virtual
// machine processes when cpuPriority is enabled: the maximum (lowest
// priority), so interactive applications (video calls) always win CPU
// contention while the cluster still uses idle cores freely.
const deprioritizeNice = 20

// applyCPUPriority lowers the scheduling priority of the cluster's virtual
// machine processes with renice, so interactive applications always win CPU
// contention (video calls stay smooth through boot storms and reconcile
// bursts) while the cluster freely uses idle cores. Disabled with
// cpuPriority: normal.
//
// It uses renice (BSD setpriority) rather than a taskpolicy QoS clamp: the
// VM is an Apple-signed, hardened process, so setting its Mach task policy
// (taskpolicy -c utility) needs the target's task port — root-only — and
// silently no-ops for the non-root daemon. Lowering nice only requires
// owning the process, which the daemon does, so it actually takes effect.
// renice is deliberately CPU-only; taskpolicy -b (DARWIN_BG) would also
// throttle the guest's disk I/O and sockets to the utility tier, which risks
// etcd fsync latency and pod/API traffic on the k3s control plane.
func applyCPUPriority(cfg *config.Config) {
	if cfg.CPUPriority == "normal" {
		return
	}
	for _, name := range []string{cfg.ServerName, cfg.RegistryName} {
		pid := vzProcessPID(name)
		if pid == 0 {
			continue
		}
		if out, err := runOut("renice", strconv.Itoa(deprioritizeNice), "-p", strconv.Itoa(pid)); err != nil {
			logger.Debug("cpu priority for " + name + ": " + out)
		}
	}
}

// vmNice returns the BSD nice value of a VM's Virtualization.framework
// process and whether it was found. The TUI uses it to show whether the
// deprioritization is in effect (nice == deprioritizeNice) or has drifted
// back to the default (a respawn resets nice to 0). nice is stable — unlike
// the scheduling priority (ps pri), which floats with CPU activity — so it
// is the right signal to visualize.
func vmNice(name string) (int, bool) {
	pid := vzProcessPID(name)
	if pid == 0 {
		return 0, false
	}
	out, err := runOut("ps", "-o", "nice=", "-p", strconv.Itoa(pid))
	if err != nil {
		return 0, false
	}
	nice, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, false
	}
	return nice, true
}

// cpuPrioState reports a running VM's CPU-deprioritization state for display:
// "low" when it is deprioritized (nice >= deprioritizeNice), "drifted" when
// deprioritization is enabled but not currently in effect (a respawn reset
// nice; the reconcile re-asserts within a minute), or "" when disabled
// (cpuPriority: normal) or the process is unreadable.
func cpuPrioState(cpuPriority, vmName string) string {
	if cpuPriority == "normal" {
		return ""
	}
	nice, ok := vmNice(vmName)
	if !ok {
		return ""
	}
	if nice >= deprioritizeNice {
		return "low"
	}
	return "drifted"
}

// priorityReconcileInterval is how often the daemon re-asserts the CPU
// deprioritization. nice lives on the VM's Virtualization.framework process,
// so a respawn resets it to the default — and Apple's container runtime can
// restart a VM out-of-band (crash/health recovery) without any k3c lifecycle
// call to re-apply it. Re-asserting keeps the promise ("video calls stay
// smooth") self-healing within one interval.
const priorityReconcileInterval = time.Minute

// startPriorityReconcile launches the periodic re-assertion of the CPU
// deprioritization inside the daemon. applyCPUPriority only runs on k3c's own
// lifecycle operations (create/start/resume/restore); this covers VMs
// respawned behind k3c's back. Disabled with cpuPriority: normal.
func startPriorityReconcile(cfg *config.Config) {
	if cfg.CPUPriority == "normal" {
		logger.Info("cpu priority reconcile disabled (cpuPriority: normal)")
		return
	}
	logger.Info("cpu priority reconcile every " + priorityReconcileInterval.String())
	go func() {
		for {
			time.Sleep(priorityReconcileInterval)
			reassertCPUPriority(cfg)
		}
	}()
}

// reassertCPUPriority re-applies the nice deprioritization to every running
// managed VM: each running cluster's server and registry, plus the docker
// sidecar. It mirrors autoReclaimTick's iteration; applyCPUPriority is
// idempotent and skips a VM whose process is not found yet (a just-respawned
// VM is caught on the next tick), so this is safe to run unconditionally on
// the interval.
func reassertCPUPriority(daemonCfg *config.Config) {
	for name, parts := range clusterStates() {
		if parts["-server"] != "running" {
			continue
		}
		cfg := resolveClusterConfig(name)
		if cfg == nil || isPaused(cfg) {
			continue
		}
		applyCPUPriority(cfg)
	}
	if daemonCfg.DockerMemory != "" && containerExists(dockerName, true) && !isDockerPaused(daemonCfg) {
		applyCPUPriority(&config.Config{ServerName: dockerName, CPUPriority: daemonCfg.CPUPriority})
	}
}
