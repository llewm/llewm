package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/app"
	"github.com/llewm/llewm/components/continuity/internal/attribution"
	"github.com/llewm/llewm/components/continuity/internal/audit"
	"github.com/llewm/llewm/components/continuity/internal/bootstrap"
	"github.com/llewm/llewm/components/continuity/internal/buildinfo"
	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
	"github.com/llewm/llewm/components/continuity/internal/clientperm"
	"github.com/llewm/llewm/components/continuity/internal/clients"
	"github.com/llewm/llewm/components/continuity/internal/clients/claude"
	"github.com/llewm/llewm/components/continuity/internal/clients/codex"
	"github.com/llewm/llewm/components/continuity/internal/config"
	"github.com/llewm/llewm/components/continuity/internal/coverage"
	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/lease"
	"github.com/llewm/llewm/components/continuity/internal/mcpserver"
	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/procedures"
	"github.com/llewm/llewm/components/continuity/internal/project"
	"github.com/llewm/llewm/components/continuity/internal/readiness"
	"github.com/llewm/llewm/components/continuity/internal/researchcapture"
	"github.com/llewm/llewm/components/continuity/internal/roadmap"
	"github.com/llewm/llewm/components/continuity/internal/trackplan"
	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
	"github.com/llewm/llewm/components/continuity/internal/workstate"
)

func main() {
	root := newRootCommand(programName())
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, root.Name()+":", err)
		os.Exit(1)
	}
}

// programName returns the name the binary was invoked as, so that a renamed
// copy or symlink generates shell completion
// scripts that register under the name the user actually types.
func programName() string {
	name := filepath.Base(os.Args[0])
	if ext := filepath.Ext(name); ext == ".exe" {
		name = strings.TrimSuffix(name, ext)
	}
	if name == "" || name == "." || name == string(filepath.Separator) {
		return "llewm"
	}
	return name
}

func newRootCommand(name string) *cobra.Command {
	root := &cobra.Command{
		Use:           name,
		Short:         "Local continuity and activity controller for coding agents",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().String("project-root", "", "explicit Git project root or path within it")
	root.AddCommand(pmCommand())
	root.AddCommand(versionCommand(), initCommand(), databaseCommand(), projectCommand(), workspaceCommand(), syncCommand(), globalCommand(), doctorCommand(), clientCommand(), briefCommand(), workCommand(), presenceCommand(), tuiCommand(), consoleCommand(), hookCommand(), coverageCommand(), usageCommand(), statsCommand(), checkpointCommand(), procedureCommand(), resourceCommand(), pinCommand(), leaseCommand(), standingGrantCommand(), forkCommand(), contractCommand(), bootstrapCommand(), activityCommand(), roadmapCommand(), releaseCommand(), trackPlanCommand(), decisionCommand(), historyCommand(), topicCommand(), narrativeCommand(), itemCommand(), memoryCommand(), mcpCommand())
	root.AddCommand(modeCommand())
	root.AddCommand(installCommand())
	return root
}

func workCommand() *cobra.Command {
	var limit int
	var output string
	var summary bool
	var selection checkpoint.Selection
	parent := &cobra.Command{Use: "work", Short: "Show the current task, its next action, and routing; subcommands list and check work", Args: cobra.NoArgs}
	parent.RunE = func(cmd *cobra.Command, _ []string) error {
		format, err := outputFormat(output)
		if err != nil {
			return err
		}
		return withApp(cmd, func(application *app.App) error {
			if summary {
				return writeWorkSummary(cmd, application, limit, format)
			}
			pulse, err := application.Pulse.NextSelected(cmd.Context(), application.ProjectID, selection)
			if err != nil {
				return err
			}
			if format == "json" {
				return writeJSON(cmd.OutOrStdout(), pulse)
			}
			latest, err := application.Checkpoints.LatestSelected(cmd.Context(), application.ProjectID, selection)
			if err != nil {
				return err
			}
			if latest == nil {
				fmt.Fprintln(cmd.OutOrStdout(), "No checkpoint recorded.")
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "Task: %s\nStatus: %s\nSummary: %s\nNext: %s\n", latest.Task, latest.Status, latest.Summary, latest.NextAction)
				if lags, err := application.CodeBehind(cmd.Context(), latest.ID); err == nil {
					if line := app.CodeBehindLine(latest.ID, lags); line != "" {
						fmt.Fprintln(cmd.OutOrStdout(), "Code: "+line)
					}
				}
			}
			if value, err := projectSyncStatus(cmd, application); err == nil {
				if line := value.Line(time.Now().UTC()); line != "" {
					fmt.Fprintln(cmd.OutOrStdout(), line)
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Routing: %s (%s)\n", pulse.RoutingDecision, pulse.RoutingRule)
			if pulse.SelectionRequired {
				for _, candidate := range pulse.RoutingCandidates {
					fmt.Fprintf(cmd.OutOrStdout(), "Candidate: node=%s task=%s checkpoint=%s title=%s\n", candidate.RoadmapNodeID, candidate.TaskID, candidate.CheckpointID, candidate.Title)
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Roadmap freshness: %s\nReview due: %t\nRoadmap nodes: %d\n", pulse.RoadmapStatus.Freshness, pulse.RoadmapStatus.ReviewDue, len(pulse.RoadmapNodes))
			_, err = fmt.Fprintln(cmd.OutOrStdout(), pulse.Disclaimer)
			return err
		})
	}
	parent.Flags().BoolVar(&summary, "summary", false, "show the roadmap, checkpoint, plan, and finding summary instead")
	parent.Flags().IntVar(&limit, "limit", workstate.DefaultLimit, "with --summary, maximum current-work and ready-item records (1-100)")
	parent.Flags().StringVar(&output, "output", "text", "text or json")
	addSelectionFlags(parent, &selection)

	var scope string
	var checkLimit int
	var checkOutput string
	check := &cobra.Command{Use: "check", Short: "Report deterministic saved-state comparisons without repairing them", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return withApp(cmd, func(application *app.App) error {
			result, err := application.Work.Check(cmd.Context(), application.ProjectID, scope, checkLimit)
			if err != nil {
				return err
			}
			format, err := outputFormat(checkOutput)
			if err != nil {
				return err
			}
			if format == "json" {
				return writeJSON(cmd.OutOrStdout(), result)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "State: %s\nScope: %s\nFindings: %d\nTruncated: %t\n", result.State, result.Scope, result.Total, result.Truncated)
			for _, finding := range result.Findings {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\tnode=%s\ttask=%s\t%s\n", finding.Severity, finding.Code, valueOrUnknown(finding.RoadmapNode), valueOrUnknown(finding.TaskID), finding.Summary)
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), result.Disclaimer)
			return err
		})
	}}
	check.Flags().StringVar(&scope, "scope", "current", "current or all")
	check.Flags().IntVar(&checkLimit, "limit", workstate.DefaultLimit, "maximum inventory records to inspect (1-100)")
	check.Flags().StringVar(&checkOutput, "output", "text", "text or json")

	var listView string
	var listLimit int
	var listOutput string
	list := &cobra.Command{Use: "list", Short: "List current, active, next, blocked, stale, all, or everything (adds finished tasks without a roadmap node) work", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return withApp(cmd, func(application *app.App) error {
			result, err := application.Work.List(cmd.Context(), application.ProjectID, listView, listLimit)
			if err != nil {
				return err
			}
			format, err := outputFormat(listOutput)
			if err != nil {
				return err
			}
			if format == "json" {
				return writeJSON(cmd.OutOrStdout(), result)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "View: %s\nTracks: %d", result.View, result.InventoryTotal)
			if result.InventoryOmitted > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), " (%d omitted)", result.InventoryOmitted)
			}
			fmt.Fprintln(cmd.OutOrStdout())
			for _, item := range result.Items {
				writeWorkItemLine(cmd, item)
			}
			if result.View == "next" {
				fmt.Fprintf(cmd.OutOrStdout(), "Ready plan items: %d", result.ReadyTotal)
				if result.ReadyOmitted > 0 {
					fmt.Fprintf(cmd.OutOrStdout(), " (%d omitted)", result.ReadyOmitted)
				}
				fmt.Fprintln(cmd.OutOrStdout())
				for _, ready := range result.ReadyItems {
					fmt.Fprintf(cmd.OutOrStdout(), "plan:%s/%s\t%s\tnode=%s\ttask=%s\n", ready.PlanID, ready.Item.ID, ready.Item.Title, valueOrUnknown(ready.RoadmapNodeID), valueOrUnknown(ready.TaskID))
				}
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), result.Disclaimer)
			return err
		})
	}}
	list.Flags().StringVar(&listView, "view", "current", "current, active, next, blocked, stale or all")
	list.Flags().IntVar(&listLimit, "limit", workstate.DefaultLimit, "maximum tracks and ready items returned (1-100)")
	list.Flags().StringVar(&listOutput, "output", "text", "text or json")

	var showOutput string
	show := &cobra.Command{Use: "show <node-or-task-id>", Short: "Show one exact joined work record", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return withApp(cmd, func(application *app.App) error {
			result, err := application.Work.Show(cmd.Context(), application.ProjectID, args[0])
			if err != nil {
				return err
			}
			format, err := outputFormat(showOutput)
			if err != nil {
				return err
			}
			if format == "json" {
				return writeJSON(cmd.OutOrStdout(), result)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Identifier: %s\nMatched by: %s\n", result.Identifier, result.MatchedBy)
			writeWorkItemDetail(cmd, result.Item)
			_, err = fmt.Fprintln(cmd.OutOrStdout(), result.Disclaimer)
			return err
		})
	}}
	show.Flags().StringVar(&showOutput, "output", "text", "text or json")

	var freshnessSelection checkpoint.Selection
	var activityLimit int
	var reminderMaxAgeSeconds int64
	var reminderMaxEvents int
	var freshnessOutput string
	freshness := &cobra.Command{
		Use:   "freshness",
		Short: "Report checkpoint age and bounded observed activity since it",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withApp(cmd, func(application *app.App) error {
				result, err := application.Checkpoints.Freshness(cmd.Context(), application.ProjectID, freshnessSelection, activityLimit, checkpoint.ReminderPolicy{MaxAgeSeconds: reminderMaxAgeSeconds, MaxEvents: reminderMaxEvents})
				if err != nil {
					return err
				}
				format, err := outputFormat(freshnessOutput)
				if err != nil {
					return err
				}
				if format == "json" {
					return writeJSON(cmd.OutOrStdout(), result)
				}
				if result.Checkpoint == nil {
					fmt.Fprintln(cmd.OutOrStdout(), "No checkpoint recorded.")
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "Checkpoint: %s\nFreshness: %s\nAge seconds: %d\nEvents since checkpoint: %d\n", result.Checkpoint.ID, result.Freshness, *result.AgeSeconds, result.EventsSince)
				}
				if result.ReminderEvaluated {
					fmt.Fprintf(cmd.OutOrStdout(), "Checkpoint recommended: %t\n", result.CheckpointRecommended)
					for _, reason := range result.ReminderReasons {
						fmt.Fprintf(cmd.OutOrStdout(), "Reminder reason: %s\n", reason)
					}
				}
				_, err = fmt.Fprintln(cmd.OutOrStdout(), result.Disclaimer)
				return err
			})
		},
	}
	addSelectionFlags(freshness, &freshnessSelection)
	freshness.Flags().IntVar(&activityLimit, "limit", checkpoint.DefaultActivityLimit, "maximum recent activity observations")
	freshness.Flags().Int64Var(&reminderMaxAgeSeconds, "remind-after-seconds", 0, "recommend a checkpoint at this age (disabled when zero)")
	freshness.Flags().IntVar(&reminderMaxEvents, "remind-after-events", 0, "recommend a checkpoint after this many observed events (disabled when zero)")
	freshness.Flags().StringVar(&freshnessOutput, "output", "text", "text or json")

	var inventoryOptions roadmap.TrackInventoryOptions
	var inventoryOutput string
	inventoryCommand := &cobra.Command{Use: "inventory", Short: "Read the joined roadmap and task inventory with structural anomalies", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return withApp(cmd, func(application *app.App) error {
			inventory, err := application.Roadmap.TrackInventory(cmd.Context(), application.ProjectID, inventoryOptions)
			if err != nil {
				return err
			}
			format, err := outputFormat(inventoryOutput)
			if err != nil {
				return err
			}
			if format == "json" {
				return writeJSON(cmd.OutOrStdout(), inventory)
			}
			writeTrackInventoryText(cmd, inventory)
			return nil
		})
	}}
	inventoryCommand.Flags().StringVar(&inventoryOptions.Filter, "filter", "all", "all (finished tasks without a roadmap node hidden), everything, current_focus, active, resumable, blocked, anomalous, or stale")
	inventoryCommand.Flags().StringVar(&inventoryOptions.RoadmapNodeID, "node", "", "exact roadmap node identifier")
	inventoryCommand.Flags().IntVar(&inventoryOptions.Offset, "offset", 0, "zero-based result offset")
	inventoryCommand.Flags().IntVar(&inventoryOptions.Limit, "limit", roadmap.DefaultTrackInventoryLimit, "maximum tracks returned")
	inventoryCommand.Flags().IntVar(&inventoryOptions.RefLimit, "ref-limit", roadmap.DefaultTrackInventoryRefLimit, "maximum action and work item references per track")
	inventoryCommand.Flags().StringVar(&inventoryOutput, "output", "json", "text or json")

	parent.AddCommand(check, list, show, freshness, inventoryCommand)
	return parent
}

// writeWorkSummary writes the current-work summary over roadmap, checkpoint,
// plan, and freshness records.
func writeWorkSummary(cmd *cobra.Command, application *app.App, limit int, format string) error {
	result, err := application.Work.Summary(cmd.Context(), application.ProjectID, limit)
	if err != nil {
		return err
	}
	if format == "json" {
		return writeJSON(cmd.OutOrStdout(), result)
	}
	if result.Checkpoint == nil {
		fmt.Fprintln(cmd.OutOrStdout(), "Current task: none recorded")
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "Current task: %s [%s]\nTask ID: %s\nCheckpoint: %s\n", result.Checkpoint.Task, result.Checkpoint.Status, valueOrUnknown(result.Checkpoint.TaskID), result.Checkpoint.ID)
		fmt.Fprintf(cmd.OutOrStdout(), "Next: %s\n", valueOrUnknown(result.RecommendedNext))
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Roadmap: %s; review due: %t; routing: %s\n", result.RoadmapFreshness, result.RoadmapReviewDue, result.RoutingDecision)
	fmt.Fprintf(cmd.OutOrStdout(), "Work: focus=%d active=%d blocked=%d resumable=%d ready=%d stale=%d\n", result.Counts.CurrentFocus, result.Counts.Active, result.Counts.Blocked, result.Counts.Resumable, result.Counts.Ready, result.Counts.Anomalous)
	for _, ready := range result.ReadyItems {
		fmt.Fprintf(cmd.OutOrStdout(), "Ready: [%s/%s] %s\n", ready.PlanID, ready.Item.ID, ready.Item.Title)
	}
	for _, finding := range result.Findings {
		fmt.Fprintf(cmd.OutOrStdout(), "Finding: %s node=%s task=%s — %s\n", finding.Code, valueOrUnknown(finding.RoadmapNode), valueOrUnknown(finding.TaskID), finding.Summary)
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), result.Disclaimer)
	return err
}

