package cmd

import (
	"github.com/spf13/cobra"

	"k3c/cluster"
)

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

var (
	snapshotSaveCold    bool
	snapshotRestoreCold bool
)

// newSnapshotCmd builds the snapshot command tree. It is registered both
// top-level (k3c snapshot ...) and under cluster (k3c cluster snapshot ...).
func newSnapshotCmd() *cobra.Command {
	snapshotCmd := &cobra.Command{
		Use:   "snapshot",
		Short: "Snapshot and restore cluster state (instant, APFS copy-on-write)",
	}

	saveCmd := &cobra.Command{
		Use:     "save [CLUSTER] [NAME]",
		Aliases: []string{"create"},
		Short:   "Save a snapshot (default name: timestamp); warm by default, restoring to a running cluster",
		Args:    cobra.MaximumNArgs(2),
		Run: func(cmd *cobra.Command, args []string) {
			// with a single argument it is the snapshot name
			if len(args) == 1 {
				fail(cluster.SnapshotSave(loadConfigDefault(nil), args[0], snapshotSaveCold))
				return
			}
			clusterArgs, name := snapshotArgs(args)
			fail(cluster.SnapshotSave(loadConfigDefault(clusterArgs), name, snapshotSaveCold))
		},
	}
	saveCmd.Flags().BoolVar(&snapshotSaveCold, "cold", false,
		"stop the cluster for a clean-shutdown snapshot instead of suspending it")

	restoreCmd := &cobra.Command{
		Use:   "restore [CLUSTER] NAME",
		Short: "Restore a snapshot and start the cluster",
		Args:  cobra.RangeArgs(1, 2),
		Run: func(cmd *cobra.Command, args []string) {
			// with a single argument it is the snapshot name
			if len(args) == 1 {
				fail(cluster.SnapshotRestore(loadConfigDefault(nil), args[0], snapshotRestoreCold))
				return
			}
			clusterArgs, name := snapshotArgs(args)
			fail(cluster.SnapshotRestore(loadConfigDefault(clusterArgs), name, snapshotRestoreCold))
		},
	}
	restoreCmd.Flags().BoolVar(&snapshotRestoreCold, "cold", false,
		"boot fresh from the snapshot's disk, ignoring its saved machine state")

	listCmd := &cobra.Command{
		Use:     "list [CLUSTER]",
		Aliases: []string{"ls"},
		Short:   "List snapshots",
		Args:    cobra.MaximumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			fail(cluster.SnapshotList(loadConfigDefault(args)))
		},
	}

	deleteCmd := &cobra.Command{
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

	snapshotCmd.AddCommand(saveCmd, restoreCmd, listCmd, deleteCmd)
	return snapshotCmd
}

func init() {
	clusterCmd.AddCommand(newSnapshotCmd())
	rootCmd.AddCommand(newSnapshotCmd())
}
