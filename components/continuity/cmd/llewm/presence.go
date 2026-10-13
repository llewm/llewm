package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/llewm/llewm/components/continuity/internal/app"
	"github.com/llewm/llewm/components/continuity/internal/presence"
)

func presenceCommand() *cobra.Command {
	var query presence.Query
	var output string
	var cleanup bool
	command := &cobra.Command{
		Use:   "presence",
		Short: "Read short-lived device-local agent presence",
		Long:  "Presence reports recent client sessions separately from durable tasks and coordination leases. Reads are bounded and never create, remove, or modify Git worktrees.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			format, err := outputFormat(output)
			if err != nil {
				return err
			}
			return withApp(cmd, func(application *app.App) error {
				query.Now = time.Now().UTC()
				report, err := application.Presence.List(cmd.Context(), application.ProjectID, query)
				if err != nil {
					return err
				}
				snapshot := presence.Snapshot{Report: report}
				if cleanup {
					snapshot.Worktrees = presence.CleanupCandidates(cmd.Context(), application.Repositories, report.Presence, query.Now)
				}
				if format == "json" {
					return writeJSON(cmd.OutOrStdout(), snapshot)
				}
				for _, value := range report.Presence {
					scope := []string{}
					if value.TaskID != "" {
						scope = append(scope, "task="+value.TaskID)
					}
					if value.WorktreePath != "" {
						scope = append(scope, "worktree="+value.WorktreePath)
					}
					if value.Branch != "" {
						scope = append(scope, "branch="+value.Branch)
					}
					fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\t%s\n", value.SessionID, value.Client, value.DerivedState, strings.Join(scope, " "), strings.Join(value.Overlap, ","))
				}
				for _, value := range snapshot.Worktrees {
					fmt.Fprintf(cmd.OutOrStdout(), "worktree\t%s\tcleanup_due=%t\t%s\n", value.Path, value.CleanupDue, value.Reason)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Presence: %d. %s\n", len(report.Presence), report.Disclaimer)
				return nil
			})
		},
	}
	command.Flags().StringVar(&query.SessionID, "session", "", "exact session ID")
	command.Flags().StringVar(&query.TaskID, "task", "", "exact task ID")
	command.Flags().StringVar(&query.RoadmapNode, "roadmap-node", "", "exact roadmap node ID")
	command.Flags().StringVar(&query.CheckoutPath, "checkout", "", "exact local checkout path")
	command.Flags().StringVar(&query.WorktreePath, "worktree", "", "exact local worktree path")
	command.Flags().StringVar(&query.Branch, "branch", "", "exact branch name")
	command.Flags().StringVar(&query.State, "state", "", "planning, active, yielded, completed, cancelled, or expired")
	command.Flags().BoolVar(&query.LiveOnly, "live", false, "only planning or active unexpired claims")
	command.Flags().BoolVar(&query.History, "history", false, "include transition history instead of only each claim's latest state")
	command.Flags().IntVar(&query.Limit, "limit", presence.DefaultLimit, "maximum presence records (1-200)")
	command.Flags().StringVar(&query.FocusSession, "focus-session", "", "classify overlap with this session")
	command.Flags().StringVar(&query.FocusTask, "focus-task", "", "classify overlap with this task")
	command.Flags().StringVar(&query.FocusRoadmap, "focus-roadmap-node", "", "classify overlap with this roadmap node")
	command.Flags().StringVar(&query.FocusWorktree, "focus-worktree", "", "classify overlap with this worktree")
	command.Flags().StringVar(&query.FocusBranch, "focus-branch", "", "classify overlap with this branch")
	command.Flags().BoolVar(&cleanup, "cleanup", false, "also inspect linked worktrees for clean integrated inactive cleanup candidates")
	command.Flags().StringVar(&output, "output", "text", "text or json")
	return command
}
