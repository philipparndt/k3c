package cmd

import (
	"github.com/spf13/cobra"

	"k3c/cluster"
)

var (
	doctorShell      bool
	doctorShellRm    bool
	doctorShellImage string
	doctorAttach     string
	doctorNamespace  string
	doctorContainer  string
)

var doctorCmd = &cobra.Command{
	Use:   "doctor [CLUSTER]",
	Short: "Diagnose the host, daemons, egress, and cluster health",
	Long: `Diagnose the host, daemons, egress, and cluster health.

All checks are read-only. With --shell a debug pod (nicolaka/netshoot
by default) is started in the cluster and an interactive shell opened
in it, for testing DNS, egress, and service routing from a pod's
perspective. --rm removes the pod (on shell exit, or standalone).

With --attach POD an ephemeral debug container (netshoot by default) is
injected into a running pod, sharing the target container's process
namespace — the way to get a shell into a distroless/scratch container
that ships no shell. The shell opens inside the target's filesystem
(/proc/1/root) with netshoot's tools on PATH; its processes are at /proc.
Use -n for the namespace and --container to pick the target (default: the
pod's first container).`,
	Args: cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		switch {
		case doctorAttach != "":
			fail(cluster.DoctorAttach(loadConfigDefault(args), doctorAttach, doctorNamespace, doctorContainer, doctorShellImage))
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
		"debug pod/container image (default nicolaka/netshoot, digest-pinned)")
	doctorCmd.Flags().StringVar(&doctorAttach, "attach", "",
		"inject a debug container into POD and open a shell (for distroless pods)")
	doctorCmd.Flags().StringVarP(&doctorNamespace, "namespace", "n", "",
		"namespace of the pod to --attach (default: default)")
	doctorCmd.Flags().StringVar(&doctorContainer, "container", "",
		"target container in the pod to --attach (default: first container)")
	rootCmd.AddCommand(doctorCmd)
}
