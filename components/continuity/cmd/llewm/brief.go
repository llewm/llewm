package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/llewm/llewm/components/continuity/internal/app"
	"github.com/llewm/llewm/components/continuity/internal/brief"
	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
)

func briefCommand() *cobra.Command {
	var output string
	command := &cobra.Command{Use: "brief [REF]", Short: "Show the record-backed brief for a plan item (4e, item:ID, or PLAN/ITEM), or the items the current next action (else pending actions) names", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		format, err := outputFormat(output)
		if err != nil {
			return err
		}
		return withApp(cmd, func(application *app.App) error {
			if len(args) == 1 {
				if taskID, ok := strings.CutPrefix(args[0], "task:"); ok {
					value, err := brief.BuildTask(cmd.Context(), application.DB, application.ProjectID, taskID)
					if err != nil {
						return err
					}
					if format == "json" {
						return writeJSON(cmd.OutOrStdout(), value)
					}
					_, err = fmt.Fprint(cmd.OutOrStdout(), brief.TaskMarkdown(value))
					return err
				}
				value, err := brief.Build(cmd.Context(), application.DB, application.ProjectID, args[0])
				if err != nil {
					return err
				}
				if format == "json" {
					return writeJSON(cmd.OutOrStdout(), value)
				}
				_, err = fmt.Fprint(cmd.OutOrStdout(), brief.Markdown(value))
				return err
			}
			latest, err := application.Checkpoints.LatestSelected(cmd.Context(), application.ProjectID, checkpoint.Selection{})
			if err != nil {
				return err
			}
			next := ""
			if latest != nil {
				next = latest.NextAction
			}
			pending, err := application.Checkpoints.PendingTexts(cmd.Context(), application.ProjectID)
			if err != nil {
				return err
			}
			items, err := brief.MentionedOrPending(cmd.Context(), application.DB, application.ProjectID, next, pending)
			if err != nil {
				return err
			}
			if format == "json" {
				if items == nil {
					items = []brief.Candidate{}
				}
				return writeJSON(cmd.OutOrStdout(), items)
			}
			if len(items) == 0 {
				_, err = fmt.Fprintln(cmd.OutOrStdout(), "Neither the current next action nor any pending action names an open plan item. Pass a reference: llewm brief 4e")
				return err
			}
			for _, item := range items {
				if _, err := fmt.Fprintln(cmd.OutOrStdout(), brief.Line(item)); err != nil {
					return err
				}
			}
			return nil
		})
	}}
	command.Short = "Show a compact exact task brief (task:ID), plan item brief, or named next-action items"
	command.Flags().StringVar(&output, "output", "text", "text or json")
	return command
}
