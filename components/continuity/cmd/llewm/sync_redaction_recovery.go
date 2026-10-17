package main

import (
	"github.com/llewm/llewm/components/continuity/internal/app"
	"github.com/spf13/cobra"
)

func syncRedactionRecoveryCommand() *cobra.Command {
	var original, derived, root, id, creator, reason, expected string
	var apply bool
	command := &cobra.Command{Use: "repair-redaction", Short: "Preview or append an exact historical root/ID redaction correction", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return withApp(cmd, func(application *app.App) error {
			syncer, err := projectSyncer(application)
			if err != nil {
				return err
			}
			result, err := syncer.RepairRedaction(cmd.Context(), original, derived, root, id, creator, reason, expected, apply)
			if writeErr := writeJSON(cmd.OutOrStdout(), result); writeErr != nil {
				return writeErr
			}
			return err
		})
	}}
	command.Flags().StringVar(&original, "original", "", "SHA256 of the original tracked canonical envelope")
	command.Flags().StringVar(&derived, "derived", "", "SHA256 of the additionally redacted tracked envelope")
	command.Flags().StringVar(&root, "redacted-root", "", "exact project root substituted on the re-exporting host")
	command.Flags().StringVar(&id, "redacted-project-id", "", "exact project ID substituted on that host")
	command.Flags().StringVar(&creator, "creator", "", "identity claiming the recovery")
	command.Flags().StringVar(&reason, "reason", "", "bounded explanation of the historical re-export")
	command.Flags().BoolVar(&apply, "apply", false, "append the previewed correction locally; no sync or operational rewrite")
	command.Flags().StringVar(&expected, "expect-recovery", "", "exact recovery ID from the preview, required with --apply")
	return command
}