func workItemIdentity(item roadmap.TrackInventoryItem) (string, string, string) {
	nodeID, title, status := "-", "-", "-"
	if item.Node != nil {
		nodeID, title, status = item.Node.ID, item.Node.Title, item.Node.Status
	}
	return nodeID, title, status
}

func writeWorkItemLine(cmd *cobra.Command, item roadmap.TrackInventoryItem) {
	nodeID, title, status := workItemIdentity(item)
	reference := "node:" + nodeID
	if item.Node == nil {
		reference = "task:" + valueOrUnknown(item.SelectedTaskID)
	}
	stale := len(item.Anomalies) > 0 || item.LatestCheckpoint != nil && item.LatestCheckpoint.Status == "completed" && item.UnresolvedActionCount > 0
	fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\tstatus=%s\ttask=%s\tactive=%t blocked=%t resumable=%t stale=%t\n", reference, title, status, valueOrUnknown(item.SelectedTaskID), item.Active, item.Blocked, item.Resumable, stale)
}

func writeWorkItemDetail(cmd *cobra.Command, item roadmap.TrackInventoryItem) {
	nodeID, title, status := workItemIdentity(item)
	fmt.Fprintf(cmd.OutOrStdout(), "Roadmap node: %s rev=%d [%s]\nTitle: %s\nTask: %s\nBinding: %s\n", nodeID, item.NodeRevision, status, title, valueOrUnknown(item.SelectedTaskID), item.BindingState)
	if item.LatestCheckpoint != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Checkpoint: %s [%s]\nNext: %s\n", item.LatestCheckpoint.ID, item.LatestCheckpoint.Status, valueOrUnknown(item.LatestCheckpoint.NextAction))
	} else {
		fmt.Fprintln(cmd.OutOrStdout(), "Checkpoint: none recorded")
	}
	fmt.Fprintf(cmd.OutOrStdout(), "State: current=%t active=%t blocked=%t resumable=%t\nActions: %d unresolved\nWork items: %d pending, %d accepted\n", item.CurrentFocus, item.Active, item.Blocked, item.Resumable, item.UnresolvedActionCount, item.PendingWorkItems, item.AcceptedWorkItems)
	if len(item.ResumableReasons) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Resumable reasons: %s\n", strings.Join(item.ResumableReasons, ", "))
	}
	if len(item.Anomalies) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Findings: %s\n", strings.Join(item.Anomalies, ", "))
	}
}

func outputFormat(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = "text"
	}
	if value != "text" && value != "json" {
		return "", fmt.Errorf("output must be text or json")
	}
	return value, nil
}

func procedureCommand() *cobra.Command {
	parent := &cobra.Command{Use: "procedure", Short: "Store and inspect inert reviewed procedure definitions"}
	var definitionPath string
	record := &cobra.Command{Use: "record", Short: "Append a versioned reviewed procedure from JSON without executing it", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		body, err := os.ReadFile(definitionPath)
		if err != nil {
			return err
		}
		var definition procedures.Procedure
		if err := json.Unmarshal(body, &definition); err != nil {
			return fmt.Errorf("decode procedure: %w", err)
		}
		return withApp(cmd, func(application *app.App) error {
			definition.ProjectID = application.ProjectID
			value, err := application.Procedures.Append(cmd.Context(), definition)
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), value)
		})
	}}
	record.Flags().StringVar(&definitionPath, "file", "", "procedure JSON file (required)")
	_ = record.MarkFlagRequired("file")

	var revision int
	var showOutput string
	var listOptions procedures.ListOptions
	show := &cobra.Command{Use: "show [ID]", Short: "Show a procedure (check or runbook) by ID, or list the latest revision of each", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		format, err := outputFormat(showOutput)
		if err != nil {
			return err
		}
		return withApp(cmd, func(application *app.App) error {
			if len(args) == 0 {
				values, err := application.Procedures.List(cmd.Context(), application.ProjectID, listOptions)
				if err != nil {
					return err
				}
				if format == "json" {
					return writeJSON(cmd.OutOrStdout(), values)
				}
				for _, value := range values {
					fmt.Fprintf(cmd.OutOrStdout(), "procedure:%s@%d\tkind=%s\tstatus=%s\tsteps=%d\ttitle=%s\n", value.ID, value.Revision, value.Kind, value.Status, len(value.Steps), singleLineText(value.Title))
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Procedures: %d. Listing does not execute any step.\n", len(values))
				return nil
			}
			value, err := application.Procedures.Latest(cmd.Context(), application.ProjectID, args[0])
			if revision > 0 {
				value, err = application.Procedures.Get(cmd.Context(), application.ProjectID, args[0], revision)
			}
			if err != nil {
				return err
			}
			if value == nil {
				return fmt.Errorf("procedure %q not found in this project", args[0])
			}
			if format == "json" {
				return writeJSON(cmd.OutOrStdout(), value)
			}
			writeProcedureText(cmd, *value)
			return nil
		})
	}}
	show.Flags().IntVar(&revision, "revision", 0, "exact revision; zero selects the latest revision of any status")
	show.Flags().StringVar(&listOptions.Kind, "kind", "", "list filter: check or runbook")
	show.Flags().StringVar(&listOptions.Status, "status", "", "list filter: proposed, accepted, or superseded")
	show.Flags().IntVar(&listOptions.Limit, "limit", 50, "list limit (1-100)")
	show.Flags().StringVar(&showOutput, "output", "json", "text or json")
	parent.AddCommand(record, show)
	return parent
}

func writeProcedureText(cmd *cobra.Command, value procedures.Procedure) {
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "Procedure: procedure:%s@%d\nTitle: %s\nPurpose: %s\nKind: %s\nStatus: %s\nAuthority: %s\nSource: %s (%s)\nWorking directory: %s\nPlatforms: %s\nRecorded: %s\n", value.ID, value.Revision, singleLineText(value.Title), singleLineText(value.Purpose), value.Kind, value.Status, value.Authority, value.Source, valueOrUnknown(value.SourceRef), value.WorkingDirectory, strings.Join(value.Platforms, ","), value.RecordedAt.Format(time.RFC3339Nano))
	fmt.Fprintf(w, "Parameters: %d; declared effects: %d; validations: %d; post-run checks: %d; steps: %d\n", len(value.Parameters), len(value.DeclaredEffects), len(value.Validations), len(value.PostRunChecks), len(value.Steps))
	for _, effect := range value.DeclaredEffects {
		fmt.Fprintf(w, "Declared effect: %s\n", singleLineText(effect))
	}
	for _, check := range value.Validations {
		fmt.Fprintf(w, "Validation: %s\n", singleLineText(check))
	}
	for _, check := range value.PostRunChecks {
		fmt.Fprintf(w, "Post-run check: %s\n", singleLineText(check))
	}
	for _, step := range value.Steps {
		if step.StageKind == "" {
			fmt.Fprintf(w, "Step: %s description=%s argv=%q\n", step.ID, singleLineText(step.Description), step.ArgvTemplate)
			continue
		}
		fmt.Fprintf(w, "Step: %s stage=%s:%s description=%s argv=%q\n", step.ID, step.StageKind, step.StageID, singleLineText(step.Description), step.ArgvTemplate)
	}
	fmt.Fprintln(w, "This procedure is a saved definition; showing it does not execute any step.")
}

func memoryCommand() *cobra.Command {
	parent := &cobra.Command{Use: "memory", Short: "Manage portable project memory"}
	var exportDirectory, exportTransport string
	export := &cobra.Command{Use: "export", Short: "Atomically publish a complete curated bundle to a new directory", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if strings.TrimSpace(exportDirectory) == "" {
			return errors.New("--directory is required")
		}
		return withApp(cmd, func(application *app.App) error {
			var result portable.ExportResult
			var err error
			switch exportTransport {
			case "local-directory":
				result, err = portable.ExportOperationalDirectory(cmd.Context(), application.DB, application.ProjectID, exportDirectory, time.Time{})
			case "git-reviewed-v1":
				result, err = portable.ExportOperationalGitReviewedDirectory(cmd.Context(), application.DB, application.ProjectID, application.Root, exportDirectory, time.Time{})
			default:
				return fmt.Errorf("unsupported export transport %q", exportTransport)
			}
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), result)
		})
	}}
	export.Flags().StringVar(&exportDirectory, "directory", "", "new local bundle directory or existing in-project Git-reviewed root (required)")
	export.Flags().StringVar(&exportTransport, "transport", "local-directory", "local-directory or git-reviewed-v1; Git-reviewed exports perform no Git actions")

	var inspectDirectory string
	inspect := &cobra.Command{Use: "inspect", Short: "Validate and summarize one bounded local bundle without writes", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if strings.TrimSpace(inspectDirectory) == "" {
			return errors.New("--directory is required")
		}
		result, err := portable.InspectBundleDirectory(inspectDirectory)
		if err != nil {
			return err
		}
		return writeJSON(cmd.OutOrStdout(), result)
	}}
	inspect.Flags().StringVar(&inspectDirectory, "directory", "", "bundle directory (required)")

	var planDirectory string
	plan := &cobra.Command{Use: "plan", Short: "Plan an import into the selected project without writes", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if strings.TrimSpace(planDirectory) == "" {
			return errors.New("--directory is required")
		}
		manifest, records, err := portable.ReadBundleDirectory(planDirectory)
		if err != nil {
			return err
		}
		return withApp(cmd, func(application *app.App) error {
			result, err := portable.PlanOperationalImport(cmd.Context(), application.DB, application.ProjectID, manifest, records)
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), result)
		})
	}}
	plan.Flags().StringVar(&planDirectory, "directory", "", "bundle directory (required)")

	var applyDirectory, confirmLineage, confirmProject string
	apply := &cobra.Command{Use: "apply", Short: "Apply a validated append-only import with exact identity confirmation", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if strings.TrimSpace(applyDirectory) == "" {
			return errors.New("--directory is required")
		}
		manifest, records, err := portable.ReadBundleDirectory(applyDirectory)
		if err != nil {
			return err
		}
		bundle, err := portable.ValidateBundle(manifest, records)
		if err != nil {
			return err
		}
		return withApp(cmd, func(application *app.App) error {
			if confirmLineage != bundle.Manifest.LineageID || confirmProject != application.ProjectID {
				return fmt.Errorf("explicit confirmation required: --confirm-lineage %s --confirm-project %s", bundle.Manifest.LineageID, application.ProjectID)
			}
			result, err := portable.ApplyOperationalImport(cmd.Context(), application.DB, application.ProjectID, manifest, records, time.Time{})
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), result)
		})
	}}
	apply.Flags().StringVar(&applyDirectory, "directory", "", "bundle directory (required)")
	apply.Flags().StringVar(&confirmLineage, "confirm-lineage", "", "exact inspected lineage ID (required)")
	apply.Flags().StringVar(&confirmProject, "confirm-project", "", "exact selected local project ID (required)")

	parent.AddCommand(export, inspect, plan, apply)
	return parent
}

func addSelectionFlags(command *cobra.Command, selection *checkpoint.Selection) {
	command.Flags().StringVar(&selection.CheckpointID, "checkpoint-id", "", "select one exact checkpoint")
	command.Flags().StringVar(&selection.TaskID, "task-id", "", "select the latest checkpoint for a stable task ID")
	command.Flags().StringVar(&selection.SessionID, "session", "", "select the latest checkpoint for a client session")
}

