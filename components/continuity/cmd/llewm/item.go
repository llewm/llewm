package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/llewm/llewm/components/continuity/internal/app"
	"github.com/llewm/llewm/components/continuity/internal/friction"
	"github.com/llewm/llewm/components/continuity/internal/workitem"
)

func itemCommand() *cobra.Command {
	parent := &cobra.Command{Use: "item", Short: "Capture and review ideas, bugs, and tasks without changing active work"}
	parent.AddCommand(itemReportCommand())
	var capture workitem.Capture
	add := &cobra.Command{Use: "add", Short: "Capture one pending work item", Long: `Capture one pending idea, bug, task, or research question (the title). Capturing never changes the active
task, checkpoint, or roadmap state. --from work_item:ID promotes that item
(it is recorded as integrated into the new one); --from friction:ID tracks a
friction candidate.`, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return withApp(cmd, func(application *app.App) error {
			capture.ProjectID = application.ProjectID
			created, err := application.WorkItems.Capture(cmd.Context(), capture)
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), created)
		})
	}}
	add.Flags().StringVar(&capture.Kind, "kind", "idea", "idea, bug, task, or research")
	add.Flags().StringVar(&capture.ID, "id", "", "stable work item ID; generated when omitted")
	add.Flags().StringVar(&capture.Title, "title", "", "short title (required)")
	add.Flags().StringVar(&capture.Body, "body", "", "bounded body (required)")
	add.Flags().StringVar(&capture.Observed, "observed", "", "bug: observed behavior")
	add.Flags().StringVar(&capture.Reproduction, "reproduction", "", "bug: steps to reproduce")
	add.Flags().StringVar(&capture.Environment, "environment", "", "bug: environment (versions, platform)")
	add.Flags().StringVar(&capture.Severity, "severity", "", "bug: low, medium, high, or critical")
	add.Flags().StringSliceVar(&capture.Tags, "tag", nil, "tag; may be repeated")
	add.Flags().StringVar(&capture.TaskID, "task-id", "", "optional originating task ID")
	add.Flags().StringVar(&capture.CheckpointID, "checkpoint-id", "", "optional originating checkpoint ID")
	add.Flags().StringVar(&capture.RoadmapNodeID, "roadmap-node", "", "optional roadmap node ID")
	add.Flags().StringVar(&capture.From, "from", "", "work_item:ID to promote, or friction:ID to track")
	add.Flags().StringVar(&capture.Creator, "creator", "human", "creator identity claim")
	add.Flags().StringVar(&capture.Model, "model", "", "optional model identity claim")

	var options workitem.ListOptions
	var listOutput string
	var audit, showFriction bool
	var maxBytes int
	var frictionOptions friction.Options
	list := &cobra.Command{Use: "list", Short: "List work item metadata, a pending-item packet, or friction candidates", Long: `List work item metadata without loading bodies. --audit instead builds a
bounded packet of pending items with bodies for review. --friction instead
reports repeated failure/recovery shapes derived from recorded tool events,
with the work items that track each one.`, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if audit && showFriction {
			return fmt.Errorf("give at most one of --audit and --friction")
		}
		return withApp(cmd, func(application *app.App) error {
			format, err := outputFormat(listOutput)
			if err != nil {
				return err
			}
			switch {
			case audit:
				v, err := application.WorkItems.Audit(cmd.Context(), application.ProjectID, options.Kind, maxBytes)
				if err != nil {
					return err
				}
				if format == "json" {
					return writeJSON(cmd.OutOrStdout(), v)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Pending work items: %d included of %d (truncated %t)\n", v.IncludedCount, v.PendingCount, v.Truncated)
				return writeMarkdownBlock(cmd.OutOrStdout(), v.Markdown, v.Disclaimer)
			case showFriction:
				frictionOptions.MaxBytes = maxBytes
				v, err := application.Friction.Report(cmd.Context(), application.ProjectID, frictionOptions)
				if err != nil {
					return err
				}
				if format == "json" {
					return writeJSON(cmd.OutOrStdout(), v)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Friction candidates: %d included of %d (truncated %t)\n", v.IncludedCount, v.CandidateCount, v.Truncated)
				fmt.Fprintf(cmd.OutOrStdout(), "Events scanned: %d of %d (scan truncated %t)\n", v.EventsScanned, v.EventsTotal, v.ScanTruncated)
				return writeMarkdownBlock(cmd.OutOrStdout(), v.Markdown, v.Disclaimer)
			}
			page, err := application.WorkItems.ListPage(cmd.Context(), application.ProjectID, options)
			if err != nil {
				return err
			}
			if format == "json" {
				return writeJSON(cmd.OutOrStdout(), page)
			}
			writeItemListText(cmd, page)
			return nil
		})
	}}
	list.Flags().StringVar(&options.Kind, "kind", "", "idea, bug, task, or research")
	list.Flags().StringVar(&options.Status, "status", "", "open, pending, accepted, deferred, integrated, discarded, merged, or answered")
	list.Flags().StringVar(&options.Tag, "tag", "", "exact tag filter")
	list.Flags().StringVar(&options.TaskID, "task-id", "", "originating task filter")
	list.Flags().StringVar(&options.RoadmapNodeID, "roadmap-node", "", "roadmap node filter")
	list.Flags().IntVar(&options.Limit, "limit", 20, "maximum items (1-100)")
	list.Flags().BoolVar(&audit, "audit", false, "build a bounded packet of pending items with bodies")
	list.Flags().BoolVar(&showFriction, "friction", false, "report repeated failure/recovery candidates")
	list.Flags().IntVar(&maxBytes, "max-bytes", 8192, "--audit or --friction Markdown byte budget (512-32768)")
	list.Flags().IntVar(&frictionOptions.MinCount, "min-count", 2, "--friction: minimum occurrences (1-1000)")
	list.Flags().IntVar(&frictionOptions.Limit, "friction-limit", friction.DefaultLimit, "--friction: maximum candidates (1-100)")
	list.Flags().StringVar(&listOutput, "output", "text", "text or json")

	var showBytes int
	var showOutput string
	show := &cobra.Command{Use: "show ID", Short: "Show one work item as bounded Markdown", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return withApp(cmd, func(application *app.App) error {
			v, err := application.WorkItems.Show(cmd.Context(), application.ProjectID, args[0], showBytes)
			if err != nil {
				return err
			}
			if v == nil {
				return fmt.Errorf("work item %q not found in this project", args[0])
			}
			format, err := outputFormat(showOutput)
			if err != nil {
				return err
			}
			if format == "json" {
				return writeJSON(cmd.OutOrStdout(), v)
			}
			_, err = fmt.Fprint(cmd.OutOrStdout(), v.Markdown)
			return err
		})
	}}
	show.Flags().IntVar(&showBytes, "max-bytes", workitem.DefaultShowBytes, "Markdown byte budget (512-32768)")
	show.Flags().StringVar(&showOutput, "output", "text", "text or json")

	var disposition workitem.Disposition
	dispose := &cobra.Command{Use: "dispose ID", Short: "Append an explicit reviewed work item disposition", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return withApp(cmd, func(application *app.App) error {
			disposition.ProjectID = application.ProjectID
			disposition.ItemID = args[0]
			v, err := application.WorkItems.Dispose(cmd.Context(), disposition)
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), v)
		})
	}}
	dispose.Flags().StringVar(&disposition.Status, "status", "", "accepted, deferred, integrated, discarded, merged, or answered (required)")
	dispose.Flags().StringVar(&disposition.Reason, "reason", "", "review rationale (required)")
	dispose.Flags().StringSliceVar(&disposition.TargetRefs, "target-ref", nil, "target reference such as plan:ID/ITEM or work_item:ID; required for accepted, integrated, and merged; may be repeated")
	dispose.Flags().StringVar(&disposition.Creator, "creator", "human", "reviewer identity claim")
	dispose.Flags().StringVar(&disposition.Model, "model", "", "optional model identity claim")
	parent.AddCommand(add, list, show, dispose)
	return parent
}

