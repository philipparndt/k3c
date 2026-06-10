package cmd

import (
	"github.com/spf13/cobra"

	"k3c/cluster"
)

var imageCmd = &cobra.Command{
	Use:   "image",
	Short: "Manage images in a cluster",
}

var imageImportCmd = &cobra.Command{
	Use:   "import IMAGE [CLUSTER]",
	Short: "Import an image from the host image store into the cluster",
	Args:  cobra.RangeArgs(1, 2),
	Run: func(cmd *cobra.Command, args []string) {
		fail(cluster.ImageImport(loadConfig(args[1:]), args[0]))
	},
}

func init() {
	imageCmd.AddCommand(imageImportCmd)
	rootCmd.AddCommand(imageCmd)
}
