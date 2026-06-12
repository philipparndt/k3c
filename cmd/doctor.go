package cmd

import (
	"github.com/spf13/cobra"

	"k3c/cluster"
)

var doctorShell bool

var doctorCmd = &cobra.Command{
	Use:   "doctor [CLUSTER]",
	Short: "Diagnose the host, daemons, egress, and cluster health",
	Long: `Diagnose the host, daemons, egress, and cluster health.

All checks are read-only. With --shell a debug pod (nicolaka/netshoot)
is started in the cluster and an interactive shell opened in it, for
testing DNS, egress, and service routing from a pod's perspective.`,
	Args: cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		if doctorShell {
			fail(cluster.DoctorShell(loadConfigDefault(args)))
			return
		}
		fail(cluster.Doctor(loadConfigDefault(args)))
	},
}

func init() {
	doctorCmd.Flags().BoolVar(&doctorShell, "shell", false,
		"open an interactive shell in an in-cluster debug pod")
	rootCmd.AddCommand(doctorCmd)
}
