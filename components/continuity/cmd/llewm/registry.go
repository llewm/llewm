package main

import (
	"fmt"
	"github.com/llewm/llewm/components/continuity/internal/config"
	"github.com/llewm/llewm/components/continuity/internal/project"
	"github.com/spf13/cobra"
)

func registryCommand() *cobra.Command {
	parent := &cobra.Command{Use: "registry", Short: "Inspect rebuildable machine-local checkout routes"}
	var output, review string
	var apply bool
	list := &cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		format, err := outputFormat(output)
		if err != nil {
			return err
		}
		global, err := config.GlobalDir()
		if err != nil {
			return err
		}
		entries, err := project.RegistryAll(cmd.Context(), global)
		if err != nil {
			return err
		}
		if format == "json" {
			return writeJSON(cmd.OutOrStdout(), entries)
		}
		for _, e := range entries {
			fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\n", e.ProjectID, e.Availability, e.Root, e.Store)
		}
		return nil
	}}
	list.Flags().StringVar(&output, "output", "text", "text or json")
	prune := &cobra.Command{Use: "prune", Short: "Preview stale derived routes; apply only an exact reviewed inventory", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if apply && review == "" {
			return fmt.Errorf("preview first, then pass --apply --review-sha256 HASH from that inventory")
		}
		if !apply && review != "" {
			return fmt.Errorf("--review-sha256 requires --apply")
		}
		global, err := config.GlobalDir()
		if err != nil {
			return err
		}
		plan, err := project.PruneRegistry(cmd.Context(), global, review)
		if err != nil {
			return err
		}
		return writeJSON(cmd.OutOrStdout(), plan)
	}}
	prune.Flags().BoolVar(&apply, "apply", false, "remove reviewed derived routes only; never project data")
	prune.Flags().StringVar(&review, "review-sha256", "", "exact preview inventory hash")
	parent.AddCommand(list, prune)
	return parent
}
