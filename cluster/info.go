package cluster

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"k3c/config"
)

// ClusterInfo describes one k3c cluster for list output and the TUI.
type ClusterInfo struct {
	Name     string `json:"name"`
	Server   string `json:"server"` // running, stopped, paused, suspended, ...
	Registry string `json:"registry"`
	RAM      string `json:"ram"`
	Context  string `json:"context"`
	Active   bool   `json:"active"`
	Kind     string `json:"kind,omitempty"` // "" for a cluster, "docker" for the docker sidecar
	// CPUPrio is the CPU-deprioritization state of a running VM: "low" when it
	// is deprioritized (renice'd so interactive apps win CPU contention),
	// "drifted" when deprioritization is enabled but not currently in effect (a
	// respawn reset it; the daemon re-asserts within a minute), or "" when
	// disabled (cpuPriority: normal) or the VM is not running.
	CPUPrio string `json:"cpuPrio,omitempty"`
}

// SnapshotInfo describes one saved snapshot.
type SnapshotInfo struct {
	Name    string
	Mode    string // warm, cold, or frozen
	Created string
	Size    int64 // on-disk allocated size of the snapshot directory, in bytes
}

// dirDiskUsage returns the total on-disk allocated size of a directory tree,
// in bytes. It sums st_blocks (sparse-aware), so the multi-GB-but-sparse
// rootfs images count only the blocks actually allocated — matching `du`
// rather than apparent size. Unreadable entries are skipped.
func dirDiskUsage(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if b := allocatedBytes(info); b > 0 {
			total += b
		} else {
			total += info.Size()
		}
		return nil
	})
	return total
}

// Clusters returns all k3c clusters (containers named <cluster>-server)
// with their state, sorted by name.
func Clusters(cfg *config.Config) []ClusterInfo {
	state := clusterStates()
	a := readActive(cfg)
	// while the sidecar is the active target, no cluster carries the ★
	active := ""
	if !a.Sidecar {
		active = a.Cluster
	}
	names := make([]string, 0, len(state))
	for cluster := range state {
		names = append(names, cluster)
	}
	sort.Strings(names)
	infos := make([]ClusterInfo, 0, len(names))
	for _, cluster := range names {
		parts := state[cluster]
		if parts["-server"] == "" {
			continue
		}
		server := parts["-server"]
		registry := parts["-registry"]
		if registry == "" {
			registry = "-"
		}
		// a paused cluster's containers still report "running"
		if _, err := os.Stat(filepath.Join(cfg.BaseDir, "clusters", cluster, "paused")); err == nil {
			server = "paused"
			if registry != "-" {
				registry = "paused"
			}
		}
		// a stopped server with saved machine state is a suspended cluster
		if server == "stopped" {
			if _, err := containerStateFilePath(cluster+"-server", vmstateFile); err == nil {
				server = "suspended"
			}
		}
		// resolve per cluster: picks up its persisted project config
		context := cfg.ContextPrefix() + cluster
		cpuPriority := cfg.CPUPriority
		if clusterCfg, err := config.Resolve(cluster, ""); err == nil {
			context = clusterCfg.KubeContext
			cpuPriority = clusterCfg.CPUPriority
		}
		cpuPrio := ""
		if server == "running" {
			cpuPrio = cpuPrioState(cpuPriority, cluster+"-server")
		}
		infos = append(infos, ClusterInfo{
			Name:     cluster,
			Server:   server,
			Registry: registry,
			RAM:      clusterRAM(cluster),
			Context:  context,
			Active:   cluster == active,
			CPUPrio:  cpuPrio,
		})
	}
	return infos
}

// Traffic returns a running cluster VM's cumulative external traffic
// (eth0 receive/transmit bytes).
func Traffic(cfg *config.Config, cluster string) (rx, tx int64, err error) {
	return trafficOf(cluster + "-server")
}

// MachineTraffic returns a machine's cumulative eth0 counters. For a cluster
// the VM is <name>-server; for the docker sidecar it is the dind container.
func MachineTraffic(cfg *config.Config, name, kind string) (rx, tx int64, err error) {
	container := name + "-server"
	if kind == "docker" {
		container = dockerName
	}
	return trafficOf(container)
}

func trafficOf(container string) (rx, tx int64, err error) {
	out, err := runContainer("exec", container, "cat",
		"/sys/class/net/eth0/statistics/rx_bytes",
		"/sys/class/net/eth0/statistics/tx_bytes")
	if err != nil {
		return 0, 0, fmt.Errorf("reading traffic counters: %s", out)
	}
	fields := strings.Fields(out)
	if len(fields) != 2 {
		return 0, 0, fmt.Errorf("unexpected counter output: %q", out)
	}
	rx, err = strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return 0, 0, err
	}
	tx, err = strconv.ParseInt(fields[1], 10, 64)
	return rx, tx, err
}

// Snapshots returns the saved snapshots of a cluster, sorted by name.
func Snapshots(cfg *config.Config, cluster string) []SnapshotInfo {
	return scanSnapshots(filepath.Join(cfg.BaseDir, "snapshots", cluster), "meta.yaml", "?")
}
