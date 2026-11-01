package main

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/llewm/llewm/components/continuity/internal/historystorage"
)

func syncStorageCommand() *cobra.Command {
	parent := &cobra.Command{
		Use: "storage", Short: "Plan, migrate, and fully audit local history storage",
		Long: "Inspect and explicitly migrate private history archives and managed Git clones. Schema migration affects the configured database; archive migration targets the selected project; Git migration preserves every ref in its configured clone. Retain the saved plan for interrupted recovery. These commands do not stop writers or contact remotes.",
		Args: cobra.NoArgs,
	}
	var save string
	plan := &cobra.Command{
		Use: "plan", Short: "Inspect sources without writes; optionally save a new private recovery plan", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := projectRoot(cmd)
			if err != nil {
				return err
			}
			p, err := historystorage.NewPlan(cmd.Context(), root)
			if err != nil {
				return err
			}
			if save != "" {
				if err := historystorage.SavePlan(save, p); err != nil {
					return err
				}
			}
			return writeJSON(cmd.OutOrStdout(), p.Seal())
		},
	}
	plan.Flags().StringVar(&save, "save", "", "publish this plan to a new private local file; never overwrite")
	parent.AddCommand(plan)
	addOperation := func(name, short string, run func(context.Context, string, historystorage.Plan, bool) (historystorage.Result, error)) {
		var file string
		var writersStopped bool
		cmd := &cobra.Command{
			Use: name, Short: short, Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				root, err := projectRoot(cmd)
				if err != nil {
					return err
				}
				p, err := historystorage.LoadPlan(file)
				if err != nil {
					return err
				}
				result, runErr := run(cmd.Context(), root, p, writersStopped)
				if err := writeJSON(cmd.OutOrStdout(), result); err != nil {
					return err
				}
				return runErr
			},
		}
		cmd.Flags().StringVar(&file, "plan", "", "exact saved local plan used for this cutover and recovery")
		cmd.Flags().BoolVar(&writersStopped, "writers-stopped", false, "assert all writers using this database/archive/clone have been stopped")
		_ = cmd.MarkFlagRequired("plan")
		parent.AddCommand(cmd)
	}
	addOperation("prepare", "Verify backups, explicitly migrate schema, and stage storage while retaining sources", historystorage.Prepare)
	addOperation("complete", "Resume the prepared cutover, retain originals, and verify active storage", historystorage.Complete)
	parent.AddCommand(&cobra.Command{
		Use: "audit", Short: "Fully verify local history bytes without changing history or contacting remotes", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := projectRoot(cmd)
			if err != nil {
				return err
			}
			result, auditErr := historystorage.Audit(cmd.Context(), root)
			if err := writeJSON(cmd.OutOrStdout(), result); err != nil {
				return err
			}
			return auditErr
		},
	})
	return parent
}
