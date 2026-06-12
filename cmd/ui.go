package cmd

import (
	"github.com/spf13/cobra"

	"k3c/tui"
)

var uiCmd = &cobra.Command{
	Use:     "ui",
	Aliases: []string{"tui"},
	Short:   "Interactive terminal UI: clusters, snapshots, lifecycle",
	Args:    cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		fail(tui.Run(loadConfigDefault(nil)))
	},
}

func init() {
	rootCmd.AddCommand(uiCmd)
}
