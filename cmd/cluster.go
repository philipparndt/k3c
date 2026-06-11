package cmd

import (
	"github.com/spf13/cobra"

	"k3c/cluster"
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

var clusterDeleteCmd = &cobra.Command{
	Use:   "delete [NAME]",
	Short: "Delete a cluster and its state",
	Args:  cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		fail(cluster.Delete(loadConfig(args)))
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
	clusterReclaimCmd.Flags().BoolVar(&reclaimRelease, "release", false,
		"deflate the balloon, giving the cluster its full configured memory back")
	clusterCmd.AddCommand(clusterCreateCmd, clusterDeleteCmd, clusterStartCmd, clusterStopCmd,
		clusterPauseCmd, clusterResumeCmd, clusterSuspendCmd, clusterReclaimCmd, clusterActivateCmd, clusterListCmd)
	rootCmd.AddCommand(clusterCmd)
}
