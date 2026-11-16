package main

import (
	"github.com/llewm/llewm/components/continuity/internal/app"
	"github.com/spf13/cobra"
)

func syncRedactionProjectionCommand() *cobra.Command {
	var source string
	command := &cobra.Command{Use: "preview-redaction", Short: "Preview a safe local interpretation of one historical observation", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return withApp(cmd, func(application *app.App) error {
			syncer, err := projectSyncer(application)
			if err != nil {
				return err
			}
			result, err := syncer.PreviewRedactionProjection(cmd.Context(), source)
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), result)
		})
	}}
	command.Flags().StringVar(&source, "source", "", "SHA256 of the tracked canonical observation; original content is never printed")
	return command
}
