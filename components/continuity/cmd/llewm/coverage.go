package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/llewm/llewm/components/continuity/internal/app"
	"github.com/llewm/llewm/components/continuity/internal/coverage"
)

func coverageCommand() *cobra.Command {
	var opts coverage.Options
	var output string
	var history bool
	command := &cobra.Command{
		Use:   "coverage",
		Short: "Show the requirement coverage matrix for clients and platforms",
		Long: "Requirements come from docs/requirements.md (marked sections); each cell records whether a " +
			"requirement is met on a client and platform, how, and on what evidence. Requirements without a " +
			"cell are unknown. With --requirement and --history, every recorded cell of that requirement.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			format, err := outputFormat(output)
			if err != nil {
				return err
			}
			return withApp(cmd, func(application *app.App) error {
				if history {
					if opts.Requirement == "" {
						return fmt.Errorf("--history requires --requirement")
					}
					cells, err := application.Coverage.History(cmd.Context(), application.ProjectID, opts.Requirement)
					if err != nil {
						return err
					}
					if format == "json" {
						return writeJSON(cmd.OutOrStdout(), cells)
					}
					instance, err := application.Coverage.LocalInstance(cmd.Context(), application.ProjectID)
					if err != nil {
						return err
					}
					for _, c := range cells {
						writeCoverageCell(cmd, c, instance)
					}
					return nil
				}
				report, err := application.Coverage.Report(cmd.Context(), application.ProjectID, opts)
				if err != nil {
					return err
				}
				stale, err := application.Coverage.StaleCells(cmd.Context(), application.ProjectID)
				if err != nil {
					return err
				}
				if format == "json" {
					return writeJSON(cmd.OutOrStdout(), map[string]any{"report": report, "stale": stale})
				}
				writeCoverageReport(cmd, report, stale)
				return nil
			})
		},
	}
	command.Flags().StringVar(&opts.Requirement, "requirement", "", "requirement ID")
	command.Flags().StringVar(&opts.Client, "client", "", "client name")
	command.Flags().StringVar(&opts.Platform, "platform", "", "platform")
	command.Flags().StringVar(&opts.Status, "status", "", "row status: unknown, unsupported, partial, supported, or failing")
	command.Flags().BoolVar(&history, "history", false, "with --requirement, list every recorded cell, newest first")
	command.Flags().IntVar(&opts.Offset, "offset", 0, "zero-based row offset")
	command.Flags().IntVar(&opts.Limit, "limit", 0, "maximum rows (default 50, maximum 200)")
	command.Flags().StringVar(&opts.RequirementsPath, "requirements", "", "requirements file relative to the project root (default "+coverage.DefaultRequirementsPath+")")
	command.Flags().StringVar(&output, "output", "text", "text or json")

	var cell coverage.Cell
	var refs []string
	record := &cobra.Command{
		Use:   "record",
		Short: "Append one coverage cell",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withApp(cmd, func(application *app.App) error {
				cell.ProjectID, cell.Refs = application.ProjectID, refs
				created, err := application.Coverage.Record(cmd.Context(), opts.RequirementsPath, cell)
				if err != nil {
					return err
				}
				return writeJSON(cmd.OutOrStdout(), created)
			})
		},
	}
	record.Flags().StringVar(&cell.RequirementID, "requirement", "", "requirement ID from the requirements file (required)")
	record.Flags().StringVar(&cell.Client, "client", "", "client name (required)")
	record.Flags().StringVar(&cell.Platform, "platform", "", "platform, such as linux, macos, windows, or any (required)")
	record.Flags().StringVar(&cell.ClientVersion, "client-version", "", "client version the observation covers")
	record.Flags().StringVar(&cell.Status, "status", "", "unsupported, partial, supported, or failing (required)")
	record.Flags().StringVar(&cell.Verification, "verification", "", "inferred, reported, observed, or tested (required)")
	record.Flags().StringVar(&cell.Implementation, "implementation", "", "how the requirement is implemented here")
	record.Flags().StringSliceVar(&refs, "ref", nil, "link: commit:HASH, file:PATH, narrative:ID, or a URL (repeatable)")
	record.Flags().StringVar(&cell.Creator, "creator", "human", "creator identity")
	record.Flags().StringVar(&opts.RequirementsPath, "requirements", "", "requirements file relative to the project root")
	command.AddCommand(record)
	return command
}

