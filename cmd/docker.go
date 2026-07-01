package cmd

import (
	"fmt"

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

var dockerBuildkitCmd = &cobra.Command{
	Use:   "buildkit [BUILDER]",
	Short: "Set up a buildx builder that works under k3c (cluster CA + proxy)",
	Long: `Create (or recreate) a docker-container buildx builder in the sidecar
that trusts the cluster CA and routes egress through the k3c proxy.

The sidecar's egress is TLS-intercepted and DNS-less, so the default BuildKit
container rejects every registry certificate and can't resolve hosts — even
though plain "docker build"/pull work. This provisions a builder that does, so
"docker buildx" image builds behave like they do on Docker Desktop / OrbStack.

BUILDER defaults to "multi-platform" (the common builder name in Makefiles), so
existing build tooling picks it up unchanged.`,
	Args: cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		name := ""
		if len(args) > 0 {
			name = args[0]
		}
		fail(cluster.DockerBuildkit(loadConfigDefault(nil), name))
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

var dockerActivateCmd = &cobra.Command{
	Use:   "activate",
	Short: "Make the sidecar the active target (it owns host ports it shares with the active cluster)",
	Long: `Make the docker sidecar the active target.

The sidecar then owns every host port both it and the active cluster publish
(contested ports), e.g. :443 ingress. The sidecar is brought up first.
Activating a cluster (incl. on start/resume) hands those ports back.`,
	Args: cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		fail(cluster.ActivateSidecar(loadConfigDefault(nil)))
	},
}

var dockerPrepareK3dCmd = &cobra.Command{
	Use:   "prepare-k3d",
	Short: "Bake the corporate CA into the configured k3s node images for nested k3d",
	Long: `Prepare the nested-k3d node images: pull each image in docker.k3sNodeImages,
bake the corporate CA into its trust store, and rebuild it at the sidecar's
native architecture (tagged identically, so k3d reuses it without a config
change). Run this once before 'k3d cluster create'.

This is no longer done automatically on 'docker up' — starting the engine
stays fast. The result is cached, so re-running is a no-op until the CA or
the configured images change. The sidecar is brought up if needed.`,
	Args: cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		cfg := loadConfigDefault(nil)
		fail(cluster.DockerUp(cfg, false))
		fail(cluster.PrepareK3sNodeImages(cfg))
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

var dockerReclaimRelease bool

var dockerReclaimCmd = &cobra.Command{
	Use:   "reclaim",
	Short: "Return unused sidecar memory to the host (balloons the VM down to its working set)",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		fail(cluster.DockerReclaim(loadConfigDefault(nil), dockerReclaimRelease))
	},
}

var dockerSnapshotCmd = &cobra.Command{
	Use:   "snapshot",
	Short: "Save/restore the sidecar (the whole image store: every nested k3d cluster)",
}

var (
	dockerSnapCold    bool
	dockerSnapReplace bool
)

var dockerSnapshotSaveCmd = &cobra.Command{
	Use:   "save NAME",
	Short: "Snapshot the sidecar (rootfs + image store) to a named, restorable state",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		fail(cluster.DockerSnapshotSave(loadConfigDefault(nil), args[0], dockerSnapCold, dockerSnapReplace))
	},
}

var dockerSnapshotRestoreCmd = &cobra.Command{
	Use:   "restore NAME",
	Short: "Restore the sidecar's image store from a snapshot (replaces current state)",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		fail(cluster.DockerSnapshotRestore(loadConfigDefault(nil), args[0], dockerSnapCold))
	},
}

var dockerSnapshotListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List sidecar snapshots",
	Args:    cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		for _, s := range cluster.DockerSnapshots(loadConfigDefault(nil)) {
			fmt.Printf("%-24s %-5s %s\n", s.Name, s.Mode, s.Created)
		}
	},
}

var dockerSnapshotDeleteCmd = &cobra.Command{
	Use:     "delete NAME",
	Aliases: []string{"rm"},
	Short:   "Delete a sidecar snapshot",
	Args:    cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		fail(cluster.DockerSnapshotDelete(loadConfigDefault(nil), args[0]))
	},
}

var dockerSnapshotRenameCmd = &cobra.Command{
	Use:     "rename OLD NEW",
	Aliases: []string{"mv"},
	Short:   "Rename a sidecar snapshot",
	Args:    cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		fail(cluster.DockerSnapshotRename(loadConfigDefault(nil), args[0], args[1]))
	},
}

func init() {
	dockerUpCmd.Flags().StringVar(&dockerUpCPUs, "cpus", "", "override sidecar CPU count (re-creates the sidecar)")
	dockerUpCmd.Flags().StringVar(&dockerUpMemory, "memory", "", "override sidecar memory, e.g. 32G (re-creates the sidecar)")
	dockerRmCmd.Flags().BoolVar(&dockerRmVolume, "volume", false, "also remove the image-store volume (deletes all sidecar data)")
	dockerSnapshotSaveCmd.Flags().BoolVar(&dockerSnapCold, "cold", false, "quiesce with a stop instead of a warm suspend")
	dockerSnapshotSaveCmd.Flags().BoolVar(&dockerSnapReplace, "replace", false, "recreate a same-named snapshot: delete the existing one, then save in its place")
	dockerSnapshotRestoreCmd.Flags().BoolVar(&dockerSnapCold, "cold", false, "boot fresh instead of resuming saved machine state")
	dockerReclaimCmd.Flags().BoolVar(&dockerReclaimRelease, "release", false,
		"deflate the balloon and give the sidecar its full configured memory again")
	dockerSnapshotCmd.AddCommand(dockerSnapshotSaveCmd, dockerSnapshotRestoreCmd, dockerSnapshotListCmd, dockerSnapshotDeleteCmd, dockerSnapshotRenameCmd)
	dockerCmd.AddCommand(dockerUpCmd, dockerActivateCmd, dockerDownCmd, dockerRmCmd, dockerStatusCmd, dockerEnvCmd,
		dockerPrepareK3dCmd, dockerPauseCmd, dockerResumeCmd, dockerSuspendCmd, dockerReclaimCmd, dockerSnapshotCmd,
		dockerBuildkitCmd)
	rootCmd.AddCommand(dockerCmd)
}
