package main

import (
	"github.com/llewm/llewm/components/continuity/internal/app"
	"github.com/llewm/llewm/components/continuity/internal/memsync"
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

func syncRedactionReviewCommand() *cobra.Command {
	var request memsync.RedactionReviewRequest
	command := &cobra.Command{Use: "review-redaction", Short: "Review safe local interpretations of historical observations that current rules reject", Long: "Lists tracked historical observations that current redaction rules reject and a redaction projection repairs. With --apply, records this host's review of each listed source; the next sync then imports its projection while retaining the original observation unchanged. Reviews stay on this host.", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return withApp(cmd, func(application *app.App) error {
			syncer, err := projectSyncer(application)
			if err != nil {
				return err
			}
			result, err := syncer.ReviewRedactionProjections(cmd.Context(), request)
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), result)
		})
	}}
	command.Flags().StringArrayVar(&request.Sources, "source", nil, "SHA256 of a tracked canonical observation; repeatable")
	command.Flags().BoolVar(&request.All, "all", false, "select every tracked observation that a redaction projection repairs")
	command.Flags().BoolVar(&request.Apply, "apply", false, "record the reviews; without it nothing is written")
	command.Flags().StringVar(&request.Reviewer, "reviewer", "", "who reviewed the projections (required with --apply)")
	command.Flags().StringVar(&request.Reason, "reason", "", "why the projections are accepted (required with --apply)")
	return command
}