func narrativeCommand() *cobra.Command {
	parent := &cobra.Command{Use: "narrative", Short: "Record and retrieve commit-optional work narratives"}
	var record worknarrative.Record
	var claimsJSON, anchorsJSON, occurredFrom, occurredTo, commitRef, commitRepository string
	var noNote bool
	recordCommand := &cobra.Command{Use: "record", Short: "Append one immutable work narrative", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if record.Title == "" || (record.Trigger == "" && commitRef == "") || claimsJSON == "" {
			return errors.New("--title, --claims-json, and --trigger (or --commit) are required")
		}
		if err := json.Unmarshal([]byte(claimsJSON), &record.Claims); err != nil {
			return fmt.Errorf("decode --claims-json: %w", err)
		}
		if anchorsJSON != "" {
			if err := json.Unmarshal([]byte(anchorsJSON), &record.Anchors); err != nil {
				return fmt.Errorf("decode --anchors-json: %w", err)
			}
		}
		if occurredFrom != "" {
			value, err := time.Parse(time.RFC3339Nano, occurredFrom)
			if err != nil {
				return fmt.Errorf("parse --occurred-from: %w", err)
			}
			record.OccurredFrom = &value
		}
		if occurredTo != "" {
			value, err := time.Parse(time.RFC3339Nano, occurredTo)
			if err != nil {
				return fmt.Errorf("parse --occurred-to: %w", err)
			}
			record.OccurredTo = &value
		}
		return withApp(cmd, func(application *app.App) error {
			record.ProjectID = application.ProjectID
			if commitRef == "" {
				created, err := application.Narratives.Append(cmd.Context(), record)
				if err != nil {
					return err
				}
				return writeJSON(cmd.OutOrStdout(), worknarrative.RecordResult{Record: created})
			}
			created, note, err := application.Narratives.RecordCommit(cmd.Context(), record, commitRef, commitRepository, noNote)
			if created.ID != "" {
				if writeErr := writeJSON(cmd.OutOrStdout(), worknarrative.RecordResult{Record: created, Note: &note}); writeErr != nil {
					return writeErr
				}
			}
			return err
		})
	}}
	recordCommand.Flags().StringVar(&record.ID, "id", "", "stable narrative ID; generated when omitted")
	recordCommand.Flags().StringVar(&record.Title, "title", "", "short narrative title (required)")
	recordCommand.Flags().StringVar(&record.Trigger, "trigger", "", "meaningful_checkpoint, accepted_decision, completed_roadmap_slice, commit, or manual")
	recordCommand.Flags().StringVar(&claimsJSON, "claims-json", "", "JSON array of ordered claim objects (required)")
	recordCommand.Flags().StringVar(&anchorsJSON, "anchors-json", "", "JSON array of ordered anchor objects")
	recordCommand.Flags().StringVar(&occurredFrom, "occurred-from", "", "optional RFC3339 occurrence start")
	recordCommand.Flags().StringVar(&occurredTo, "occurred-to", "", "optional RFC3339 occurrence end")
	recordCommand.Flags().StringVar(&record.Creator, "creator", "human", "creator identity claim")
	recordCommand.Flags().StringVar(&record.CreatorInvocationID, "creator-invocation", "", "optional exact actor invocation ID")
	recordCommand.Flags().StringVar(&record.SupersedesID, "supersedes", "", "current project-owned work narrative corrected by this append")
	recordCommand.Flags().StringVar(&commitRepository, "repo", "", "workspace member holding --commit (default: the member containing the working directory)")
	recordCommand.Flags().StringVar(&commitRef, "commit", "", "commit to anchor: requires outcome, intent, verification, and limitation claims; adds the exact commit and content-hashed changed-file anchors, the commit time, and trigger commit when unset; then writes the commit's Git note from the claims")
	recordCommand.Flags().BoolVar(&noNote, "no-note", false, "with --commit, record the narrative without writing the Git note")

	var options worknarrative.ListOptions
	var listOutput string
	listCommand := &cobra.Command{Use: "list", Short: "List unified narrative metadata without loading bodies", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return withApp(cmd, func(application *app.App) error {
			result, err := application.Narratives.List(cmd.Context(), application.ProjectID, options)
			if err != nil {
				return err
			}
			format, err := outputFormat(listOutput)
			if err != nil {
				return err
			}
			if format == "json" {
				return writeJSON(cmd.OutOrStdout(), result)
			}
			writeNarrativeListText(cmd, result)
			return nil
		})
	}}
	listCommand.Flags().StringVar(&options.RecordType, "type", "", "work_narrative")
	listCommand.Flags().StringVar(&options.Trigger, "trigger", "", "exact trigger filter")
	listCommand.Flags().StringVar(&options.AnchorKind, "anchor-kind", "", "exact anchor kind filter")
	listCommand.Flags().StringVar(&options.Anchor, "anchor", "", "exact anchor target filter; matches any anchor kind unless --anchor-kind is set")
	listCommand.Flags().StringVar(&options.State, "state", "", "current or superseded")
	listCommand.Flags().IntVar(&options.Limit, "limit", worknarrative.DefaultListLimit, "maximum metadata records (1-100)")
	listCommand.Flags().StringVar(&options.Cursor, "cursor", "", "opaque cursor from a prior result")
	listCommand.Flags().StringVar(&listOutput, "output", "json", "text or json")

	var id string
	var maxBytes int
	var output string
	showCommand := &cobra.Command{Use: "show", Short: "Load one exact work narrative as bounded Markdown", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return withApp(cmd, func(application *app.App) error {
			result, err := application.Narratives.Show(cmd.Context(), application.ProjectID, id, maxBytes)
			if err != nil {
				return err
			}
			if result == nil {
				return fmt.Errorf("work narrative %q not found in this project", id)
			}
			format, err := outputFormat(output)
			if err != nil {
				return err
			}
			if format == "json" {
				return writeJSON(cmd.OutOrStdout(), result)
			}
			_, err = fmt.Fprint(cmd.OutOrStdout(), result.Markdown)
			return err
		})
	}}
	showCommand.Flags().StringVar(&id, "id", "", "exact narrative:<id> reference or bare ID (required)")
	showCommand.Flags().IntVar(&maxBytes, "max-bytes", worknarrative.DefaultRenderBytes, "Markdown byte budget (512-32768)")
	showCommand.Flags().StringVar(&output, "output", "text", "text or json")
	parent.AddCommand(recordCommand, listCommand, showCommand, narrativeImportCommand())
	return parent
}

func writeNarrativeListText(cmd *cobra.Command, result worknarrative.ListResult) {
	fmt.Fprintf(cmd.OutOrStdout(), "Records: %d of %d candidates (scope %d)\n", result.Measurement.RecordsReturned, result.Measurement.CandidateRecords, result.Measurement.ScopeRecords)
	for _, record := range result.Records {
		reference := "narrative:" + record.ID
		fmt.Fprintf(cmd.OutOrStdout(), "%s\tstate=%s\ttype=%s\ttrigger=%s\trecorded=%s\tanchors=%d\ttitle=%s\n", reference, record.State, record.RecordType, valueOrUnknown(record.Trigger), record.RecordedAt.Format(time.RFC3339Nano), len(record.Anchors), singleLineText(record.Title))
	}
	if result.NextCursor != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Next cursor: %s\n", result.NextCursor)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Measurement: loaded=%d metadata_bytes=%d content_bytes=%d expansion_depth=%d\n", result.Measurement.RecordsLoaded, result.Measurement.MetadataBytes, result.Measurement.ContentBytes, result.Measurement.ExpansionDepth)
	fmt.Fprintf(cmd.OutOrStdout(), "Authority: %s\n%s\n", result.Authority, result.Disclaimer)
}

func singleLineText(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func canonicalOptionalReference(kind, id string) string {
	if id == "" {
		return "none"
	}
	return kind + ":" + id
}

func joinCanonicalReferences(kind string, ids []string) string {
	if len(ids) == 0 {
		return "none"
	}
	references := make([]string, 0, len(ids))
	for _, id := range ids {
		references = append(references, kind+":"+singleLineText(id))
	}
	return strings.Join(references, ",")
}

func trackPlanCommand() *cobra.Command {
	parent := &cobra.Command{Use: "track-plan", Short: "Persist, inspect, and resume versioned track plans"}
	var path, reviewStatus, reviewer string
	var claim func() lease.Claim
	record := &cobra.Command{Use: "record [PLAN_ID]", Short: "Append one complete immutable plan revision from --file, or record a human review of PLAN_ID's latest revision", Long: `With --file, append one complete immutable plan revision from JSON.
With PLAN_ID instead, record an explicit human review of the plan's latest
revision as --status accepted, active, or completed (completed requires every
item finished); --creator names the reviewing human.`, Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if (len(args) == 1) == (path != "") {
			return errors.New("give exactly one of --file and a plan ID to review")
		}
		if len(args) == 1 {
			if reviewer == "" {
				return errors.New("--creator is required to record a review")
			}
			return withApp(cmd, func(application *app.App) error {
				accepted, err := application.AcceptPlan(cmd.Context(), args[0], reviewStatus, reviewer, claim())
				if err != nil {
					return err
				}
				return writeJSON(cmd.OutOrStdout(), accepted)
			})
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var value trackplan.Plan
		if err := json.Unmarshal(body, &value); err != nil {
			return fmt.Errorf("decode track plan: %w", err)
		}
		return withApp(cmd, func(application *app.App) error {
			value.ProjectID = application.ProjectID
			value.Fence.Claim = claim()
			created, err := application.AppendPlan(cmd.Context(), value)
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), created)
		})
	}}
	record.Flags().StringVar(&path, "file", "", "JSON plan definition")
	record.Flags().StringVar(&reviewStatus, "status", "accepted", "review: accepted, active, or completed")
	record.Flags().StringVar(&reviewer, "creator", "", "review: reviewing human identity")
	claim = leaseClaimFlags(record)

	var options trackplan.PlanListOptions
	var historyOptions trackplan.PlanHistoryOptions
	var revision int
	var history, ready, render, detail bool
	var showOutput, itemID string
	show := &cobra.Command{Use: "show [PLAN_ID]", Short: "Outline one plan revision (--detail in full, --item one item, --history its revisions); without an ID list plans, or with --ready list ready items, or with --render print the current-plan projection", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		format, err := outputFormat(showOutput)
		if err != nil {
			return err
		}
		if len(args) == 1 && (ready || render) || len(args) == 0 && history {
			return errors.New("--history needs a plan ID; --ready and --render take none")
		}
		return withApp(cmd, func(application *app.App) error {
			switch {
			case render:
				projection, err := application.TrackPlans.RenderCurrentPlan(cmd.Context(), application.ProjectID)
				if err != nil {
					return err
				}
				if format == "json" {
					return writeJSON(cmd.OutOrStdout(), projection)
				}
				_, err = io.WriteString(cmd.OutOrStdout(), projection.Markdown)
				return err
			case ready:
				values, err := application.TrackPlans.Ready(cmd.Context(), application.ProjectID, trackplan.PlanListOptions{RoadmapNodeID: options.RoadmapNodeID, TaskID: options.TaskID, Limit: options.Limit})
				if err != nil {
					return err
				}
				if format == "text" {
					writeTrackPlanReadyText(cmd, values, options.Limit)
					return nil
				}
				return writeJSON(cmd.OutOrStdout(), values)
			case len(args) == 0:
				if format == "json" {
					values, err := application.TrackPlans.ListPlans(cmd.Context(), application.ProjectID, options)
					if err != nil {
						return err
					}
					return writeJSON(cmd.OutOrStdout(), values)
				}
				page, err := application.TrackPlans.ListPlansPage(cmd.Context(), application.ProjectID, options)
				if err != nil {
					return err
				}
				writeTrackPlanListText(cmd, page)
				return nil
			case history:
				if format == "text" {
					historyOptions.Limit = options.Limit
					page, err := application.TrackPlans.PlanHistoryPage(cmd.Context(), application.ProjectID, args[0], historyOptions)
					if err != nil {
						return err
					}
					writeTrackPlanHistoryText(cmd, page)
					return nil
				}
				values, err := application.TrackPlans.PlanHistory(cmd.Context(), application.ProjectID, args[0])
				if err != nil || detail {
					if err != nil {
						return err
					}
					return writeJSON(cmd.OutOrStdout(), values)
				}
				revisions := make([]trackplan.PlanRevisionSummary, 0, len(values))
				for _, value := range values {
					revisions = append(revisions, value.RevisionSummary())
				}
				return writeJSON(cmd.OutOrStdout(), revisions)
			}
			value, err := application.TrackPlans.Plan(cmd.Context(), application.ProjectID, args[0], revision)
			if err != nil {
				return err
			}
			if value == nil {
				return fmt.Errorf("track plan %q not found in this project", args[0])
			}
			if itemID != "" {
				item, ok := value.Item(itemID)
				if !ok {
					return fmt.Errorf("plan:%s@%d has no item %q", value.ID, value.Revision, itemID)
				}
				return writeJSON(cmd.OutOrStdout(), item)
			}
			if format == "json" {
				if detail {
					return writeJSON(cmd.OutOrStdout(), value)
				}
				return writeJSON(cmd.OutOrStdout(), value.Outline())
			}
			writeTrackPlanText(cmd, *value, detail)
			return nil
		})
	}}
	show.Flags().IntVar(&revision, "revision", 0, "exact revision; defaults to latest")
	show.Flags().IntVar(&historyOptions.Offset, "offset", 0, "with --history, zero-based revision offset in text output")
	show.Flags().BoolVar(&history, "history", false, "list the plan's revisions (with --detail, every revision in full)")
	show.Flags().BoolVar(&detail, "detail", false, "include item detail, evidence, and work items")
	show.Flags().StringVar(&itemID, "item", "", "print only this item in full")
	show.Flags().BoolVar(&ready, "ready", false, "list dependency-satisfied items from accepted or active latest plans")
	show.Flags().BoolVar(&render, "render", false, "print the byte-stable current-plan projection without writing files")
	show.Flags().StringVar(&options.Status, "status", "", "list: exact latest-revision status")
	show.Flags().StringVar(&options.RoadmapNodeID, "roadmap-node", "", "list, ready: exact roadmap node ID")
	show.Flags().StringVar(&options.TaskID, "task", "", "list, ready: exact task ID")
	show.Flags().IntVar(&options.Limit, "limit", 20, "list, ready: maximum plans; history text: maximum revisions (1-100)")
	show.Flags().StringVar(&showOutput, "output", "json", "text or json")

	var checkPath string
	var checkOutput string
	check := &cobra.Command{Use: "check", Short: "Compare the current-plan projection byte-for-byte without writing it", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return withApp(cmd, func(application *app.App) error {
			result, err := application.TrackPlans.CheckCurrentPlanProjection(cmd.Context(), application.ProjectID, checkPath)
			if err != nil {
				return err
			}
			format, err := outputFormat(checkOutput)
			if err != nil {
				return err
			}
			if format == "json" {
				return writeJSON(cmd.OutOrStdout(), result)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Projection: %s\nPath: %s\nExpected hash: %s\nSource hash: %s\n", result.State, result.Path, result.ExpectedHash, result.SourceHash)
			if result.ActualHash != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "Actual hash: %s\n", result.ActualHash)
			}
			fmt.Fprintln(cmd.OutOrStdout(), result.Disclaimer)
			return nil
		})
	}}
	check.Flags().StringVar(&checkPath, "path", trackplan.CurrentPlanProjectionPath, "project-relative projection file to check")
	check.Flags().StringVar(&checkOutput, "output", "text", "text or json")

	parent.AddCommand(record, show, check)
	return parent
}

func writeTrackPlanListText(cmd *cobra.Command, page trackplan.PlanListPage) {
	fmt.Fprintf(cmd.OutOrStdout(), "Track plans: %d of %d (limit %d, truncated %t, next_cursor=unavailable)\n", len(page.Plans), page.Total, page.Limit, page.Truncated)
	for _, value := range page.Plans {
		fmt.Fprintf(cmd.OutOrStdout(), "plan:%s@%d\tstatus=%s\trecorded=%s\troadmap=roadmap:%s\ttask=%s\titems=%d\tready=%d\ttitle=%s\n", value.ID, value.Revision, value.Status, value.RecordedAt.Format(time.RFC3339Nano), value.RoadmapNodeID, canonicalOptionalReference("task", value.TaskID), value.ItemCount, value.ReadyCount, singleLineText(value.Title))
	}
	fmt.Fprintln(cmd.OutOrStdout(), "Authority: caller-asserted plan revisions and deterministic dependency readiness")
	fmt.Fprintln(cmd.OutOrStdout(), "This metadata view does not select, activate, resume, or execute a plan or item.")
}

