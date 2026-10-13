package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/llewm/llewm/components/continuity/internal/app"
	"github.com/llewm/llewm/components/continuity/internal/codestats"
	"github.com/llewm/llewm/components/continuity/internal/trackplan"
)

func statsCommand() *cobra.Command {
	parent := &cobra.Command{Use: "stats", Short: "Report project statistics computed without a model"}
	var by, since, until, revisions, output string
	var days, limit int
	var merges bool
	code := &cobra.Command{
		Use:   "code",
		Short: "Count lines added and removed from Git history, by language, extension, day, author, commit, directory, task, plan item, or roadmap node",
		Long: "Reads git log --numstat in every repository of the project. Merges are skipped unless --merges; generated, vendored, and lock files " +
			"(" + fmt.Sprint(codestats.DefaultExclude) + " and config.toml [stats] exclude) are counted apart as excluded. task and node come from " +
			"the anchors of narratives recorded for each commit (node also from plan items citing it); plan-item from plan items whose evidence cites commit:HASH. " +
			"A commit tied to several keys counts toward each.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			format, err := outputFormat(output)
			if err != nil {
				return err
			}
			options := codestats.Options{By: by, Range: revisions, IncludeMerges: merges, Limit: limit}
			if options.Since, err = usageTime(since); err != nil {
				return err
			}
			if options.Until, err = usageTime(until); err != nil {
				return err
			}
			if days > 0 {
				options.Since = time.Now().AddDate(0, 0, -days)
			}
			return withApp(cmd, func(application *app.App) error {
				options.Exclude = application.Config.Stats.Exclude
				changes, err := codestats.Collect(cmd.Context(), application.Repositories.All(), options)
				if err != nil {
					return err
				}
				var attribution map[string][]string
				if by == "task" || by == "plan-item" || by == "node" {
					plans, err := latestPlans(cmd, application)
					if err != nil {
						return err
					}
					commits := make([]string, 0, len(changes))
					for _, change := range changes {
						commits = append(commits, change.Commit)
					}
					if attribution, err = codestats.Attribution(cmd.Context(), application.DB, application.ProjectID, by, plans, commits); err != nil {
						return err
					}
				}
				report, err := codestats.Build(changes, options, attribution)
				if err != nil {
					return err
				}
				if format == "json" {
					return writeJSON(cmd.OutOrStdout(), report)
				}
				writeCodeStats(cmd, report)
				return nil
			})
		},
	}
	code.Flags().StringVar(&by, "by", "language", "language, extension, day, author, commit, directory, task, plan-item, or node")
	code.Flags().StringVar(&since, "since", "", "start (YYYY-MM-DD or RFC 3339)")
	code.Flags().StringVar(&until, "until", "", "end (YYYY-MM-DD or RFC 3339)")
	code.Flags().IntVar(&days, "days", 0, "the last N days (overrides --since)")
	code.Flags().StringVar(&revisions, "range", "", "Git revision range such as v1..HEAD")
	code.Flags().BoolVar(&merges, "merges", false, "count merge commits too")
	code.Flags().IntVar(&limit, "limit", 30, "maximum rows; 0 for all")
	code.Flags().StringVar(&output, "output", "text", "text or json")
	parent.AddCommand(code)
	return parent
}

// latestPlans loads the latest revision of every track plan.
func latestPlans(cmd *cobra.Command, application *app.App) ([]trackplan.Plan, error) {
	summaries, err := application.TrackPlans.ListPlans(cmd.Context(), application.ProjectID, trackplan.PlanListOptions{Limit: 100})
	if err != nil {
		return nil, err
	}
	plans := make([]trackplan.Plan, 0, len(summaries))
	for _, summary := range summaries {
		plan, err := application.TrackPlans.Plan(cmd.Context(), application.ProjectID, summary.ID, 0)
		if err != nil {
			return nil, err
		}
		if plan != nil {
			plans = append(plans, *plan)
		}
	}
	return plans, nil
}

func writeCodeStats(cmd *cobra.Command, report codestats.Report) {
	out := cmd.OutOrStdout()
	type line struct {
		key    string
		values [5]string
	}
	format := func(key string, row codestats.Row) line {
		return line{key, [5]string{fmt.Sprint(row.Commits), fmt.Sprint(row.Files), fmt.Sprintf("+%d", row.Added), fmt.Sprintf("-%d", row.Removed), fmt.Sprintf("%+d", row.Added-row.Removed)}}
	}
	lines := []line{{report.By, [5]string{"commits", "files", "added", "removed", "net"}}}
	for _, row := range report.Rows {
		lines = append(lines, format(row.Key, row))
	}
	if report.Unattributed != nil {
		lines = append(lines, format("(no record)", *report.Unattributed))
	}
	lines = append(lines, format("total", report.Total), format("excluded", report.Excluded))
	var keyWidth int
	var widths [5]int
	for _, value := range lines {
		keyWidth = max(keyWidth, len(value.key))
		for i, cell := range value.values {
			widths[i] = max(widths[i], len(cell))
		}
	}
	for _, value := range lines {
		fmt.Fprintf(out, "%-*s", keyWidth, value.key)
		for i, cell := range value.values {
			fmt.Fprintf(out, "  %*s", widths[i], cell)
		}
		fmt.Fprintln(out)
	}
	if report.Truncated {
		fmt.Fprintf(out, "Showing the first %d rows; --limit 0 shows all.\n", len(report.Rows))
	}
}
