package cmd

import (
	"github.com/spf13/cobra"

	"k3c/cluster"
)

var dockerCmd = &cobra.Command{
	Use:   "docker",
	Short: "Manage the Docker sidecar (a docker:dind VM with a real engine API)",
	Long: `Manage the Docker sidecar: a docker:dind VM managed by k3c that
provides a real Docker Engine API for Testcontainers, the docker CLI,
and other tools. Pulls go through the k3c proxy (and pull cache for
docker.io); the image store lives on a volume surviving recreation.

'docker up' activates the "k3c" docker context, so the docker CLI and
Testcontainers use the sidecar automatically; for shells/CI that prefer
an env var, use: eval $(k3c docker env)`,
}

var (
	dockerUpCPUs   string
	dockerUpMemory string
)

var dockerUpCmd = &cobra.Command{
	Use:     "up",
	Aliases: []string{"start"},
	Short:   "Start the Docker sidecar (created on first use)",
	Long: `Start the Docker sidecar, creating it on first use.

--cpus and --memory override the sidecar's resources. Because a VM's
resources are fixed at creation, passing either flag re-creates an existing
sidecar (the image-store volume is preserved).`,
	Args: cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		cfg := loadConfigDefault(nil)
		recreate := false
		if cmd.Flags().Changed("cpus") {
			cfg.DockerCPUs = dockerUpCPUs
			recreate = true
		}
		if cmd.Flags().Changed("memory") {
			cfg.DockerMemory = dockerUpMemory
			recreate = true
		}
		fail(cluster.DockerUp(cfg, recreate))
	},
}

var dockerDownCmd = &cobra.Command{
	Use:     "down",
	Aliases: []string{"stop"},
	Short:   "Stop the Docker sidecar (the image store is kept)",
	Args:    cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		fail(cluster.DockerDown(loadConfigDefault(nil)))
	},
}

var dockerRmVolume bool

var dockerRmCmd = &cobra.Command{
	Use:   "rm",
	Short: "Remove the Docker sidecar (recreate with 'up'; image store kept unless --volume)",
	Long: `Remove the Docker sidecar container so 'docker up' recreates it
(e.g. to change --cpus/--memory). The image-store volume is preserved unless
--volume is given.`,
	Args: cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		fail(cluster.DockerRemove(loadConfigDefault(nil), dockerRmVolume))
	},
}

var dockerStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the Docker sidecar state and endpoint",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		fail(cluster.DockerStatus(loadConfigDefault(nil)))
	},
}

var dockerEnvCmd = &cobra.Command{
	Use:   "env",
	Short: "Print shell exports for the sidecar engine (eval $(k3c docker env))",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		fail(cluster.DockerEnv(loadConfigDefault(nil)))
	},
}

var dockerPauseCmd = &cobra.Command{
	Use:   "pause",
	Short: "Freeze the sidecar in memory (instant resume; freezes the whole nested cluster)",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		fail(cluster.DockerPause(loadConfigDefault(nil)))
	},
}

var dockerResumeCmd = &cobra.Command{
	Use:   "resume",
	Short: "Unfreeze a paused sidecar",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		fail(cluster.DockerResume(loadConfigDefault(nil)))
	},
}

var dockerSuspendCmd = &cobra.Command{
	Use:   "suspend",
	Short: "Suspend the sidecar to disk, releasing CPU and memory (docker up restores it)",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		fail(cluster.DockerSuspend(loadConfigDefault(nil)))
	},
}

func init() {
	dockerUpCmd.Flags().StringVar(&dockerUpCPUs, "cpus", "", "override sidecar CPU count (re-creates the sidecar)")
	dockerUpCmd.Flags().StringVar(&dockerUpMemory, "memory", "", "override sidecar memory, e.g. 32G (re-creates the sidecar)")
	dockerRmCmd.Flags().BoolVar(&dockerRmVolume, "volume", false, "also remove the image-store volume (deletes all sidecar data)")
	dockerCmd.AddCommand(dockerUpCmd, dockerDownCmd, dockerRmCmd, dockerStatusCmd, dockerEnvCmd,
		dockerPauseCmd, dockerResumeCmd, dockerSuspendCmd)
	rootCmd.AddCommand(dockerCmd)
}