func writeTrackPlanText(cmd *cobra.Command, plan trackplan.Plan, detail bool) {
	fmt.Fprintf(cmd.OutOrStdout(), "Track plan: plan:%s@%d\n", singleLineText(plan.ID), plan.Revision)
	fmt.Fprintf(cmd.OutOrStdout(), "Roadmap: %s\nTask: %s\nStatus: %s\nRecorded: %s\nCreator: %s\nAuthority: %s\nTitle: %s\nSummary: %s\nItems: %d (maximum 100; plan limit 256 KiB)\n", singleLineText(canonicalOptionalReference("roadmap", plan.RoadmapNodeID)), singleLineText(canonicalOptionalReference("task", plan.TaskID)), plan.Status, plan.RecordedAt.Format(time.RFC3339Nano), singleLineText(plan.Creator), plan.Authority, singleLineText(plan.Title), singleLineText(plan.Summary), len(plan.Items))
	if plan.Model != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Model: %s\n", singleLineText(plan.Model))
	}
	if len(plan.WorkItemIDs) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Work items: %s\n", joinCanonicalReferences("work_item", plan.WorkItemIDs))
	}
	for _, item := range plan.Items {
		parent := canonicalOptionalReference("item", item.ParentID)
		if !detail {
			fmt.Fprintf(cmd.OutOrStdout(), "item:%s\tposition=%d\tstatus=%s\tparent=%s\tdepends_on=%s\ttitle=%s\n", singleLineText(item.ID), item.Position, item.Status, singleLineText(parent), joinCanonicalReferences("item", item.DependsOn), singleLineText(item.Title))
			continue
		}
		fmt.Fprintf(cmd.OutOrStdout(), "item:%s\tposition=%d\tstatus=%s\tparent=%s\ttitle=%s\tdetail=%s\n", singleLineText(item.ID), item.Position, item.Status, singleLineText(parent), singleLineText(item.Title), singleLineText(item.Detail))
		if len(item.DependsOn) > 0 || len(item.EvidenceRefs) > 0 || len(item.WorkItemIDs) > 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "  depends_on=%s evidence=%s work_items=%s\n", joinCanonicalReferences("item", item.DependsOn), singleLineText(strings.Join(item.EvidenceRefs, ",")), joinCanonicalReferences("work_item", item.WorkItemIDs))
		}
	}
}

func writeTrackPlanHistoryText(cmd *cobra.Command, page trackplan.PlanHistoryPage) {
	fmt.Fprintf(cmd.OutOrStdout(), "Track plan revisions: %d at offset %d of %d (limit %d, has_more %t, next_cursor=unavailable)\n", len(page.Revisions), page.Offset, page.Total, page.Limit, page.HasMore)
	for _, value := range page.Revisions {
		fmt.Fprintf(cmd.OutOrStdout(), "plan:%s@%d\tstatus=%s\trecorded=%s\troadmap=roadmap:%s\ttask=%s\titems=%d\ttitle=%s\n", value.ID, value.Revision, value.Status, value.RecordedAt.Format(time.RFC3339Nano), value.RoadmapNodeID, canonicalOptionalReference("task", value.TaskID), value.ItemCount, singleLineText(value.Title))
	}
	fmt.Fprintln(cmd.OutOrStdout(), "Authority: caller-asserted plan revisions and deterministic metadata counts")
	fmt.Fprintln(cmd.OutOrStdout(), "This metadata view omits plan summaries and item details; it does not select, activate, resume, or execute a plan or item.")
}

func writeTrackPlanReadyText(cmd *cobra.Command, values []trackplan.ReadyItem, planLimit int) {
	if planLimit == 0 {
		planLimit = 20
	}
	const itemLimit = 100
	shown := min(len(values), itemLimit)
	fmt.Fprintf(cmd.OutOrStdout(), "Ready items: %d of %d within the inspected plans (plan limit %d, item display limit %d, item display truncated %t)\n", shown, len(values), planLimit, itemLimit, len(values) > shown)
	for _, value := range values[:shown] {
		fmt.Fprintf(cmd.OutOrStdout(), "plan:%s@%d/item:%s\troadmap=%s\ttask=%s\tstatus=%s\ttitle=%s\n", singleLineText(value.PlanID), value.PlanRevision, singleLineText(value.Item.ID), singleLineText(canonicalOptionalReference("roadmap", value.RoadmapNodeID)), singleLineText(canonicalOptionalReference("task", value.TaskID)), value.Item.Status, singleLineText(value.Item.Title))
	}
	fmt.Fprintln(cmd.OutOrStdout(), "The plan limit can omit other plans and their ready items; this read has no continuation cursor.")
	fmt.Fprintln(cmd.OutOrStdout(), "Authority: caller-asserted plans and deterministic dependency readiness. This view does not select, activate, resume, or execute work.")
}

func versionCommand() *cobra.Command {
	var output string
	command := &cobra.Command{
		Use:   "version",
		Short: "Report this executable's build and API identity",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			info := buildinfo.Current()
			format, err := outputFormat(output)
			if err != nil {
				return err
			}
			if format == "json" {
				return writeJSON(cmd.OutOrStdout(), info)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Version: %s\nCommit: %s\nDirty: %t\nBuild time: %s\nGo: %s\nMCP API: %d\nWriter API: %d\nExecutable: %s\n", info.ApplicationVersion, info.GitCommit, info.BuildDirty, valueOrUnknown(info.BuildTime), info.GoVersion, info.MCPAPIVersion, info.WriterAPIVersion, info.Executable)
			return err
		},
	}
	command.Flags().StringVar(&output, "output", "text", "text or json")
	return command
}

func valueOrUnknown(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}

func globalCommand() *cobra.Command {
	parent := &cobra.Command{Use: "global", Short: "Manage engineer-wide guidance shared by all projects"}
	parent.AddCommand(&cobra.Command{
		Use: "path", Short: "Print the engineer-wide guidance directory", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := config.GlobalDir()
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), path)
			return err
		},
	}, &cobra.Command{
		Use: "init", Short: "Create global guidance files without replacing existing content", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := config.InitGlobal()
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Global guidance: %s\nEdit AGENTS.md, CONTEXT.md, and PROMPTS.md; all projects load these on their next bootstrap.\n", path)
			return err
		},
	})
	return parent
}

func projectRoot(cmd *cobra.Command) (string, error) {
	start, err := cmd.Root().PersistentFlags().GetString("project-root")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(start) == "" {
		start, err = os.Getwd()
		if err != nil {
			return "", err
		}
	} else if _, err := os.Stat(start); err != nil {
		return "", fmt.Errorf("project root: %w", err)
	}
	checkout, err := project.Resolve(start)
	if err != nil {
		return "", err
	}
	return checkout.Root, nil
}

// projectCheckout resolves the working checkout, including its workspace member.
func projectCheckout(cmd *cobra.Command) (project.Checkout, error) {
	if _, err := projectRoot(cmd); err != nil {
		return project.Checkout{}, err
	}
	return currentCheckout(cmd)
}

func withApp(cmd *cobra.Command, fn func(*app.App) error) error {
	checkout, err := projectCheckout(cmd)
	if err != nil {
		return err
	}
	application, err := app.OpenCheckout(cmd.Context(), checkout)
	if err != nil {
		return err
	}
	enableAutomaticSync(application)
	enableCrossMachineLeases(application)
	defer application.Close()
	return fn(application)
}

func withReadOnlyApp(cmd *cobra.Command, fn func(*app.App) error) error {
	checkout, err := projectCheckout(cmd)
	if err != nil {
		return err
	}
	application, err := app.OpenCheckoutReadOnly(cmd.Context(), checkout)
	if err != nil {
		return err
	}
	defer application.Close()
	return fn(application)
}

func databaseCommand() *cobra.Command {
	parent := &cobra.Command{Use: "database", Short: "Inspect and explicitly migrate project storage"}
	parent.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Inspect schema state without changing the database",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := projectRoot(cmd)
			if err != nil {
				return err
			}
			cfg, err := config.Load(root)
			if err != nil {
				return err
			}
			status, err := database.Inspect(cmd.Context(), cfg.Storage.Database)
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), status)
		},
	})
	var backupPath string
	backupCommand := &cobra.Command{
		Use:   "backup",
		Short: "Create and verify a consistent database backup",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := projectRoot(cmd)
			if err != nil {
				return err
			}
			cfg, err := config.Load(root)
			if err != nil {
				return err
			}
			destination := backupPath
			if destination == "" {
				destination, err = database.NextBackupPath(cfg.Backups.Dir, time.Now())
				if err != nil {
					return err
				}
			}
			result, err := database.Backup(cmd.Context(), cfg.Storage.Database, destination)
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), result)
		},
	}
	backupCommand.Flags().StringVar(&backupPath, "backup", "", "destination path; generated under the configured backup directory when omitted")
	var applyPrune bool
	pruneCommand := &cobra.Command{
		Use:   "prune",
		Short: "Preview backups eligible for pruning, or remove them with --apply",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := projectRoot(cmd)
			if err != nil {
				return err
			}
			cfg, err := config.Load(root)
			if err != nil {
				return err
			}
			plan, err := database.PlanBackupRetention(database.BackupRetentionOptions{Dir: cfg.Backups.Dir, KeepLast: cfg.Backups.KeepLast, KeepDays: cfg.Backups.KeepDays, Pinned: cfg.Backups.Pinned})
			if err != nil {
				return err
			}
			if applyPrune {
				receipt, applyErr := database.ApplyBackupRetention(plan, time.Now())
				if applyErr != nil {
					if len(receipt.Removed) > 0 {
						_ = writeJSON(cmd.OutOrStdout(), receipt)
					}
					return applyErr
				}
				return writeJSON(cmd.OutOrStdout(), receipt)
			}
			return writeJSON(cmd.OutOrStdout(), plan)
		},
	}
	pruneCommand.Flags().BoolVar(&applyPrune, "apply", false, "remove the current dry-run candidates after revalidation")
	backupCommand.AddCommand(pruneCommand)
	parent.AddCommand(backupCommand)
	var from, to, backup string
	migrate := &cobra.Command{
		Use:   "migrate",
		Short: "Back up, verify, and explicitly apply pending migrations",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := projectRoot(cmd)
			if err != nil {
				return err
			}
			cfg, err := config.Load(root)
			if err != nil {
				return err
			}
			result, err := database.Migrate(cmd.Context(), cfg.Storage.Database, database.MigrateOptions{From: from, To: to, BackupPath: backup, BackupRetention: &database.BackupRetentionOptions{Dir: cfg.Backups.Dir, KeepLast: cfg.Backups.KeepLast, KeepDays: cfg.Backups.KeepDays, Pinned: cfg.Backups.Pinned}})
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), result)
		},
	}
	migrate.Flags().StringVar(&from, "from", "", "confirm the exact currently applied migration")
	migrate.Flags().StringVar(&to, "to", "", "confirm the exact compiled target migration")
	migrate.Flags().StringVar(&backup, "backup", "", "required path for a new consistent pre-migration backup")
	_ = migrate.MarkFlagRequired("from")
	_ = migrate.MarkFlagRequired("to")
	_ = migrate.MarkFlagRequired("backup")
	var floorSet int
	writerFloor := &cobra.Command{
		Use:   "writer-floor",
		Short: "Show the oldest writer API allowed to write, or with --set lower it before rolling back to an older build",
		Long: `A build raises the store's writer floor only when its write semantics are
incompatible with older processes. Ordinary MCP API upgrades leave the writer
floor unchanged, so active clients can still checkpoint. A long-running process
older than the writer floor refuses writes until restarted. Lower the floor with
--set only when deliberately reinstalling an older writer.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withApp(cmd, func(application *app.App) error {
				if floorSet > 0 {
					if err := database.SetWriterFloor(cmd.Context(), application.DB, floorSet); err != nil {
						return err
					}
				}
				floor, err := database.WriterFloor(cmd.Context(), application.DB)
				if err != nil {
					return err
				}
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "Writer floor: writer API %d (this build: %d; MCP API: %d)\n", floor, buildinfo.WriterAPIVersion, buildinfo.MCPAPIVersion)
				return err
			})
		},
	}
	writerFloor.Flags().IntVar(&floorSet, "set", 0, "set the floor to this writer API (only to roll back)")
	parent.AddCommand(migrate, writerFloor)
	return parent
}

func initCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Initialize continuity state for the current project",
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := projectRoot(cmd)
			if err != nil {
				return err
			}
			cfg, err := config.Load(root)
			if err != nil {
				return err
			}
			configPath := filepath.Join(config.StateDir(root), config.FileName)
			configExisted, err := pathExists(configPath)
			if err != nil {
				return fmt.Errorf("inspect config: %w", err)
			}
			databaseExisted, err := pathExists(cfg.Storage.Database)
			if err != nil {
				return fmt.Errorf("inspect database: %w", err)
			}
			path, err := config.WriteDefault(root)
			if err != nil {
				return err
			}
			if !databaseExisted {
				db, err := database.Initialize(cmd.Context(), cfg.Storage.Database)
				if err != nil {
					return fmt.Errorf("initialize database: %w", err)
				}
				if err := db.Close(); err != nil {
					return err
				}
			}
			return withApp(cmd, func(application *app.App) error {
				if configExisted && databaseExisted {
					fmt.Fprintf(cmd.OutOrStdout(), "Already initialized %s\n", application.Config.Project.Name)
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "Initialized %s\n", application.Config.Project.Name)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Config: %s (%s)\nDatabase: %s (%s)\n", path, creationStatus(configExisted), application.Config.Storage.Database, creationStatus(databaseExisted))
				if configExisted && databaseExisted {
					fmt.Fprintln(cmd.OutOrStdout(), "No initialization files were created.")
					fmt.Fprintln(cmd.OutOrStdout(), "Run `llewm doctor` to verify project health.")
				}
				return nil
			})
		},
	}
}

func pathExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func creationStatus(existed bool) string {
	if existed {
		return "existing"
	}
	return "created"
}

func doctorCommand() *cobra.Command {
	var readinessReport, checkIntegrity bool
	var probes, sourceVerifications []string
	command := &cobra.Command{
		Use:   "doctor",
		Short: "Verify project discovery, configuration, storage, and client files",
		Long: `Verify project discovery, configuration, storage, and client files.
With --readiness (implied by --probe, --source-verification, or
--check-integrity), instead print the operational-readiness report as JSON:
separate source, installed artifact, database, runtime, and named probe
observations. Nothing is changed either way.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withApp(cmd, func(application *app.App) error {
				if readinessReport || checkIntegrity || len(probes) > 0 || len(sourceVerifications) > 0 {
					report, err := application.Readiness.Report(cmd.Context(), readiness.Input{Probes: probes, SourceVerifications: sourceVerifications, CheckIntegrity: checkIntegrity})
					if err != nil {
						return err
					}
					return writeJSON(cmd.OutOrStdout(), report)
				}
				migration, err := database.LatestMigration(cmd.Context(), application.DB)
				if err != nil {
					return err
				}
				runtimeHealth := buildinfo.CaptureRuntime().CheckInstalled()
				identity := runtimeHealth.Runtime.Build
				checks := []struct {
					name     string
					ok       bool
					detail   string
					optional bool
				}{
					{"project root", true, application.Root, false},
					{"database", application.DB.PingContext(cmd.Context()) == nil, application.Config.Storage.Database, false},
					{"binary", true, identity.ApplicationVersion + " " + identity.GitCommit, false},
					{"runtime", runtimeHealth.Status == "current", runtimeHealth.Status, false},
					{"DB schema", true, migration, false},
					{"AGENTS.md", fileExists(filepath.Join(application.Root, "AGENTS.md")), "project instruction source", false},
					{"CLAUDE.md", fileExists(filepath.Join(application.Root, "CLAUDE.md")), "optional project instruction source", true},
				}
				failed := false
				for _, check := range checks {
					status := "ok"
					if !check.ok {
						if check.optional {
							status = "absent"
						} else {
							status = "missing"
							failed = true
						}
					}
					fmt.Fprintf(cmd.OutOrStdout(), "%-12s %-8s %s\n", check.name, status, check.detail)
				}
				// Missing client rules are a warning, not a failure: the
				// person may prefer prompts. Only installed clients are checked.
				if homes, err := clientperm.DefaultHomes(); err == nil {
					tools, _ := mcpserver.ToolNames(cmd.Context(), application)
					for _, client := range clientperm.Detect(homes, application.Root, tools) {
						status, detail := "ok", client.Name()+": "+strings.Join(client.Configured, ", ")
						if !client.OK() {
							status, detail = "warning", client.Warning()
						}
						fmt.Fprintf(cmd.OutOrStdout(), "%-12s %-8s %s\n", "client rules", status, detail)
					}
				}
				if homes, err := clientperm.DefaultHomes(); err == nil && fileExists(filepath.Join(application.Root, coverage.DefaultRequirementsPath)) {
					for _, client := range clientperm.Detect(homes, application.Root, nil) {
						status, detail, err := coverageDoctorLine(cmd.Context(), application, string(client.Client))
						if err != nil {
							return err
						}
						fmt.Fprintf(cmd.OutOrStdout(), "%-12s %-8s %s\n", "coverage", status, detail)
					}
				}
				if failed {
					return errors.New("one or more checks need attention")
				}
				return nil
			})
		},
	}
	command.Flags().BoolVar(&readinessReport, "readiness", false, "print the operational-readiness report as JSON")
	command.Flags().StringSliceVar(&probes, "probe", nil, "readiness: named live probe; supported form is narrative:<id>")
	command.Flags().StringSliceVar(&sourceVerifications, "source-verification", nil, "readiness: caller-reported source verification reference")
	command.Flags().BoolVar(&checkIntegrity, "check-integrity", false, "readiness: run SQLite PRAGMA integrity_check and report it separately")
	return command
}