func itemReportCommand() *cobra.Command {
	var options workitem.ReportOptions
	var output string
	command := &cobra.Command{Use: "report ID", Short: "Prepare a redacted bug report without submitting it", Long: `Prepare a bounded provider-neutral report from an existing bug. Only that bug's
selected fields and reporting-runtime version/platform diagnostics are included.
Expected behavior and hypothesis are report-only claims, not changes to the bug.
Pattern-based redaction does not replace review before sharing. No network
request, submission permission, delivery queue, or source update is created.`, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		format, err := outputFormat(output)
		if err != nil {
			return err
		}
		return withReadOnlyApp(cmd, func(application *app.App) error {
			report, err := application.WorkItems.Report(cmd.Context(), application.ProjectID, args[0], options)
			if err != nil {
				return err
			}
			if format == "json" {
				return writeJSON(cmd.OutOrStdout(), report)
			}
			_, err = io.WriteString(cmd.OutOrStdout(), report.Markdown())
			return err
		})
	}}
	command.Flags().StringVar(&options.Expected, "expected", "", "expected behavior (report-only caller claim)")
	command.Flags().StringVar(&options.Hypothesis, "hypothesis", "", "possible cause, explicitly unverified (report-only)")
	command.Flags().StringVar(&output, "output", "text", "text or json")
	return command
}

func writeMarkdownBlock(out io.Writer, markdown, disclaimer string) error {
	if _, err := io.WriteString(out, markdown); err != nil {
		return err
	}
	if !strings.HasSuffix(markdown, "\n") {
		fmt.Fprintln(out)
	}
	_, err := fmt.Fprintln(out, disclaimer)
	return err
}

func writeItemListText(cmd *cobra.Command, page workitem.ListPage) {
	fmt.Fprintf(cmd.OutOrStdout(), "Work items: %d of %d (limit %d, truncated %t)\n", len(page.Items), page.Total, page.Limit, page.Truncated)
	for _, value := range page.Items {
		tags := "none"
		if len(value.Tags) > 0 {
			tags = strings.Join(value.Tags, ",")
		}
		kind := value.Kind
		if value.Severity != "" {
			kind += "/" + value.Severity
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\tstatus=%s\trecorded=%s\ttags=%s\ttitle=%s\n", workitem.Ref(value.ID), kind, value.Status, value.RecordedAt.Format(time.RFC3339Nano), tags, singleLineText(value.Title))
	}
	fmt.Fprintln(cmd.OutOrStdout(), workitem.Disclaimer)
}
