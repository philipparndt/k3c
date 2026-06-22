package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/philipparndt/go-logger"
	"github.com/spf13/cobra"

	"k3c/cluster"
	"k3c/config"
)

var clusterCmd = &cobra.Command{
	Use:   "cluster",
	Short: "Manage clusters",
}

var clusterCreateCmd = &cobra.Command{
	Use:   "create [NAME]",
	Short: "Create a new cluster",
	Args:  cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		fail(cluster.Create(loadConfig(args)))
	},
}

var clusterImportRunCmd = &cobra.Command{
	Use:   "import-run FILE [NAME]",
	Short: "Create a cluster from an exported snapshot and restore it, in one step",
	Args:  cobra.RangeArgs(1, 2),
	Run: func(cmd *cobra.Command, args []string) {
		file := args[0]
		info, err := cluster.SnapshotArchiveInfo(file)
		fail(err)
		name := info.Cluster
		if len(args) > 1 {
			name = args[1]
		}
		if name == "" {
			fail(fmt.Errorf("the archive does not record a cluster name; pass one: k3c cluster import-run %s NAME", file))
		}
		// Use the snapshot's embedded cluster config unless the user passed
		// --config. Seed it as the cluster's persisted config so Resolve picks
		// it up (sizing, transparent egress, mirrors); the host-specific CA
		// bundle is regenerated from the host at create time regardless.
		if configFile == "" && info.Config != "" {
			persisted := filepath.Join(config.StateDir(), "clusters", name, "k3c.yaml")
			if err := os.MkdirAll(filepath.Dir(persisted), 0o755); err != nil {
				fail(err)
			}
			if err := os.WriteFile(persisted, []byte(info.Config), 0o644); err != nil {
				fail(err)
			}
			logger.Info("using the snapshot's embedded cluster config (override with --config)")
		}
		// Resolve the target cluster's config, then let the archive's CIDRs win
		// — the snapshot's datastore is baked with them, so the cluster must use
		// them or the restore is refused.
		cfg := loadConfig([]string{name})
		if info.ClusterCIDR != "" {
			cfg.ClusterCIDR = info.ClusterCIDR
		}
		if info.ServiceCIDR != "" {
			cfg.ServiceCIDR = info.ServiceCIDR
		}
		fail(cluster.ClusterImportRun(cfg, file))
	},
}

var deleteSnapshots bool

var clusterDeleteCmd = &cobra.Command{
	Use:   "delete [NAME]",
	Short: "Delete a cluster and its state",
	Args:  cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		fail(cluster.Delete(loadConfig(args), deleteSnapshots))
	},
}

var clusterStartCmd = &cobra.Command{
	Use:   "start [NAME]",
	Short: "Resume a stopped cluster",
	Args:  cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		fail(cluster.Start(loadConfigDefault(args)))
	},
}

var clusterStopCmd = &cobra.Command{
	Use:   "stop [NAME]",
	Short: "Stop a cluster, keeping its state",
	Args:  cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		fail(cluster.Stop(loadConfigDefault(args)))
	},
}

var clusterPauseCmd = &cobra.Command{
	Use:   "pause [NAME]",
	Short: "Freeze a running cluster in memory (instant resume, pods keep running)",
	Args:  cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		fail(cluster.Pause(loadConfigDefault(args)))
	},
}

var clusterResumeCmd = &cobra.Command{
	Use:   "resume [NAME]",
	Short: "Unfreeze a paused cluster",
	Args:  cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		fail(cluster.Resume(loadConfigDefault(args)))
	},
}

var clusterSuspendCmd = &cobra.Command{
	Use:   "suspend [NAME]",
	Short: "Suspend a cluster to disk, releasing CPU and memory (start restores it)",
	Args:  cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		fail(cluster.Suspend(loadConfigDefault(args)))
	},
}

var reclaimRelease bool

var clusterReclaimCmd = &cobra.Command{
	Use:   "reclaim [NAME]",
	Short: "Return memory the cluster no longer uses to the host (balloon stays sized to usage)",
	Args:  cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		fail(cluster.Reclaim(loadConfigDefault(args), reclaimRelease))
	},
}

var clusterActivateCmd = &cobra.Command{
	Use:     "activate [NAME]",
	Aliases: []string{"use"},
	Short:   "Make a cluster current: resume/start it, switch routing and kube context",
	Args:    cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		fail(cluster.Activate(loadConfigDefault(args)))
	},
}

var clusterListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List clusters",
	Args:    cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		fail(cluster.List(loadConfig(nil)))
	},
}

func init() {
	clusterDeleteCmd.Flags().BoolVar(&deleteSnapshots, "snapshots", false,
		"also delete the cluster's snapshots")
	clusterReclaimCmd.Flags().BoolVar(&reclaimRelease, "release", false,
		"deflate the balloon, giving the cluster its full configured memory back")
	clusterCmd.AddCommand(clusterCreateCmd, clusterImportRunCmd, clusterDeleteCmd, clusterStartCmd, clusterStopCmd,
		clusterPauseCmd, clusterResumeCmd, clusterSuspendCmd, clusterReclaimCmd, clusterActivateCmd, clusterListCmd)
	rootCmd.AddCommand(clusterCmd)
}
