package cmd

import (
	"github.com/spf13/cobra"

	"k3c/cluster"
	"k3c/runtime"
	"k3c/ui"
	"k3c/version"
)

var statusCmd = &cobra.Command{
	Use:   "status [NAME]",
	Short: "Show cluster, daemon, and node status",
	Args:  cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		fail(cluster.Status(loadConfigDefault(args)))
	},
}

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage the k3c configuration",
}

var configViewCmd = &cobra.Command{
	Use:   "view [NAME]",
	Short: "Show the effective configuration",
	Args:  cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		cfg := loadConfig(args)
		if ui.JSON() {
			fail(ui.EmitJSON(cfg.View()))
			return
		}
		cfg.Print()
	},
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Show version information",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		info := version.Get()
		bundled := runtime.BundledContainerVersion()
		if ui.JSON() {
			fail(ui.EmitJSON(struct {
				version.Info
				BundledContainer string `json:"bundledContainer,omitempty"`
			}{info, bundled}))
			return
		}
		ui.Section("k3c")
		ui.KV("version", info.Version, 10)
		ui.KV("commit", info.GitCommit, 10)
		ui.KV("built", info.BuildDate, 10)
		ui.KV("go", info.GoVersion, 10)
		ui.KV("platform", info.Platform, 10)
		if bundled != "" {
			ui.KV("bundled", bundled, 10)
		}
	},
}

// daemonsCmd groups the host-daemon subcommands (proxy, SNI gateway, egress,
// webhook). Invoked bare it prints help; the foreground worker that cluster
// create/start spawn detached lives under `daemons run`.
var daemonsCmd = &cobra.Command{
	Use:   "daemons",
	Short: "Manage the host daemons (proxy, SNI gateway, egress, webhook)",
}

// daemonsRunCmd runs the host daemons in the foreground. It is the worker
// that cluster create/start spawn detached; run by hand it serves until
// interrupted and refuses to start a second copy over a running one.
var daemonsRunCmd = &cobra.Command{
	Use:   "run",
	Short: "Run the host daemons in the foreground (used internally by cluster start)",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		fail(cluster.RunDaemons(loadConfig(nil)))
	},
}

var daemonsStatusCmd = &cobra.Command{
	Use:     "status",
	Aliases: []string{"list", "ls"},
	Short:   "Show the daemons' process and listener state",
	Args:    cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		fail(cluster.DaemonsStatus(loadConfigDefault(nil)))
	},
}

var daemonsRestartCmd = &cobra.Command{
	Use:   "restart",
	Short: "Stop the daemons and spawn them fresh (picks up config changes)",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		fail(cluster.RestartDaemons(loadConfigDefault(nil)))
	},
}

var daemonsStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the host daemons (cluster start spawns them again)",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		cluster.StopDaemons(loadConfigDefault(nil))
	},
}

func init() {
	configCmd.AddCommand(configViewCmd)
	daemonsCmd.AddCommand(daemonsRunCmd, daemonsStatusCmd, daemonsRestartCmd, daemonsStopCmd)
	rootCmd.AddCommand(statusCmd, configCmd, versionCmd, daemonsCmd)
}