func hookCommand() *cobra.Command {
	var auditOnly bool
	command := &cobra.Command{
		Use:   "hook CLIENT [EVENT]",
		Short: "Ingest a client lifecycle event from JSON on stdin",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			const maxHookBytes = 2 << 20
			body, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), maxHookBytes+1))
			if err != nil {
				return err
			}
			if len(body) > maxHookBytes {
				return errors.New("hook payload exceeds 2 MiB limit")
			}
			if len(args) == 2 {
				body, err = addEventName(body, args[1])
				if err != nil {
					return err
				}
			}
			return withApp(cmd, func(application *app.App) error {
				if auditOnly || application.Config.Mode == "audit-only" {
					return auditHook(cmd, application, args[0], body)
				}
				var codexEnvelope clients.HookEnvelope
				if strings.EqualFold(args[0], "codex") {
					_ = json.Unmarshal(body, &codexEnvelope)
				}
				var eventErr error
				var eventClient = strings.ToLower(args[0])
				var event activity.Event
				var invocation attribution.Invocation
				switch eventClient {
				case "codex":
					hookName := codexEnvelope.HookEvent
					if hookName == "" {
						hookName = codexEnvelope.EventName
					}
					storeRaw := application.Config.Privacy.StoreRawPayloads && !strings.EqualFold(hookName, "Stop") && !strings.EqualFold(hookName, "UserPromptSubmit")
					observation, err := codex.DecodeObservation(application.ProjectID, body, storeRaw)
					event, invocation, eventErr = observation.Event, observation.Invocation, err
				case "claude", "claude-code":
					observation, err := claude.DecodeObservation(application.ProjectID, body, application.Config.Privacy.StoreRawPayloads)
					event, invocation, eventErr = observation.Event, observation.Invocation, err
				default:
					return fmt.Errorf("unsupported client %q", args[0])
				}
				if eventErr != nil {
					return eventErr
				}
				if event.Payload == nil {
					event.Payload = map[string]any{}
				}
				event.Payload["continuity_mode"] = "full"
				storedInvocation, err := application.Attribution.AppendInvocation(cmd.Context(), invocation)
				if err != nil {
					return err
				}
				invocation = storedInvocation
				if err := application.Events.Append(cmd.Context(), event); err != nil {
					return err
				}
				if err := application.Events.ObserveSession(cmd.Context(), application.ProjectID, event.SessionID, event.Client, event.ActorModel, event.EventType, event.OccurredAt); err != nil {
					return err
				}
				hookUsage(cmd.Context(), application, event.Client, strings.ToLower(event.EventType), event.SessionID)
				var envelope clients.HookEnvelope
				_ = json.Unmarshal(body, &envelope)
				_ = application.ObservePresence(cmd.Context(), event.Client, event.SessionID, invocation.ID, event.EventType, envelope.CWD, event.ReceivedAt)
				// Best-effort: attribution never blocks a hook.
				application.ObserveHeads(cmd.Context(), event.Client, event.SessionID, strings.ToLower(event.EventType), envelope.ToolName, envelope.ToolInput)
				if strings.EqualFold(event.EventType, "sessionstart") {
					// Sync first so records from other machines shape this bootstrap.
					syncLine := sessionStartSync(cmd.Context(), application)
					// A resumed session keeps its session ID, so its live task presence comes first.
					packet, err := application.Bootstrap.BuildForSession(cmd.Context(), application.ProjectID, application.Root, application.Config.Project.Name, checkpoint.Selection{}, event.SessionID)
					if err != nil {
						return err
					}
					markdown := bootstrap.Markdown(packet)
					// Best-effort: a failed record only means continuity_bootstrap repeats the packet.
					_ = bootstrap.RecordDelivery(cmd.Context(), application.DB, bootstrap.Delivery{ProjectID: application.ProjectID, Client: event.Client, SessionID: event.SessionID, DeliveredAt: time.Now(), CheckpointID: packet.CheckpointID(), PacketSHA256: bootstrap.PacketHash(markdown)})
					if event.SessionID != "" {
						// After the delivery hash, like the sync line: the session is not packet state.
						markdown += "\nThis session: `" + event.SessionID + "`. Pass it as session_id when checkpointing, so a resumed or compacted session keeps its task.\n"
					}
					if syncLine != "" {
						markdown += "\n" + syncLine + "\n"
					}
					if line := worknarrative.UnnarratedLine(application.UnnarratedCommits(cmd.Context())); line != "" {
						markdown += "\n" + line + "\n"
					}
					if application.Config.RoadmapScan.OnSessionStart {
						if context := sessionStartRoadmapContext(cmd.Context(), application); context != "" {
							markdown += "\n" + context + "\n"
						}
					}
					return writeJSON(cmd.OutOrStdout(), map[string]any{
						"hookSpecificOutput": map[string]any{
							"hookEventName":     "SessionStart",
							"additionalContext": markdown,
						},
					})
				}
				if strings.EqualFold(event.EventType, "posttooluse") {
					policy := checkpoint.ReminderPolicy{
						MaxAgeSeconds: application.Config.CheckpointReminder.RemindAfterSeconds,
						MaxEvents:     application.Config.CheckpointReminder.RemindAfterEvents,
					}
					if policy.MaxAgeSeconds == 0 && policy.MaxEvents == 0 {
						return nil
					}
					taskID, err := application.SessionTask(cmd.Context(), event.SessionID)
					if err != nil {
						return err
					}
					edge, err := application.Checkpoints.EvaluateReminderEdge(cmd.Context(), application.ProjectID, event.Client, event.SessionID, taskID, policy)
					if err != nil {
						return err
					}
					if edge.ShouldSurface {
						reasons := strings.Join(edge.Freshness.ReminderReasons, ", ")
						return writeJSON(cmd.OutOrStdout(), map[string]any{
							"hookSpecificOutput": map[string]any{
								"hookEventName":     "PostToolUse",
								"additionalContext": "Continuity advisory: explicit checkpoint reminder thresholds were met (" + reasons + "). Consider using the Llewm Continuity checkpoint skill at the next safe task boundary. This does not infer task progress.",
							},
						})
					}
				}
				return nil
			})
		},
	}
	command.Flags().BoolVar(&auditOnly, "audit-only", false, "collect usage silently without bootstrap, reminders, sync, presence or attribution")
	return command
}

// sessionStartRoadmapContext runs the opt-in deterministic roadmap scan and
// returns its lifecycle context, or "" for current/not-due state. Any scan
// error is swallowed here so an optional, best-effort check never displaces
// bootstrap delivery.
func sessionStartRoadmapContext(ctx context.Context, application *app.App) string {
	status, err := application.Roadmap.Status(ctx, application.ProjectID)
	if err != nil {
		return ""
	}
	return roadmap.LifecycleContext(status)
}

