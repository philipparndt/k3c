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

var dockerUpCmd = &cobra.Command{
	Use:     "up",
	Aliases: []string{"start"},
	Short:   "Start the Docker sidecar (created on first use)",
	Args:    cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		fail(cluster.DockerUp(loadConfigDefault(nil)))
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

func init() {
	dockerCmd.AddCommand(dockerUpCmd, dockerDownCmd, dockerStatusCmd, dockerEnvCmd)
	rootCmd.AddCommand(dockerCmd)
}