// coverageObserver names where a cell was observed: here, or the instance of
// the machine it synced from.
func coverageObserver(c coverage.Cell, local string) string {
	switch {
	case c.InstanceID == "":
		return "observer unknown"
	case c.InstanceID == local:
		return "this machine"
	}
	return "instance " + c.InstanceID
}

func writeCoverageCell(cmd *cobra.Command, c coverage.Cell, local string) {
	fmt.Fprintf(cmd.OutOrStdout(), "  %s/%s %s (%s; version %s; %s; %s)\n", c.Client, c.Platform, c.Status, c.Verification, valueOrUnknown(c.ClientVersion), c.RecordedAt.Format("2006-01-02"), coverageObserver(c, local))
	if c.Implementation != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "    how: %s\n", c.Implementation)
	}
	if len(c.Refs) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "    links: %s\n", strings.Join(c.Refs, ", "))
	}
}

func writeCoverageReport(cmd *cobra.Command, report coverage.Report, stale []coverage.Stale) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Requirements: %d of %d from %s\n", report.Returned, report.Total, report.RequirementsPath)
	for _, row := range report.Rows {
		fmt.Fprintf(out, "%s [%s] %s\n", row.ID, row.Status, row.Title)
		for _, c := range row.Cells {
			writeCoverageCell(cmd, c, report.InstanceID)
		}
	}
	for _, c := range report.Orphaned {
		fmt.Fprintf(out, "orphaned: %s\n", c.RequirementID)
		writeCoverageCell(cmd, c, report.InstanceID)
	}
	for _, s := range stale {
		fmt.Fprintf(out, "stale: %s on %s/%s verified at %s; last observed %s\n", s.Cell.RequirementID, s.Cell.Client, s.Cell.Platform, s.Cell.ClientVersion, s.Installed)
	}
	for _, p := range report.Problems {
		fmt.Fprintf(out, "problem: %s\n", p)
	}
	if report.Truncated {
		fmt.Fprintf(out, "More rows exist; raise --limit or use --offset.\n")
	}
	fmt.Fprintf(out, "%s\n", report.Disclaimer)
}

// coverageDoctorLine summarizes one detected client's coverage: how many
// requirements have a current cell in each status, and cells verified against
// a client version other than the one last observed. Gaps are warnings, not
// failures.
func coverageDoctorLine(ctx context.Context, application *app.App, client string) (status, detail string, err error) {
	report, err := application.Coverage.Report(ctx, application.ProjectID, coverage.Options{Client: client, Limit: coverage.MaxLimit})
	if err != nil {
		return "", "", err
	}
	stale, err := application.Coverage.StaleCells(ctx, application.ProjectID)
	if err != nil {
		return "", "", err
	}
	counts := map[string]int{}
	for _, row := range report.Rows {
		counts[row.Status]++
	}
	staleCount := 0
	for _, s := range stale {
		if s.Cell.Client == client {
			staleCount++
		}
	}
	status = "ok"
	if counts["failing"]+counts["unsupported"]+counts["unknown"]+staleCount+len(report.Problems) > 0 {
		status = "warning"
	}
	detail = fmt.Sprintf("%s: %d supported, %d partial, %d unsupported, %d failing, %d unknown of %d requirements; %d stale", client, counts["supported"], counts["partial"], counts["unsupported"], counts["failing"], counts["unknown"], report.Total, staleCount)
	if len(report.Problems) > 0 {
		detail += "; " + strings.Join(report.Problems, "; ")
	}
	return status, detail, nil
}
