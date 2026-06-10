package cmd

import (
	"github.com/spf13/cobra"

	"k3c/cluster"
)

var snapshotCmd = &cobra.Command{
	Use:   "snapshot",
	Short: "Snapshot and restore cluster state (instant, APFS copy-on-write)",
}

// snapshotArgs splits [CLUSTER] [SNAPSHOT] arguments.
func snapshotArgs(args []string) (clusterArgs []string, snapshot string) {
	if len(args) > 0 {
		clusterArgs = args[:1]
	}
	if len(args) > 1 {
		snapshot = args[1]
	}
	return clusterArgs, snapshot
}

var snapshotSaveCmd = &cobra.Command{
	Use:   "save [CLUSTER] [NAME]",
	Short: "Save a snapshot (default name: timestamp)",
	Args:  cobra.MaximumNArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		clusterArgs, name := snapshotArgs(args)
		fail(cluster.SnapshotSave(loadConfigDefault(clusterArgs), name))
	},
}

var snapshotRestoreCmd = &cobra.Command{
	Use:   "restore [CLUSTER] NAME",
	Short: "Restore a snapshot and start the cluster",
	Args:  cobra.RangeArgs(1, 2),
	Run: func(cmd *cobra.Command, args []string) {
		// with a single argument it is the snapshot name
		if len(args) == 1 {
			fail(cluster.SnapshotRestore(loadConfigDefault(nil), args[0]))
			return
		}
		clusterArgs, name := snapshotArgs(args)
		fail(cluster.SnapshotRestore(loadConfigDefault(clusterArgs), name))
	},
}

var snapshotListCmd = &cobra.Command{
	Use:     "list [CLUSTER]",
	Aliases: []string{"ls"},
	Short:   "List snapshots",
	Args:    cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		fail(cluster.SnapshotList(loadConfigDefault(args)))
	},
}

var snapshotDeleteCmd = &cobra.Command{
	Use:     "delete [CLUSTER] NAME",
	Aliases: []string{"rm"},
	Short:   "Delete a snapshot",
	Args:    cobra.RangeArgs(1, 2),
	Run: func(cmd *cobra.Command, args []string) {
		if len(args) == 1 {
			fail(cluster.SnapshotDelete(loadConfigDefault(nil), args[0]))
			return
		}
		clusterArgs, name := snapshotArgs(args)
		fail(cluster.SnapshotDelete(loadConfigDefault(clusterArgs), name))
	},
}

func init() {
	snapshotCmd.AddCommand(snapshotSaveCmd, snapshotRestoreCmd, snapshotListCmd, snapshotDeleteCmd)
	clusterCmd.AddCommand(snapshotCmd)
}
