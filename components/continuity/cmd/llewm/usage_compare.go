package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/llewm/llewm/components/continuity/internal/app"
	"github.com/llewm/llewm/components/continuity/internal/usage"
)

func usageCompareCommand() *cobra.Command {
	var baseline, candidate, output string
	cmd := &cobra.Command{
		Use: "compare", Short: "Compare two explicit local sessions; no inferred efficiency claim",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			format, err := outputFormat(output)
			if err != nil {
				return err
			}
			b, err := usage.ParseRunRef(baseline)
			if err != nil {
				return err
			}
			c, err := usage.ParseRunRef(candidate)
			if err != nil {
				return err
			}
			return withApp(cmd, func(application *app.App) error {
				result, err := usageReporter(application).Compare(cmd.Context(), b, c)
				if err != nil {
					return err
				}
				if format == "json" {
					return writeJSON(cmd.OutOrStdout(), result)
				}
				out := cmd.OutOrStdout()
				fmt.Fprintf(out, "Baseline: %s\nCandidate: %s\nDelta = candidate - baseline; negative means reduction.\n", baseline, candidate)
				for _, m := range result.Metrics {
					change := "n/a (zero baseline)"
					if m.ChangePercent != nil {
						change = fmt.Sprintf("%+.1f%%", *m.ChangePercent)
					}
					fmt.Fprintf(out, "%s\t%.6g → %.6g\tdelta %+.6g\t%s\n", m.Name, m.Baseline, m.Candidate, m.Delta, change)
				}
				for _, run := range []usage.Run{result.Baseline, result.Candidate} {
					fmt.Fprintf(out, "\n%s:%s\n", run.Ref.Client, run.Ref.Session)
					writeUsageReport(cmd, usage.Report{By: "model", Total: usage.Row{Tokens: run.Tokens}, Rows: run.Models, Tools: run.Tools, Prices: run.Prices})
				}
				for _, warning := range result.Warnings {
					fmt.Fprintln(out, "Warning:", warning)
				}
				fmt.Fprintln(out, result.Disclaimer)
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&baseline, "baseline", "", "baseline client:SESSION (claude-code or codex)")
	cmd.Flags().StringVar(&candidate, "candidate", "", "candidate client:SESSION (claude-code or codex)")
	cmd.Flags().StringVar(&output, "output", "text", "text or json")
	return cmd
}