func checkpointCommand() *cobra.Command {
	parent := &cobra.Command{Use: "checkpoint", Short: "Create and inspect continuity checkpoints"}
	var task, taskID, predecessorCheckpointID, reviewsTaskID, requestKey, status, summary, next, session, createOutput string
	var attemptsJSON, actionUpdatesJSON string
	var completed, changes, verified, evidenceRefs, carryForwardActionIDs []string
	var dryRun bool
	var claim func() lease.Claim
	create := &cobra.Command{
		Use:   "create",
		Short: "Create a structured checkpoint, or with --dry-run compile it against the startup budget without saving",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if task == "" || next == "" {
				return errors.New("--task and --next are required")
			}
			format, err := outputFormat(createOutput)
			if err != nil {
				return err
			}
			var attempts []checkpoint.Attempt
			if attemptsJSON != "" {
				if err := json.Unmarshal([]byte(attemptsJSON), &attempts); err != nil {
					return fmt.Errorf("decode --attempts-json: %w", err)
				}
			}
			var actionUpdates []checkpoint.ActionUpdate
			if actionUpdatesJSON != "" {
				if err := json.Unmarshal([]byte(actionUpdatesJSON), &actionUpdates); err != nil {
					return fmt.Errorf("decode --action-updates-json: %w", err)
				}
			}
			value := checkpoint.Checkpoint{
				SessionID: session, TaskID: taskID, PredecessorCheckpointID: predecessorCheckpointID, ReviewsTaskID: reviewsTaskID, RequestKey: requestKey, Task: task,
				Status: status, Summary: summary, NextAction: next,
				Completed: completed, Changes: changes, Verified: verified, Attempts: attempts, EvidenceRefs: evidenceRefs,
				Fence: lease.Fence{Claim: claim()},
			}
			reconciliation := checkpoint.ActionReconciliation{Updates: actionUpdates, CarryForwardActionIDs: carryForwardActionIDs}
			return withApp(cmd, func(application *app.App) error {
				if dryRun {
					result, err := application.Bootstrap.PreflightWithReconciliation(cmd.Context(), application.ProjectID, application.Root, application.Config.Project.Name, value, reconciliation)
					if err != nil {
						return err
					}
					if format == "json" {
						return writeJSON(cmd.OutOrStdout(), result)
					}
					fmt.Fprintf(cmd.OutOrStdout(), "Ready: %t\nBudget: %d bytes\nJSON: %d bytes\nMarkdown: %d bytes\nOversize by: %d bytes\n", result.Ready, result.MaxBytes, result.JSONBytes, result.MarkdownBytes, result.OversizeByBytes)
					fmt.Fprintf(cmd.OutOrStdout(), "Markdown sections: base=%d global_guidance=%d project_guidance=%d checkpoint_kernel=%d checkpoint_detail=%d unresolved_actions=%d\n", result.BaseBytes, result.GlobalGuidanceBytes, result.ProjectGuidanceBytes, result.CheckpointKernelBytes, result.CheckpointDetailBytes, result.UnresolvedActionBytes)
					fmt.Fprintf(cmd.OutOrStdout(), "Actions: current=%d projected=%d reconciled=%d explicitly_carried=%d creates_next=%t\n", result.CurrentUnresolvedActionCount, result.ProjectedUnresolvedActionCount, result.ReconciledActionCount, result.ExplicitlyCarriedActionCount, result.CreatesNextAction)
					_, err = fmt.Fprintln(cmd.OutOrStdout(), result.Disclaimer)
					return err
				}
				created, err := application.SaveCheckpointWithReconciliation(cmd.Context(), value, reconciliation)
				if err == nil {
					if format == "json" {
						return writeJSON(cmd.OutOrStdout(), struct {
							ID                string                           `json:"id"`
							CarriedForward    []checkpoint.Action              `json:"carried_forward"`
							Reconciled        []checkpoint.Action              `json:"reconciled"`
							ActionCreated     bool                             `json:"action_created"`
							Lease             *lease.Stamp                     `json:"lease,omitempty"`
							SessionLease      string                           `json:"session_lease,omitempty"`
							UnnarratedCommits []worknarrative.UnnarratedCommit `json:"unnarrated_commits,omitempty"`
							ResearchCapture   *researchcapture.Report          `json:"research_capture,omitempty"`
						}{created.Checkpoint.ID, created.CarriedForward, created.Reconciled, created.ActionCreated, created.Lease, created.SessionLease, application.UnnarratedCommits(cmd.Context()), application.ResearchCapture(cmd.Context(), value)})
					}
					fmt.Fprintln(cmd.OutOrStdout(), created.Checkpoint.ID)
					if line := researchcapture.Line(application.ResearchCapture(cmd.Context(), value)); line != "" {
						fmt.Fprintln(cmd.OutOrStdout(), line)
					}
					if strings.Contains(created.SessionLease, "is leased by") || strings.Contains(created.SessionLease, "failed") {
						fmt.Fprintln(cmd.ErrOrStderr(), "Note:", created.SessionLease)
					}
					if created.Lease != nil && created.Lease.State == lease.Lapsed {
						fmt.Fprintln(cmd.ErrOrStderr(), "Note:", created.Lease.Note())
					}
				}
				return err
			})
		},
	}
	create.Flags().StringVar(&task, "task", "", "current task title")
	create.Flags().StringVar(&taskID, "task-id", "", "stable task identifier")
	create.Flags().StringVar(&predecessorCheckpointID, "predecessor-checkpoint", "", "exact current task-tip checkpoint for strict continuation")
	create.Flags().StringVar(&requestKey, "request-key", "", "optional idempotent retry key")
	create.Flags().StringVar(&status, "status", "in_progress", "task status")
	if err := create.RegisterFlagCompletionFunc("status", func(_ *cobra.Command, _ []string, prefix string) ([]string, cobra.ShellCompDirective) {
		var matches []string
		for _, value := range []string{"in_progress", "completed", "blocked", "paused"} {
			if strings.HasPrefix(value, prefix) {
				matches = append(matches, value)
			}
		}
		return matches, cobra.ShellCompDirectiveNoFileComp
	}); err != nil {
		panic(err) // A missing or duplicate static flag registration is a programming error.
	}
	create.Flags().StringVar(&summary, "summary", "", "current-state summary")
	create.Flags().StringVar(&next, "next", "", "recommended next action")
	create.Flags().StringVar(&session, "session", "", "client session identifier")
	create.Flags().StringVar(&reviewsTaskID, "reviews-task-id", "", "another task this work reviews or repairs; shown in both tasks' bootstraps")
	create.Flags().StringArrayVar(&completed, "completed", nil, "completed work item; may be repeated")
	create.Flags().StringArrayVar(&changes, "change", nil, "changed file or artifact; may be repeated")
	create.Flags().StringArrayVar(&verified, "verified", nil, "verification claim; may be repeated")
	create.Flags().StringVar(&attemptsJSON, "attempts-json", "", "JSON array of attempts with description, outcome, and optional reason")
	create.Flags().StringArrayVar(&evidenceRefs, "evidence-ref", nil, "evidence reference; may be repeated")
	create.Flags().StringVar(&actionUpdatesJSON, "action-updates-json", "", "JSON array of predecessor action updates")
	create.Flags().StringArrayVar(&carryForwardActionIDs, "carry-forward-action", nil, "unfinished predecessor action ID; may be repeated")
	claim = leaseClaimFlags(create)
	create.Flags().BoolVar(&dryRun, "dry-run", false, "validate, redact, and compile against the startup byte budget without saving")
	create.Flags().StringVar(&createOutput, "output", "text", "text or json")
	var query, cursor string
	var limit, maxBytes int
	var showOutput string
	show := &cobra.Command{
		Use:   "show [ID]",
		Short: "Export one prior handoff by ID, or with --query find handoffs by literal text",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if (len(args) == 1) == (query != "") {
				return errors.New("give exactly one of a checkpoint ID and --query")
			}
			format, err := outputFormat(showOutput)
			if err != nil {
				return err
			}
			return withApp(cmd, func(application *app.App) error {
				if query != "" {
					result, err := application.Checkpoints.Find(cmd.Context(), application.ProjectID, checkpoint.FindRequest{Query: query, Limit: limit, Cursor: cursor})
					if err != nil {
						return err
					}
					if format == "json" {
						return writeJSON(cmd.OutOrStdout(), result)
					}
					if len(result.Hits) == 0 {
						fmt.Fprintln(cmd.OutOrStdout(), "No matching handoffs.")
						return nil
					}
					for _, hit := range result.Hits {
						fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\n", hit.ID, hit.CreatedAt.Format(time.RFC3339Nano), hit.Status, hit.Task)
						if hit.Excerpt != "" {
							fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", hit.Excerpt)
						}
					}
					if result.NextCursor != "" {
						fmt.Fprintf(cmd.OutOrStdout(), "Next cursor: %s\n", result.NextCursor)
					}
					return nil
				}
				export, err := application.Checkpoints.Get(cmd.Context(), application.ProjectID, args[0], maxBytes)
				if err != nil {
					return err
				}
				if export == nil {
					return fmt.Errorf("checkpoint %q not found in this project", args[0])
				}
				if format == "json" {
					return writeJSON(cmd.OutOrStdout(), export)
				}
				fmt.Fprint(cmd.OutOrStdout(), export.Markdown)
				return nil
			})
		},
	}
	show.Flags().StringVar(&query, "query", "", "literal text to find instead of exporting one checkpoint")
	show.Flags().IntVar(&limit, "limit", checkpoint.DefaultHistoryLimit, "with --query, maximum matches")
	show.Flags().StringVar(&cursor, "cursor", "", "with --query, opaque cursor from a prior result")
	show.Flags().IntVar(&maxBytes, "max-bytes", checkpoint.DefaultExportBytes, "Markdown export byte budget")
	show.Flags().StringVar(&showOutput, "output", "text", "text or json")
	var actionsOutput, actionsTaskID string
	var actionsAll bool
	actions := &cobra.Command{Use: "actions", Short: "List a task's unresolved checkpoint actions", RunE: func(cmd *cobra.Command, _ []string) error {
		return withApp(cmd, func(application *app.App) error {
			values, err := application.Checkpoints.ListActions(cmd.Context(), application.ProjectID, actionsTaskID, actionsAll)
			if err != nil {
				return err
			}
			format, err := outputFormat(actionsOutput)
			if err != nil {
				return err
			}
			if format == "json" {
				return writeJSON(cmd.OutOrStdout(), map[string]any{"actions": values})
			}
			for _, value := range values {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\n", value.ID, value.TaskID, value.Status, value.Description)
			}
			return nil
		})
	}}
	actions.Flags().StringVar(&actionsOutput, "output", "text", "text or json")
	actions.Flags().StringVar(&actionsTaskID, "task-id", "", "list this task's actions")
	actions.Flags().BoolVar(&actionsAll, "all", false, "list every task's actions; without --task-id or --all, refuses when several tasks have actions")
	var actionID, actionStatus, actionReason, actionTaskID string
	actionUpdate := &cobra.Command{Use: "action-update", Short: "Complete or supersede a checkpoint action", RunE: func(cmd *cobra.Command, _ []string) error {
		return withApp(cmd, func(application *app.App) error {
			value, err := application.Checkpoints.TransitionAction(cmd.Context(), application.ProjectID, actionTaskID, actionID, actionStatus, actionReason)
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), value)
		})
	}}
	actionUpdate.Flags().StringVar(&actionID, "id", "", "checkpoint action identifier")
	actionUpdate.Flags().StringVar(&actionStatus, "status", "", "completed or superseded")
	actionUpdate.Flags().StringVar(&actionReason, "reason", "", "transition reason")
	actionUpdate.Flags().StringVar(&actionTaskID, "task-id", "", "your task; refuses an action owned by another task")
	var operation, lifecycleTaskID, targetTaskID, lifecycleCheckpointID, lifecycleRequestKey, lifecycleReason string
	taskTransition := &cobra.Command{Use: "task-transition", Short: "Append an explicit task start, fork, rekey, supersede or abandon operation", RunE: func(cmd *cobra.Command, _ []string) error {
		return withApp(cmd, func(application *app.App) error {
			value, err := application.Checkpoints.TransitionTask(cmd.Context(), checkpoint.TaskTransition{ProjectID: application.ProjectID, Operation: operation, TaskID: lifecycleTaskID, TargetTaskID: targetTaskID, CheckpointID: lifecycleCheckpointID, RequestKey: lifecycleRequestKey, Reason: lifecycleReason})
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), value)
		})
	}}
	taskTransition.Flags().StringVar(&operation, "operation", "", "start, fork, rekey, supersede or abandon")
	taskTransition.Flags().StringVar(&lifecycleTaskID, "task-id", "", "task acted on")
	taskTransition.Flags().StringVar(&targetTaskID, "target-task-id", "", "new or replacement task for fork, rekey or supersede")
	taskTransition.Flags().StringVar(&lifecycleCheckpointID, "checkpoint-id", "", "exact current task tip, or adopted tip for start")
	taskTransition.Flags().StringVar(&lifecycleRequestKey, "request-key", "", "required idempotent retry key")
	taskTransition.Flags().StringVar(&lifecycleReason, "reason", "", "caller-supplied reason")
	var lineageTaskID string
	var taskLineageLimit int
	var taskLineageOutput string
	taskLineage := &cobra.Command{Use: "task-lineage", Short: "Read one task's lifecycle history and derived state, or without --task-id report lineage anomalies across tasks", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		format, err := outputFormat(taskLineageOutput)
		if err != nil {
			return err
		}
		return withApp(cmd, func(application *app.App) error {
			if lineageTaskID == "" {
				value, err := application.Checkpoints.LineageAnomalies(cmd.Context(), application.ProjectID, taskLineageLimit)
				if err != nil {
					return err
				}
				if format == "json" {
					return writeJSON(cmd.OutOrStdout(), value)
				}
				writeLineageReportText(cmd, value)
				return nil
			}
			value, err := application.Checkpoints.TaskLineage(cmd.Context(), application.ProjectID, lineageTaskID, taskLineageLimit)
			if err != nil {
				return err
			}
			if format == "json" {
				return writeJSON(cmd.OutOrStdout(), value)
			}
			writeTaskLineageText(cmd, value)
			return nil
		})
	}}
	taskLineage.Flags().StringVar(&lineageTaskID, "task-id", "", "task identifier; omit for the anomaly report")
	taskLineage.Flags().IntVar(&taskLineageLimit, "limit", checkpoint.DefaultLineageReportLimit, "maximum lifecycle events, or anomalous tasks without --task-id")
	taskLineage.Flags().StringVar(&taskLineageOutput, "output", "json", "text or json")
	recovery := checkpointRecoveryCommand()
	parent.AddCommand(create, show, actions, actionUpdate, taskTransition, taskLineage, recovery)
	return parent
}

func checkpointRecoveryCommand() *cobra.Command {
	parent := &cobra.Command{Use: "recovery", Short: "Inspect, apply, or discard device-local checkpoint recovery envelopes"}
	var listOutput string
	list := &cobra.Command{Use: "list", Short: "List pending structured checkpoint envelopes", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		format, err := outputFormat(listOutput)
		if err != nil {
			return err
		}
		return withApp(cmd, func(application *app.App) error {
			values, err := application.CheckpointRecovery.List()
			if err != nil {
				return err
			}
			if format == "json" {
				return writeJSON(cmd.OutOrStdout(), map[string]any{"recoveries": values})
			}
			if len(values) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No pending checkpoint recovery envelopes.")
				return nil
			}
			for _, value := range values {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\t%s\n", value.ID, value.CreatedAt.Format(time.RFC3339Nano), value.Status, value.TaskID, value.Task)
			}
			return nil
		})
	}}
	list.Flags().StringVar(&listOutput, "output", "text", "text or json")

	apply := &cobra.Command{Use: "apply ID", Short: "Save one recovered checkpoint with current semantics and remove it after confirmed readback", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return withApp(cmd, func(application *app.App) error {
			created, err := application.ApplyCheckpointRecovery(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), struct {
				RecoveryID       string                           `json:"recovery_id"`
				ID               string                           `json:"id"`
				ReadbackVerified bool                             `json:"readback_verified"`
				Removed          bool                             `json:"recovery_removed"`
				CarriedForward   []checkpoint.Action              `json:"carried_forward"`
				Reconciled       []checkpoint.Action              `json:"reconciled"`
				ActionCreated    bool                             `json:"action_created"`
				Lease            *lease.Stamp                     `json:"lease,omitempty"`
				SessionLease     string                           `json:"session_lease,omitempty"`
				Unnarrated       []worknarrative.UnnarratedCommit `json:"unnarrated_commits,omitempty"`
			}{args[0], created.Checkpoint.ID, true, true, created.CarriedForward, created.Reconciled, created.ActionCreated, created.Lease, created.SessionLease, application.UnnarratedCommits(cmd.Context())})
		})
	}}

	discard := &cobra.Command{Use: "discard ID", Short: "Explicitly discard one unsaved checkpoint recovery envelope", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return withApp(cmd, func(application *app.App) error {
			if _, err := application.CheckpointRecovery.Get(args[0]); err != nil {
				return err
			}
			if err := application.CheckpointRecovery.Remove(args[0]); err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), map[string]any{"recovery_id": args[0], "discarded": true})
		})
	}}

	parent.AddCommand(list, apply, discard)
	return parent
}

func writeTaskLineageText(cmd *cobra.Command, value checkpoint.TaskLineageView) {
	fmt.Fprintf(cmd.OutOrStdout(), "Task: task:%s\nState: %s managed=%t", value.TaskID, value.State.Status, value.State.Managed)
	if value.State.OriginOperation != "" {
		fmt.Fprintf(cmd.OutOrStdout(), " origin=%s", value.State.OriginOperation)
	}
	if value.State.ReplacementTask != "" {
		fmt.Fprintf(cmd.OutOrStdout(), " replacement=task:%s", value.State.ReplacementTask)
	}
	fmt.Fprintln(cmd.OutOrStdout())
	fmt.Fprintf(cmd.OutOrStdout(), "Current tip: %s\nEvents: %d (limit %d, truncated %t)\n", checkpointReference(value.CurrentTip), len(value.Events), value.Limit, value.Truncated)
	for _, event := range value.Events {
		fmt.Fprintf(cmd.OutOrStdout(), "lifecycle:%s\t%s\ttask=task:%s", event.ID, event.Operation, event.TaskID)
		if event.TargetTaskID != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "\ttarget=task:%s", event.TargetTaskID)
		}
		if event.CheckpointID != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "\tcheckpoint=checkpoint:%s", event.CheckpointID)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "\tcreated=%s\n", event.CreatedAt.Format(time.RFC3339Nano))
		if event.Reason != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "  reason=%s\n", singleLineText(event.Reason))
		}
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Authority: %s\n%s\n", value.Authority, value.Disclaimer)
}

func writeLineageReportText(cmd *cobra.Command, value checkpoint.LineageReport) {
	fmt.Fprintf(cmd.OutOrStdout(), "Anomalous tasks: %d (limit %d, truncated %t)\n", len(value.Anomalies), value.Limit, value.Truncated)
	for _, anomaly := range value.Anomalies {
		fmt.Fprintf(cmd.OutOrStdout(), "task:%s\tcodes=%s\tcheckpoints=%d roots=%d tips=%d\tlifecycle=%s", anomaly.TaskID, strings.Join(anomaly.Codes, ","), anomaly.CheckpointCount, anomaly.RootCount, anomaly.TipCount, anomaly.LifecycleStatus)
		if anomaly.SelectedCheckpointID != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "\tselected=checkpoint:%s(%s)", anomaly.SelectedCheckpointID, valueOrUnknown(anomaly.SelectedStatus))
		}
		if anomaly.ReplacementTaskID != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "\treplacement=task:%s", anomaly.ReplacementTaskID)
		}
		fmt.Fprintln(cmd.OutOrStdout())
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Authority: %s\n%s\n", value.Authority, value.Disclaimer)
}

