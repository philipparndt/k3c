package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"k3c/cluster"
	"k3c/runtime"
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
		loadConfig(args).Print()
	},
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Show version information",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println(version.Get().String())
		if v := runtime.BundledContainerVersion(); v != "" {
			fmt.Println("bundled container: " + v)
		}
	},
}

// daemonsCmd runs the host-side proxy and SNI gateway in the foreground.
// It is spawned detached by cluster create/start.
var daemonsCmd = &cobra.Command{
	Use:    "daemons",
	Hidden: true,
	Args:   cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		fail(cluster.RunDaemons(loadConfig(nil)))
	},
}

func init() {
	configCmd.AddCommand(configViewCmd)
	rootCmd.AddCommand(statusCmd, configCmd, versionCmd, daemonsCmd)
}
