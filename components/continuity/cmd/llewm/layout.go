package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/llewm/llewm/components/continuity/internal/layout"
	"github.com/llewm/llewm/components/continuity/internal/project"
	"github.com/spf13/cobra"
)

func addLayoutCommands(parent *cobra.Command) {
	var apply, stopped bool
	var backup string
	migrate := &cobra.Command{Use: "migrate-layout", Short: "Preview the explicit .continuity to .llewm/continuity move; --apply requires a verified backup and stopped writers", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		root, err := layoutProjectRoot(cmd)
		if err != nil {
			return err
		}
		if !apply {
			plan, err := layout.Preview(cmd.Context(), root)
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), plan)
		}
		receipt, err := layout.Migrate(cmd.Context(), root, backup, stopped)
		if err != nil {
			return fmt.Errorf("layout migration: %w; preserve backup and inspect any layout.lock before retrying", err)
		}
		return writeJSON(cmd.OutOrStdout(), receipt)
	}}
	migrate.Flags().BoolVar(&apply, "apply", false, "apply the reviewed layout move (default is read-only preview)")
	migrate.Flags().BoolVar(&stopped, "writers-stopped", false, "attest all CLI, hook, MCP and sync writers stopped/drained on affected machines")
	migrate.Flags().StringVar(&backup, "backup", "", "new backup directory on the same home-disk filesystem outside both state directories")
	parent.AddCommand(migrate)
	var restoreBackup string
	var restoreStopped bool
	restore := &cobra.Command{Use: "restore-layout", Short: "Restore a migration backup only while new state still matches its receipt; also recover an interrupted move", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		root, err := layoutProjectRoot(cmd)
		if err != nil {
			return err
		}
		if err = layout.Restore(root, restoreBackup, restoreStopped); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Original .continuity layout restored. Snapshot and receipt retained. Use the matching previous executable and client packages; new writes require reconciliation rather than blind rollback.")
		return nil
	}}
	restore.Flags().BoolVar(&restoreStopped, "writers-stopped", false, "attest all affected writers stopped/drained")
	restore.Flags().StringVar(&restoreBackup, "backup", "", "backup directory returned by migrate-layout")
	parent.AddCommand(restore)
}

// Layout maintenance must not open an app or resolve through a legacy workspace
// manifest that normal reads correctly refuse. An explicit root is exact.
func layoutProjectRoot(cmd *cobra.Command) (string, error) {
	root, err := cmd.Root().PersistentFlags().GetString("project-root")
	if err != nil {
		return "", err
	}
	if root != "" {
		return filepath.Abs(root)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	if _, err = os.Stat(filepath.Join(cwd, ".continuity")); err == nil {
		return cwd, nil
	}
	if _, err = os.Stat(filepath.Join(cwd, ".llewm", "project.toml")); err == nil {
		return cwd, nil
	}
	return project.FindRoot(cwd)
}