func checkpointReference(id string) string {
	if id == "" {
		return "-"
	}
	return "checkpoint:" + id
}

func bootstrapCommand() *cobra.Command {
	var output string
	var selection checkpoint.Selection
	command := &cobra.Command{
		Use:   "bootstrap",
		Short: "Compile the compact bootstrap for a fresh session",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withApp(cmd, func(application *app.App) error {
				packet, err := application.Bootstrap.BuildSelected(cmd.Context(), application.ProjectID, application.Root, application.Config.Project.Name, selection)
				if err != nil {
					return err
				}
				format, err := outputFormat(output)
				if err != nil {
					return err
				}
				if format == "json" {
					return writeJSON(cmd.OutOrStdout(), packet)
				}
				fmt.Fprint(cmd.OutOrStdout(), bootstrap.Markdown(packet))
				return nil
			})
		},
	}
	command.Flags().StringVar(&output, "output", "text", "text or json")
	addSelectionFlags(command, &selection)
	return command
}

func activityCommand() *cobra.Command {
	var limit int
	var output string
	command := &cobra.Command{
		Use:   "activity",
		Short: "Summarize local activity and model allocation, with recent agent invocation attribution",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			format, err := outputFormat(output)
			if err != nil {
				return err
			}
			return withApp(cmd, func(application *app.App) error {
				summary, err := application.Audit.Summarize(cmd.Context(), application.ProjectID)
				if err != nil {
					return err
				}
				snapshot, err := application.Attribution.Snapshot(cmd.Context(), application.ProjectID, limit)
				if err != nil {
					return err
				}
				if format == "json" {
					return writeJSON(cmd.OutOrStdout(), struct {
						Summary     audit.Summary        `json:"summary"`
						Attribution attribution.Snapshot `json:"attribution"`
					}{summary, snapshot})
				}
				fmt.Fprint(cmd.OutOrStdout(), audit.Text(summary))
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "Invocations: %d\n", len(snapshot.Invocations))
				return err
			})
		},
	}
	command.Flags().IntVar(&limit, "limit", attribution.DefaultReadLimit, "maximum attribution records per category")
	command.Flags().StringVar(&output, "output", "text", "text or json")
	return command
}

func roadmapCommand() *cobra.Command {
	parent := &cobra.Command{Use: "roadmap", Short: "Record roadmap structure and inspect deterministic freshness"}

	var node roadmap.Node
	nodeCommand := &cobra.Command{Use: "node", Short: "Record an append-only roadmap node revision", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return withApp(cmd, func(application *app.App) error {
			node.ProjectID = application.ProjectID
			created, err := application.Roadmap.AppendNode(cmd.Context(), node)
			if err != nil {
				return err
			}
			if created.Status == "completed" {
				// Advisory only: the revision is recorded either way.
				findings, err := application.Roadmap.NodePlanFindings(cmd.Context(), application.ProjectID, created.ID, created.Status)
				if err != nil {
					return err
				}
				for _, finding := range findings {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s: %s\n", finding, workstate.FindingSummary(finding))
				}
			}
			return writeJSON(cmd.OutOrStdout(), created)
		})
	}}
	nodeCommand.Flags().StringVar(&node.ID, "id", "", "stable roadmap node ID (required)")
	nodeCommand.Flags().IntVar(&node.Revision, "revision", 0, "explicit revision; defaults to the next revision")
	nodeCommand.Flags().StringVar(&node.Title, "title", "", "node title (required)")
	nodeCommand.Flags().StringVar(&node.Status, "status", "", "proposed, accepted, active, blocked, completed, deferred, or superseded")
	nodeCommand.Flags().IntVar(&node.Priority, "priority", 50, "priority from 0 (highest) to 100 (lowest)")
	nodeCommand.Flags().StringVar(&node.Kind, "kind", "work_item", "initiative, milestone, work_item, or research")
	nodeCommand.Flags().StringVar(&node.TargetDate, "target-date", "", "caller-asserted milestone target date")
	nodeCommand.Flags().StringVar(&node.TargetRelease, "target-release", "", "caller-asserted milestone target release")
	nodeCommand.Flags().StringVar(&node.TargetHorizon, "target-horizon", "", "caller-asserted milestone target horizon")
	nodeCommand.Flags().StringSliceVar(&node.EvidenceRefs, "evidence", nil, "exact evidence reference (repeatable)")
	nodeCommand.Flags().StringVar(&node.SourcePath, "source-path", "", "project-relative roadmap source path (required)")
	nodeCommand.Flags().StringVar(&node.SourceRef, "source-ref", "", "source revision or hash")

	var linkKind string
	var link roadmap.NodeLink
	var binding roadmap.Binding
	var taskBinding roadmap.TaskBinding
	linkCommand := &cobra.Command{Use: "link", Short: "Record a roadmap relationship: --kind node (planning link to --target), file (an important file or directory), or task (a versioned node/task binding)", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return withApp(cmd, func(application *app.App) error {
			var created any
			var err error
			switch linkKind {
			case "node":
				link.ProjectID = application.ProjectID
				created, err = application.Roadmap.AppendNodeLink(cmd.Context(), link)
			case "file":
				binding.ProjectID, binding.NodeID, binding.NodeRevision, binding.Relationship = application.ProjectID, link.NodeID, link.NodeRevision, link.Relationship
				created, err = application.Roadmap.AppendBinding(cmd.Context(), binding)
			case "task":
				taskBinding.ProjectID, taskBinding.RoadmapNodeID, taskBinding.RoadmapNodeRevision = application.ProjectID, link.NodeID, link.NodeRevision
				created, err = application.Roadmap.AppendTaskBinding(cmd.Context(), taskBinding)
			default:
				return fmt.Errorf("--kind must be node, file, or task, not %q", linkKind)
			}
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), created)
		})
	}}
	linkCommand.Flags().StringVar(&linkKind, "kind", "node", "node, file, or task")
	linkCommand.Flags().StringVar(&link.NodeID, "node", "", "source roadmap node ID (required)")
	linkCommand.Flags().IntVar(&link.NodeRevision, "node-revision", 0, "source node revision; defaults to latest")
	linkCommand.Flags().StringVar(&link.Relationship, "relationship", "", "node: depends_on, blocks, parallel_with, parent_of, or explores; file: implementation, contract, test, documentation, or verification")
	linkCommand.Flags().StringVar(&link.TargetNodeID, "target", "", "node: target roadmap node ID")
	linkCommand.Flags().StringVar(&binding.Path, "path", "", "file: project-relative file or directory path")
	linkCommand.Flags().StringVar(&binding.PathKind, "path-kind", "file", "file: file or directory")
	linkCommand.Flags().StringVar(&taskBinding.TaskID, "task", "", "task: recorded checkpoint or managed task ID")
	linkCommand.Flags().IntVar(&taskBinding.Revision, "revision", 0, "task: binding revision for this node/task pair; defaults to next")
	linkCommand.Flags().StringVar(&taskBinding.Status, "status", "active", "task: active or superseded")
	linkCommand.Flags().StringVar(&taskBinding.Reason, "reason", "", "task: caller-asserted binding rationale")
	linkCommand.Flags().StringVar(&taskBinding.RequestKey, "request-key", "", "task: stable idempotency key (required)")

	var syncRun roadmap.SyncRun
	var routine roadmap.ReviewRoutine
	var routineCommits, routineFiles int
	var routineElapsed int64
	syncCommand := &cobra.Command{Use: "sync", Short: "Record an accepted model roadmap-sync watermark, or with --routine a versioned review routine", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return withApp(cmd, func(application *app.App) error {
			if routine.ID != "" {
				routine.ProjectID = application.ProjectID
				if cmd.Flags().Changed("commit-threshold") {
					routine.CommitThreshold = &routineCommits
				}
				if cmd.Flags().Changed("elapsed-seconds-threshold") {
					routine.ElapsedSecondsThreshold = &routineElapsed
				}
				if cmd.Flags().Changed("changed-files-threshold") {
					routine.ChangedFilesThreshold = &routineFiles
				}
				created, err := application.Roadmap.AppendReviewRoutine(cmd.Context(), routine)
				if err != nil {
					return err
				}
				return writeJSON(cmd.OutOrStdout(), created)
			}
			syncRun.ProjectID = application.ProjectID
			created, err := application.Roadmap.RecordSync(cmd.Context(), syncRun)
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), created)
		})
	}}
	syncCommand.Flags().StringVar(&syncRun.Client, "client", "", "client that reported the review")
	syncCommand.Flags().StringVar(&syncRun.Model, "model", "", "model that reportedly performed the review")
	syncCommand.Flags().StringVar(&syncRun.InvocationID, "invocation", "", "attributed invocation ID")
	syncCommand.Flags().StringSliceVar(&syncRun.ReviewedNodes, "reviewed-node", nil, "reviewed node ID; may be repeated")
	syncCommand.Flags().StringSliceVar(&syncRun.ReviewedFiles, "reviewed-file", nil, "reviewed file reference; may be repeated")
	syncCommand.Flags().StringVar(&routine.ID, "routine", "", "record this review routine ID instead of a sync")
	syncCommand.Flags().IntVar(&routine.Revision, "routine-revision", 0, "routine: explicit revision; defaults to the next revision")
	syncCommand.Flags().StringVar(&routine.Title, "title", "", "routine: title (required)")
	syncCommand.Flags().StringVar(&routine.Status, "status", "proposed", "routine: proposed, accepted, or superseded")
	syncCommand.Flags().IntVar(&routineCommits, "commit-threshold", 0, "routine: review after this many commits")
	syncCommand.Flags().Int64Var(&routineElapsed, "elapsed-seconds-threshold", 0, "routine: review after this many elapsed seconds")
	syncCommand.Flags().IntVar(&routineFiles, "changed-files-threshold", 0, "routine: review after this many changed bound files")
	syncCommand.Flags().StringVar(&routine.Source, "source", "", "routine: source reference defining it (required)")
	syncCommand.Flags().StringVar(&routine.SourceRef, "source-ref", "", "routine: source revision or hash")

	var viewLimit, viewOffset int
	var viewNode, viewPath, viewOutput string
	viewCommand := &cobra.Command{Use: "view [status|overview|tree|coverage|history|render|check]", Short: "Read the roadmap without model inference: status (default), overview, tree, coverage, node history (--id), render, or check (--path)", Args: cobra.MaximumNArgs(1), ValidArgs: []string{"status", "overview", "tree", "coverage", "history", "render", "check"}, RunE: func(cmd *cobra.Command, args []string) error {
		view := "status"
		if len(args) == 1 {
			view = args[0]
		}
		format, err := outputFormat(viewOutput)
		if err != nil {
			return err
		}
		limit := func(fallback int) int {
			if cmd.Flags().Changed("limit") {
				return viewLimit
			}
			return fallback
		}
		return withApp(cmd, func(application *app.App) error {
			ctx, projectID := cmd.Context(), application.ProjectID
			switch view {
			case "status":
				status, err := application.Roadmap.Status(ctx, projectID)
				if err != nil || format == "json" {
					return firstErr(err, func() error { return writeJSON(cmd.OutOrStdout(), status) })
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Freshness: %s\nCurrent commit: %s\nWorking tree dirty: %t\n", status.Freshness, valueOrUnknown(status.CurrentCommit), status.WorkingTreeDirty)
				if status.LastSync != nil {
					fmt.Fprintf(cmd.OutOrStdout(), "Last model sync: %s\nCommits since sync: %d\nElapsed since sync: %s\n", status.LastSync.ID, *status.CommitsSinceSync, time.Duration(*status.ElapsedSeconds)*time.Second)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Changed bound files: %d\nAffected roadmap nodes: %d\nReview due: %t\n", len(status.ChangedFiles), len(status.AffectedNodes), status.ReviewDue)
				if len(status.DueReasons) > 0 {
					fmt.Fprintf(cmd.OutOrStdout(), "Due reasons: %s\n", strings.Join(status.DueReasons, ", "))
				}
				fmt.Fprintln(cmd.OutOrStdout(), status.Disclaimer)
				return nil
			case "overview":
				overview, err := application.Roadmap.Overview(ctx, projectID, limit(roadmap.DefaultOverviewLimit))
				if err != nil || format == "json" {
					return firstErr(err, func() error { return writeJSON(cmd.OutOrStdout(), overview) })
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Current focus: %d\nBlocked: %d\nFuture: %d\nCompleted: %d\nChanged nodes since sync: %d\nRelationships: %d\nTracks: %d\nReview triggers: %d\nRepository freshness: %s\nReview due: %t\n", len(overview.CurrentFocus), len(overview.Blocked), len(overview.Future), len(overview.Completed), len(overview.ChangedNodesSinceSync), len(overview.Relationships), len(overview.Tracks), len(overview.ReviewTriggers), overview.Status.Freshness, overview.Status.ReviewDue)
				for _, item := range overview.CurrentFocus {
					fmt.Fprintf(cmd.OutOrStdout(), "Focus: [%s] %s\n", item.ID, item.Title)
				}
				for _, item := range overview.Future {
					fmt.Fprintf(cmd.OutOrStdout(), "Future: [%s] %s (%s)\n", item.ID, item.Title, item.Status)
				}
				for _, track := range overview.Tracks {
					fmt.Fprintf(cmd.OutOrStdout(), "Track: [%s] %s (%s)\n", track.Node.ID, track.Node.Title, track.Node.Status)
					writeRoadmapRefs(cmd, "  Parents", track.Parents)
					writeRoadmapRefs(cmd, "  Subtasks", track.Subtasks)
					writeRoadmapRefs(cmd, "  Depends on", track.DependsOn)
					writeRoadmapRefs(cmd, "  Unresolved dependencies", track.UnresolvedDependencies)
					writeRoadmapRefs(cmd, "  Blocked by", track.BlockedBy)
					writeRoadmapRefs(cmd, "  Blocks", track.Blocks)
					writeRoadmapRefs(cmd, "  Parallel with", track.ParallelWith)
					writeRoadmapRefs(cmd, "  Explores", track.Explores)
				}
				for _, trigger := range overview.ReviewTriggers {
					threshold, observed := "n/a", "n/a"
					if trigger.Threshold != nil {
						threshold = fmt.Sprint(*trigger.Threshold)
					}
					if trigger.Observed != nil {
						observed = fmt.Sprint(*trigger.Observed)
					}
					fmt.Fprintf(cmd.OutOrStdout(), "Trigger: %s state=%s observed=%s threshold=%s routine=%s@%d\n", trigger.Kind, trigger.State, observed, threshold, trigger.RoutineID, trigger.RoutineRevision)
				}
				fmt.Fprintln(cmd.OutOrStdout(), overview.Disclaimer)
				return nil
			case "tree":
				tree, err := application.Roadmap.Tree(ctx, projectID, roadmap.TreeOptions{Offset: viewOffset, Limit: limit(roadmap.DefaultTreeLimit)})
				if err != nil || format == "json" {
					return firstErr(err, func() error { return writeJSON(cmd.OutOrStdout(), tree) })
				}
				writeRoadmapTreeText(cmd, tree)
				return nil
			case "coverage":
				coverage, err := application.Roadmap.Coverage(ctx, projectID, roadmap.CoverageOptions{Offset: viewOffset, Limit: limit(roadmap.DefaultCoverageLimit)})
				if err != nil || format == "json" {
					return firstErr(err, func() error { return writeJSON(cmd.OutOrStdout(), coverage) })
				}
				writeRoadmapCoverageText(cmd, coverage)
				return nil
			case "history":
				history, err := application.Roadmap.NodeHistory(ctx, projectID, roadmap.NodeHistoryOptions{NodeID: viewNode, Offset: viewOffset, Limit: limit(roadmap.DefaultNodeHistoryLimit)})
				if err != nil || format == "json" {
					return firstErr(err, func() error { return writeJSON(cmd.OutOrStdout(), history) })
				}
				writeRoadmapNodeHistoryText(cmd, history)
				return nil
			case "render":
				projection, err := application.Roadmap.Render(ctx, projectID)
				if err != nil || format == "json" {
					return firstErr(err, func() error { return writeJSON(cmd.OutOrStdout(), projection) })
				}
				_, err = io.WriteString(cmd.OutOrStdout(), projection.Markdown)
				return err
			case "check":
				result, err := application.Roadmap.CheckProjection(ctx, projectID, viewPath)
				if err != nil || format == "json" {
					return firstErr(err, func() error { return writeJSON(cmd.OutOrStdout(), result) })
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Projection: %s\nPath: %s\nExpected hash: %s\n", result.State, result.Path, result.ExpectedHash)
				if result.ActualHash != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "Actual hash: %s\n", result.ActualHash)
				}
				fmt.Fprintln(cmd.OutOrStdout(), result.Disclaimer)
				return nil
			}
			return fmt.Errorf("roadmap view must be status, overview, tree, coverage, history, render, or check, not %q", view)
		})
	}}
	viewCommand.Flags().IntVar(&viewLimit, "limit", 0, "overview, tree, coverage, history: maximum records (defaults per view)")
	viewCommand.Flags().IntVar(&viewOffset, "offset", 0, "tree, coverage, history: zero-based offset")
	viewCommand.Flags().StringVar(&viewNode, "id", "", "history: exact roadmap node ID")
	viewCommand.Flags().StringVar(&viewPath, "path", "", "check: project-relative generated roadmap path")
	viewCommand.Flags().StringVar(&viewOutput, "output", "text", "text or json")

	var packetBytes int
	var packetManual bool
	var packetOutput string
	packetCommand := &cobra.Command{Use: "review-packet", Short: "Build a bounded source-backed packet for a due review", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		format, err := outputFormat(packetOutput)
		if err != nil {
			return err
		}
		return withApp(cmd, func(application *app.App) error {
			packet, err := application.Roadmap.ReviewPacket(cmd.Context(), application.ProjectID, packetBytes, packetManual)
			if err != nil {
				return err
			}
			if format == "json" {
				return writeJSON(cmd.OutOrStdout(), packet)
			}
			writeRoadmapReviewPacketText(cmd, packet)
			return nil
		})
	}}
	packetCommand.Flags().IntVar(&packetBytes, "max-bytes", roadmap.DefaultReviewPacketBytes, "maximum source-content bytes")
	packetCommand.Flags().BoolVar(&packetManual, "manual", false, "build a packet on explicit request even when review is not due")
	packetCommand.Flags().StringVar(&packetOutput, "output", "json", "text or json")

	parent.AddCommand(nodeCommand, linkCommand, syncCommand, viewCommand, packetCommand)
	return parent
}

