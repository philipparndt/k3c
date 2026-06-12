package cmd

import (
	"github.com/spf13/cobra"

	"k3c/cluster"
)

var (
	doctorShell      bool
	doctorShellRm    bool
	doctorShellImage string
)

var doctorCmd = &cobra.Command{
	Use:   "doctor [CLUSTER]",
	Short: "Diagnose the host, daemons, egress, and cluster health",
	Long: `Diagnose the host, daemons, egress, and cluster health.

All checks are read-only. With --shell a debug pod (nicolaka/netshoot
by default) is started in the cluster and an interactive shell opened
in it, for testing DNS, egress, and service routing from a pod's
perspective. --rm removes the pod (on shell exit, or standalone).`,
	Args: cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		switch {
		case doctorShell:
			fail(cluster.DoctorShell(loadConfigDefault(args), doctorShellImage, doctorShellRm))
		case doctorShellRm:
			fail(cluster.DoctorShellRemove(loadConfigDefault(args)))
		default:
			fail(cluster.Doctor(loadConfigDefault(args)))
		}
	},
}

func init() {
	doctorCmd.Flags().BoolVar(&doctorShell, "shell", false,
		"open an interactive shell in an in-cluster debug pod")
	doctorCmd.Flags().BoolVar(&doctorShellRm, "rm", false,
		"remove the debug pod (with --shell: when the shell exits)")
	doctorCmd.Flags().StringVar(&doctorShellImage, "image", "",
		"debug pod image (default docker.io/nicolaka/netshoot:latest)")
	rootCmd.AddCommand(doctorCmd)
}
