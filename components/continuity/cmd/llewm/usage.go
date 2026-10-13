package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/llewm/llewm/components/continuity/internal/app"
	"github.com/llewm/llewm/components/continuity/internal/memsync"
	"github.com/llewm/llewm/components/continuity/internal/usage"
)

// startBackgroundUsage launches a detached usage catch-up; tests replace it.
var startBackgroundUsage = func(executable, root string) error {
	return memsync.StartBackgroundCommand(executable, root, "usage", "ingest")
}

func usageReporter(application *app.App) usage.Reporter {
	return usage.Reporter{DB: application.DB, ProjectID: application.ProjectID, Prices: usage.Prices{Overrides: application.Config.Usage.Prices}}
}

// usageTime parses a date (YYYY-MM-DD, local midnight) or an RFC 3339 time.
func usageTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	if at, err := time.ParseInLocation("2006-01-02", value, time.Local); err == nil {
		return at, nil
	}
	at, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("time %q must be YYYY-MM-DD or RFC 3339", value)
	}
	return at, nil
}

// UsageOptions turns flag-style values into report options; days, when set,
// starts the period that many days ago.
func usageOptions(by, since, until, client string, days, limit int) (usage.Options, error) {
	options := usage.Options{By: by, Client: client, Limit: limit}
	var err error
	if options.Since, err = usageTime(since); err != nil {
		return options, err
	}
	if options.Until, err = usageTime(until); err != nil {
		return options, err
	}
	if days > 0 {
		options.Since = time.Now().AddDate(0, 0, -days)
	}
	return options, nil
}

func writeUsageReport(cmd *cobra.Command, report usage.Report) {
	out := cmd.OutOrStdout()
	tokens := func(t usage.Tokens) string {
		cost := "unpriced"
		if t.EstimatedCost > 0 || t.UnpricedTokens == 0 {
			cost = fmt.Sprintf("~$%.2f", t.EstimatedCost)
			if t.UnpricedTokens > 0 {
				cost += fmt.Sprintf(" + %s unpriced tokens", compactCount(t.UnpricedTokens))
			}
		}
		return fmt.Sprintf("%d req\tin %s\tcache write %s\tcache read %s\tout %s\thit %.0f%%\t%s", t.Requests, compactCount(t.Input), compactCount(t.CacheWrite), compactCount(t.CacheRead), compactCount(t.Output), 100*t.CacheHitRatio(), cost)
	}
	period := "all time"
	if report.Since != nil || report.Until != nil {
		from, to := "start", "now"
		if report.Since != nil {
			from = report.Since.Local().Format("2006-01-02 15:04")
		}
		if report.Until != nil {
			to = report.Until.Local().Format("2006-01-02 15:04")
		}
		period = from + " to " + to
	}
	fmt.Fprintf(out, "Usage (%s), by %s\n", period, report.By)
	fmt.Fprintf(out, "total\t%s\n", tokens(report.Total.Tokens))
	for _, row := range report.Rows {
		sessions := ""
		if row.Sessions > 0 && report.By != "session" {
			sessions = fmt.Sprintf(" (%d sessions)", row.Sessions)
		}
		fmt.Fprintf(out, "%s%s\t%s\n", row.Key, sessions, tokens(row.Tokens))
	}
	for _, row := range report.Tools {
		family := "\t" + row.Family
		if report.By == "family" {
			family = ""
		}
		avg := int64(0)
		if row.Calls > 0 {
			avg = row.ResultBytes / row.Calls
		}
		fmt.Fprintf(out, "%s%s\t%d calls\tresults %s bytes (~%s tokens)\tavg %s\tmax %s\t%d errors\n", row.Key, family, row.Calls, compactCount(row.ResultBytes), compactCount(row.EstimatedTokens), compactCount(avg), compactCount(row.MaxResultBytes), row.Errors)
	}
	for _, overhead := range report.Overhead {
		fmt.Fprintf(out, "session start prompt (%s, %d sessions): median %s, average %s, max %s tokens\n", overhead.Client, overhead.Sessions, compactCount(overhead.Median), compactCount(overhead.Average), compactCount(overhead.Max))
	}
	if len(report.Total.UnpricedModels) > 0 {
		fmt.Fprintf(out, "Unpriced models (set [usage.prices.\"MODEL\"] in config.toml): %s\n", strings.Join(report.Total.UnpricedModels, ", "))
	}
	for _, price := range report.Prices {
		fmt.Fprintf(out, "Price for %s (rate card %s, as of %s, %s): %s\n", price.Model, price.RateModel, price.AsOf, price.Basis, price.Source)
	}
	if report.Truncated {
		fmt.Fprintln(out, "More rows exist; raise --limit.")
	}
	fmt.Fprintln(out, report.Disclaimer)
}

