package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"k3c/cluster"
)

// exportMode maps the export flags to a frozen export tier (default fat).
// --slim and --thin are mutually exclusive (enforced on the command).
func exportMode() cluster.FrozenExportMode {
	switch {
	case snapshotExportThin:
		return cluster.FrozenThin
	case snapshotExportSlim:
		return cluster.FrozenSlim
	default:
		return cluster.FrozenFat
	}
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

var (
	snapshotSaveCold    bool
	snapshotSaveFrozen  bool
	snapshotSaveReplace bool
	snapshotRestoreCold bool
	snapshotExportOut   string
	snapshotExportThin  bool
	snapshotExportSlim  bool
)

// saveMode resolves the tier flags into a SnapshotMode, rejecting the
// mutually-exclusive combination.
func saveMode() (cluster.SnapshotMode, error) {
	if snapshotSaveCold && snapshotSaveFrozen {
		return "", fmt.Errorf("--cold and --frozen are mutually exclusive")
	}
	switch {
	case snapshotSaveFrozen:
		return cluster.ModeFrozen, nil
	case snapshotSaveCold:
		return cluster.ModeCold, nil
	default:
		return cluster.ModeWarm, nil
	}
}

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
		Long: "Save a snapshot of cluster state. Three tiers trade size for restore time:\n" +
			"  warm   (default) full rootfs + VM RAM image; resumes in place, instant restore\n" +
			"  --cold full rootfs clone; boots fresh, restores in seconds\n" +
			"  --frozen logical extract (datastore + all PVC data + image manifest); the\n" +
			"         smallest tier — drops the image store and rehydrates it from the\n" +
			"         pull-cache on restore, taking minutes to thaw",
		Args: cobra.MaximumNArgs(2),
		Run: func(cmd *cobra.Command, args []string) {
			mode, err := saveMode()
			fail(err)
			// with a single argument it is the snapshot name
			if len(args) == 1 {
				fail(cluster.SnapshotSave(loadConfigDefault(nil), args[0], mode, snapshotSaveReplace))
				return
			}
			clusterArgs, name := snapshotArgs(args)
			fail(cluster.SnapshotSave(loadConfigDefault(clusterArgs), name, mode, snapshotSaveReplace))
		},
	}
	saveCmd.Flags().BoolVar(&snapshotSaveCold, "cold", false,
		"stop the cluster for a clean-shutdown snapshot instead of suspending it")
	saveCmd.Flags().BoolVar(&snapshotSaveFrozen, "frozen", false,
		"smallest tier: a logical extract (datastore + all PVC data + image manifest); minutes to thaw")
	saveCmd.Flags().BoolVar(&snapshotSaveReplace, "replace", false,
		"recreate a same-named snapshot: delete the existing one, then save in its place")

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

	renameCmd := &cobra.Command{
		Use:     "rename [CLUSTER] OLD NEW",
		Aliases: []string{"mv"},
		Short:   "Rename a snapshot",
		Args:    cobra.RangeArgs(2, 3),
		Run: func(cmd *cobra.Command, args []string) {
			// with two args they are OLD NEW on the active cluster; with three
			// the first is the cluster
			if len(args) == 2 {
				fail(cluster.SnapshotRename(loadConfigDefault(nil), args[0], args[1]))
				return
			}
			fail(cluster.SnapshotRename(loadConfigDefault(args[:1]), args[1], args[2]))
		},
	}

	exportCmd := &cobra.Command{
		Use:   "export [CLUSTER] NAME",
		Short: "Export a snapshot to a portable archive (warm/cold restore cold; frozen exports fat by default)",
		Args:  cobra.RangeArgs(1, 2),
		Run: func(cmd *cobra.Command, args []string) {
			if len(args) == 1 {
				fail(cluster.SnapshotExport(loadConfigDefault(nil), args[0], snapshotExportOut, exportMode()))
				return
			}
			clusterArgs, name := snapshotArgs(args)
			fail(cluster.SnapshotExport(loadConfigDefault(clusterArgs), name, snapshotExportOut, exportMode()))
		},
	}
	exportCmd.Flags().StringVarP(&snapshotExportOut, "output", "o", "",
		"output file (default <cluster>-<name>.k3csnap)")
	exportCmd.Flags().BoolVar(&snapshotExportSlim, "slim", false,
		"frozen only: bundle local-only images; re-pull remote-registry images on import")
	exportCmd.Flags().BoolVar(&snapshotExportThin, "thin", false,
		"frozen only: bundle no images at all (only safe when the cluster has no local-only images)")
	exportCmd.MarkFlagsMutuallyExclusive("slim", "thin")

	var importCluster string
	importCmd := &cobra.Command{
		Use:   "import FILE [NAME]",
		Short: "Import an exported snapshot archive as a stored snapshot of an existing cluster",
		Long: "Import an exported archive into an existing cluster as a stored snapshot.\n" +
			"FILE is the archive; the optional NAME is the snapshot name to store it\n" +
			"under (defaulting to the archive's name). The target cluster is the\n" +
			"active one (or the only one) unless --cluster selects another.\n\n" +
			"To create a brand-new cluster from an archive instead, use\n" +
			"`k3c cluster import-run` — there the second argument is the cluster name.",
		Args: cobra.RangeArgs(1, 2),
		Run: func(cmd *cobra.Command, args []string) {
			name := ""
			if len(args) > 1 {
				name = args[1]
			}
			var clusterArgs []string
			if importCluster != "" {
				clusterArgs = []string{importCluster}
			}
			fail(cluster.SnapshotImport(loadConfigDefault(clusterArgs), args[0], name))
		},
	}
	importCmd.Flags().StringVarP(&importCluster, "cluster", "C", "",
		"target cluster (default: the active cluster, or the only one)")

	snapshotCmd.AddCommand(saveCmd, restoreCmd, listCmd, deleteCmd, renameCmd, exportCmd, importCmd)
	return snapshotCmd
}

func init() {
	clusterCmd.AddCommand(newSnapshotCmd())
	rootCmd.AddCommand(newSnapshotCmd())
}