// firstErr returns err, or when err is nil the result of next.
func firstErr(err error, next func() error) error {
	if err != nil {
		return err
	}
	return next()
}

func writeRoadmapReviewPacketText(cmd *cobra.Command, packet roadmap.ReviewPacket) {
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "Roadmap review packet: trigger=%s due=%t freshness=%s nodes=%d sources=%d content_bytes=%d/%d truncated=%t\n", packet.Trigger, packet.Status.ReviewDue, packet.Status.Freshness, len(packet.Nodes), len(packet.Sources), packet.ContentBytes, packet.ByteBudget, packet.Truncated)
	fmt.Fprintf(w, "Commit: %s; working_tree_dirty=%t\n", valueOrUnknown(packet.Status.CurrentCommit), packet.Status.WorkingTreeDirty)
	for _, reason := range packet.Status.DueReasons {
		fmt.Fprintf(w, "Due reason: %s\n", singleLineText(reason))
	}
	for _, node := range packet.Nodes {
		fmt.Fprintf(w, "Node: roadmap:%s@%d status=%s priority=%d authority=%s title=%s\n", node.ID, node.Revision, node.Status, node.Priority, node.Authority, singleLineText(node.Title))
	}
	for _, source := range packet.Sources {
		fmt.Fprintf(w, "Source: %s ref=%s sha256=%s authority=%s truncated=%t\n", source.Path, valueOrUnknown(source.SourceRef), source.SHA256, source.Authority, source.Truncated)
		fmt.Fprintf(w, "%s\n", source.Content)
	}
	fmt.Fprintln(w, packet.Disclaimer)
	fmt.Fprintln(w, packet.Status.Disclaimer)
}

func writeTrackInventoryText(cmd *cobra.Command, inventory roadmap.TrackInventory) {
	fmt.Fprintf(cmd.OutOrStdout(), "Tracks: %d of %d (offset %d, limit %d, omitted %d, truncated %t)\n", len(inventory.Items), inventory.Total, inventory.Offset, inventory.Limit, inventory.Omitted, inventory.Truncated)
	fmt.Fprintf(cmd.OutOrStdout(), "Filter: %s\n", inventory.Filter)
	for _, item := range inventory.Items {
		writeWorkItemLine(cmd, item)
		fmt.Fprintf(cmd.OutOrStdout(), "  binding=%s checkpoint=%s actions=%d/%d work_items=%d/%d refs_truncated=%t\n", item.BindingState, inventoryCheckpointRef(item), len(item.Actions), item.UnresolvedActionCount, len(item.WorkItems), item.PendingWorkItems+item.AcceptedWorkItems, item.ActionsTruncated || item.WorkItemsTruncated)
		if len(item.Anomalies) > 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "  findings=%s\n", strings.Join(item.Anomalies, ","))
		}
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Authority: %s\n%s\n", inventory.Authority, inventory.Disclaimer)
}

func inventoryCheckpointRef(item roadmap.TrackInventoryItem) string {
	if item.LatestCheckpoint == nil {
		return "-"
	}
	return item.LatestCheckpoint.ID + "(" + item.LatestCheckpoint.Status + ")"
}

func writeRoadmapRefs(cmd *cobra.Command, label string, values []roadmap.NodeRef) {
	if len(values) == 0 {
		return
	}
	ids := make([]string, 0, len(values))
	for _, value := range values {
		ids = append(ids, value.ID+"("+value.Status+")")
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", label, strings.Join(ids, ", "))
}

func writeRoadmapCoverageText(cmd *cobra.Command, coverage roadmap.Coverage) {
	fmt.Fprintf(cmd.OutOrStdout(), "Current nodes: %d\nNodes with current-revision bindings: %d\nNodes without current-revision bindings: %d\n", coverage.CurrentNodes, coverage.NodesWithCurrentBindings, coverage.NodesWithoutCurrentBindings)
	fmt.Fprintf(cmd.OutOrStdout(), "Current-revision bindings: %d\nPrior-revision bindings: %d\nCurrent relationships: %d\nPrior-revision relationships: %d\n", coverage.CurrentRevisionBindings, coverage.PriorRevisionBindings, coverage.CurrentRelationships, coverage.PriorRevisionRelationships)
	fmt.Fprintf(cmd.OutOrStdout(), "Dangling relationship targets: %d\nHierarchy roots: %d\nNodes with multiple parents: %d\nHierarchy cycle nodes: %d\n", coverage.DanglingRelationshipTargets, coverage.HierarchyRoots, coverage.NodesWithMultipleParents, coverage.HierarchyCycleNodes)
	fmt.Fprintf(cmd.OutOrStdout(), "Nodes: %d of %d (offset %d, limit %d, omitted %d, truncated %t)\n", coverage.Returned, coverage.Total, coverage.Offset, coverage.Limit, coverage.Omitted, coverage.Truncated)
	for _, node := range coverage.Nodes {
		fmt.Fprintf(cmd.OutOrStdout(), "node:%s@%d [%s] %s source=%s source_state=%s source_ref=%s source_comparison=%s bindings=%d prior_bindings=%d prior_relationships=%d", node.ID, node.Revision, node.Status, node.Title, node.SourcePath, node.SourcePathState, valueOrUnknown(node.SourceRef), node.SourceComparison, node.CurrentBindings, node.PriorRevisionBindings, node.PriorRevisionRelationships)
		relationships := make([]string, 0, len(node.Relationships))
		for relationship, count := range node.Relationships {
			relationships = append(relationships, fmt.Sprintf("%s:%d", relationship, count))
		}
		sort.Strings(relationships)
		fmt.Fprintf(cmd.OutOrStdout(), " relationships=%s\n", strings.Join(relationships, ","))
		writeRoadmapRefs(cmd, "  Parents", node.Parents)
		writeRoadmapRefs(cmd, "  Children", node.Children)
		if len(node.DanglingTargets) > 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "  Dangling targets: %s\n", strings.Join(node.DanglingTargets, ", "))
		}
		if len(node.ExpectedScope) > 0 {
			expected := make([]string, 0, len(node.ExpectedScope))
			for _, scope := range node.ExpectedScope {
				expected = append(expected, fmt.Sprintf("%s(%s,%s)", scope.Path, scope.PathKind, scope.Relationship))
			}
			fmt.Fprintf(cmd.OutOrStdout(), "  Expected scope: %s\n", strings.Join(expected, ", "))
		}
		if len(node.ObservedTouches) > 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "  Observed narrative touches: %s\n", strings.Join(node.ObservedTouches, ", "))
		}
		if len(node.ExpectedWithoutObserved) > 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "  Expected without observed touch: %s\n", strings.Join(node.ExpectedWithoutObserved, ", "))
		}
		if len(node.ObservedOutsideExpected) > 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "  Observed outside expected scope: %s\n", strings.Join(node.ObservedOutsideExpected, ", "))
		}
		fmt.Fprintf(cmd.OutOrStdout(), "  Scope evidence truncated: %t\n", node.ScopeEvidenceTruncated)
		if len(node.Observations) > 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "  Observations: %s\n", strings.Join(node.Observations, ", "))
		}
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Source paths checked: %t\nAuthority: %s\n%s\n", coverage.SourcePathsChecked, coverage.Authority, coverage.Disclaimer)
}

func writeRoadmapNodeHistoryText(cmd *cobra.Command, history roadmap.NodeHistory) {
	fmt.Fprintf(cmd.OutOrStdout(), "Node: %s\nRevisions: %d of %d (offset %d, limit %d, omitted %d, truncated %t)\n", history.NodeID, history.Returned, history.Total, history.Offset, history.Limit, history.Omitted, history.Truncated)
	for _, revision := range history.Revisions {
		current := ""
		if revision.Current {
			current = " current"
		}
		fields := "none"
		if len(revision.ChangedFields) > 0 {
			fields = strings.Join(revision.ChangedFields, ",")
		}
		fmt.Fprintf(cmd.OutOrStdout(), "rev:%d%s [%s] priority=%d title=%q source=%s source_ref=%s changed=%s recorded=%s\n", revision.Revision, current, revision.Status, revision.Priority, revision.Title, revision.SourcePath, valueOrUnknown(revision.SourceRef), fields, revision.RecordedAt.Format(time.RFC3339Nano))
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Authority: %s\n%s\n", history.Authority, history.Disclaimer)
}

func writeRoadmapTreeText(cmd *cobra.Command, tree roadmap.RoadmapTree) {
	fmt.Fprintf(cmd.OutOrStdout(), "Current nodes: %d\nHierarchy roots: %d\nRoots: %d of %d (offset %d, limit %d, omitted %d, truncated %t)\nMultiple-parent nodes: %d\nHierarchy-cycle nodes: %d\nDangling targets: %d\nUnrooted nodes: %d\n", tree.Nodes, tree.HierarchyRoots, tree.Returned, tree.Total, tree.Offset, tree.Limit, tree.Omitted, tree.Truncated, tree.NodesWithMultipleParents, tree.HierarchyCycleNodes, len(tree.DanglingTargets), len(tree.UnrootedNodes))
	for _, node := range tree.Entries {
		writeRoadmapTreeNode(cmd, node)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Authority: %s\n%s\n", tree.Authority, tree.Disclaimer)
}

func writeRoadmapTreeNode(cmd *cobra.Command, node roadmap.RoadmapTreeNode) {
	marker := ""
	if node.Reference {
		marker = " -> reference"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%snode:%s@%d [%s] %s%s", strings.Repeat("  ", node.Depth), node.ID, node.Revision, node.Status, node.Title, marker)
	if len(node.Observations) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), " observations=%s", strings.Join(node.Observations, ","))
	}
	fmt.Fprintln(cmd.OutOrStdout())
}

func mcpCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Run the continuity MCP server over stdio",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withApp(cmd, func(application *app.App) error {
				if application.Config.Mode == "audit-only" {
					return errors.New("audit-only mode does not serve MCP tools; disable the full plugin/MCP for baseline sessions or select mode full")
				}
				return mcpserver.Run(cmd.Context(), application)
			})
		},
	}
}

func addEventName(body []byte, event string) ([]byte, error) {
	var payload map[string]any
	if len(body) == 0 {
		payload = map[string]any{}
	} else if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	if payload == nil {
		return nil, errors.New("hook payload must be a JSON object")
	}
	payload["hook_event_name"] = event
	return json.Marshal(payload)
}

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