// compactCount renders 1234567 as 1.2M.
func compactCount(value int64) string {
	switch {
	case value >= 1_000_000_000:
		return fmt.Sprintf("%.1fB", float64(value)/1e9)
	case value >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(value)/1e6)
	case value >= 10_000:
		return fmt.Sprintf("%.0fk", float64(value)/1e3)
	}
	return fmt.Sprintf("%d", value)
}

func usageIngester(ctx context.Context, application *app.App) usage.Ingester {
	return usage.Ingester{DB: application.DB, ProjectID: application.ProjectID, Roots: application.Repositories.CheckoutRoots(ctx)}
}

// hookUsage ingests the session's file at Stop, SessionEnd and PreCompact, bounded so
// a large transcript never holds the hook up; anything left is read by the
// next catch-up. At SessionStart it starts a background catch-up instead.
func hookUsage(ctx context.Context, application *app.App, client, eventType, session string) {
	switch eventType {
	case "stop", "sessionend", "precompact":
		bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		_, _ = usageIngester(bounded, application).Session(bounded, client, session)
	case "sessionstart":
		if executable, err := os.Executable(); err == nil {
			_ = startBackgroundUsage(executable, application.Root)
		}
	}
}

func usageCommand() *cobra.Command {
	parent := &cobra.Command{
		Use:   "usage",
		Short: "Read exact model usage and tool-result sizes from Claude Code and Codex session files",
		Long: "Usage comes from the clients' own session files (Claude Code transcripts, Codex rollouts): token counts per model request, " +
			"tool calls by name, and the byte size of each tool result. Only those fields are stored, never content, and they stay on this machine.",
	}
	var client, session, output string
	ingest := &cobra.Command{
		Use:   "ingest",
		Short: "Read new lines of this project's session files (all of them, or one session)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			format, err := outputFormat(output)
			if err != nil {
				return err
			}
			return withApp(cmd, func(application *app.App) error {
				ingester := usageIngester(cmd.Context(), application)
				var result usage.Result
				if session != "" {
					if client != usage.ClaudeCode && client != usage.Codex {
						return fmt.Errorf("--session needs --client %s or %s", usage.ClaudeCode, usage.Codex)
					}
					result, err = ingester.Session(cmd.Context(), client, session)
				} else {
					result, err = ingester.All(cmd.Context())
				}
				if err != nil {
					return err
				}
				if format == "json" {
					return writeJSON(cmd.OutOrStdout(), result)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Ingested %d files: %d usage records, %d requests, %d tool calls, %d tool results, %d parse issues.\n", result.Files, result.UsageRecords, result.Requests, result.Calls, result.Results, result.ParseIssues)
				return nil
			})
		},
	}
	ingest.Flags().StringVar(&client, "client", "", "claude-code or codex, with --session")
	ingest.Flags().StringVar(&session, "session", "", "ingest only this session's file")
	ingest.Flags().StringVar(&output, "output", "text", "text or json")
	var by, since, until, reportOutput string
	var days, limit int
	report := &cobra.Command{
		Use:   "report",
		Short: "Summarize exact token usage, estimated cost, tool-result sizes, and session start prompts",
		Long:  "Group by session, day, model, client, task (sessions attributed to the task of their checkpoints, by session ID or else by time), tool, or family (mcp, skill, cli, builtin). Cost uses built-in Anthropic list prices or config.toml [usage.prices].",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			format, err := outputFormat(reportOutput)
			if err != nil {
				return err
			}
			options, err := usageOptions(by, since, until, client, days, limit)
			if err != nil {
				return err
			}
			options.Session = session
			return withApp(cmd, func(application *app.App) error {
				result, err := usageReporter(application).Report(cmd.Context(), options)
				if err != nil {
					return err
				}
				if format == "json" {
					return writeJSON(cmd.OutOrStdout(), result)
				}
				writeUsageReport(cmd, result)
				return nil
			})
		},
	}
	report.Flags().StringVar(&by, "by", "model", "session, day, model, client, task, tool, or family")
	report.Flags().StringVar(&since, "since", "", "start (YYYY-MM-DD or RFC 3339)")
	report.Flags().StringVar(&until, "until", "", "end, exclusive (YYYY-MM-DD or RFC 3339)")
	report.Flags().IntVar(&days, "days", 0, "the last N days (overrides --since)")
	report.Flags().StringVar(&client, "client", "", "only claude-code or codex")
	report.Flags().StringVar(&session, "session", "", "only this session, with --client")
	report.Flags().IntVar(&limit, "limit", 30, "maximum rows (1-500)")
	report.Flags().StringVar(&reportOutput, "output", "text", "text or json")
	parent.AddCommand(ingest, report, usageCompareCommand())
	return parent
}
