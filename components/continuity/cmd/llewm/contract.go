package main

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/llewm/llewm/components/continuity/internal/app"
	"github.com/llewm/llewm/components/continuity/internal/contracts"
)

func writeContractLine(out io.Writer, entry contracts.Entry) {
	where := fmt.Sprintf("%s:%d", entry.Path, entry.Line)
	if entry.State == contracts.Missing {
		where = entry.Path + " (not found)"
	}
	fmt.Fprintf(out, "%s\t%s\t%s\t%s\t%s\n", entry.ID, entry.Kind, entry.State, where, singleLineText(entry.Title))
}

func writeContractReport(cmd *cobra.Command, report contracts.Report) {
	out := cmd.OutOrStdout()
	for _, entry := range report.Contracts {
		writeContractLine(out, entry)
	}
	for _, problem := range report.Problems {
		fmt.Fprintf(out, "problem: %s\n", problem)
	}
	fmt.Fprintf(out, "Contracts: %d, drifted: %d. %s\n", len(report.Contracts), len(report.Drifted()), report.Disclaimer)
}

func contractCommand() *cobra.Command {
	parent := &cobra.Command{
		Use:   "contract",
		Short: "Read the project's contracts: marked sections of repository Markdown",
		Long: "A contract states a project shape (an axiom, directive, promise, or constraint) as a section of a Markdown file in the repository, " +
			"marked with <!-- continuity:contract ID --> before its heading and followed by kind:, scope:, expiry:, and review: lines. " +
			"The files are canonical. Continuity finds the sections, returns one exactly, and reports drift against the hashes it last accepted.",
	}
	var output string
	list := &cobra.Command{
		Use:   "list",
		Short: "List every contract with its kind, location, and drift state",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			format, err := outputFormat(output)
			if err != nil {
				return err
			}
			return withApp(cmd, func(application *app.App) error {
				report, err := application.Contracts.Check(cmd.Context(), application.ProjectID)
				if err != nil {
					return err
				}
				if format == "json" {
					return writeJSON(cmd.OutOrStdout(), report)
				}
				writeContractReport(cmd, report)
				return nil
			})
		},
	}
	show := &cobra.Command{
		Use:   "show ID",
		Short: "Show one contract's statement exactly as written",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			format, err := outputFormat(output)
			if err != nil {
				return err
			}
			return withApp(cmd, func(application *app.App) error {
				entry, err := application.Contracts.Show(cmd.Context(), application.ProjectID, args[0])
				if err != nil {
					return err
				}
				if format == "json" {
					return writeJSON(cmd.OutOrStdout(), entry)
				}
				out := cmd.OutOrStdout()
				fmt.Fprintf(out, "Contract: %s (%s)\nTitle: %s\nSource: %s:%d\nState: %s\nSHA-256: %s\n", entry.ID, entry.Kind, entry.Title, entry.Path, entry.Line, entry.State, valueOrUnknown(entry.SHA256))
				for label, value := range map[string]string{"Scope": entry.Scope, "Expiry": entry.Expiry, "Review": entry.Review} {
					if value != "" {
						fmt.Fprintf(out, "%s: %s\n", label, value)
					}
				}
				fmt.Fprintf(out, "\n%s\n", entry.Statement)
				return nil
			})
		},
	}
	var accept bool
	check := &cobra.Command{
		Use:   "check",
		Short: "Report contracts that changed, appeared, or vanished since last accepted; --accept records them",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			format, err := outputFormat(output)
			if err != nil {
				return err
			}
			return withApp(cmd, func(application *app.App) error {
				if accept {
					accepted, err := application.Contracts.Accept(cmd.Context(), application.ProjectID)
					if err != nil {
						return err
					}
					if format == "json" {
						return writeJSON(cmd.OutOrStdout(), map[string]any{"accepted": accepted})
					}
					for _, entry := range accepted {
						writeContractLine(cmd.OutOrStdout(), entry)
					}
					fmt.Fprintf(cmd.OutOrStdout(), "Accepted %d contract changes into the index.\n", len(accepted))
					return nil
				}
				report, err := application.Contracts.Check(cmd.Context(), application.ProjectID)
				if err != nil {
					return err
				}
				drifted := contracts.Report{SchemaVersion: report.SchemaVersion, Contracts: append([]contracts.Entry{}, report.Drifted()...), Problems: report.Problems, Disclaimer: report.Disclaimer}
				if format == "json" {
					return writeJSON(cmd.OutOrStdout(), drifted)
				}
				writeContractReport(cmd, drifted)
				return nil
			})
		},
	}
	check.Flags().BoolVar(&accept, "accept", false, "record the current hash of every drifted contract (and drop vanished ones) after reviewing them")
	for _, command := range []*cobra.Command{list, show, check} {
		command.Flags().StringVar(&output, "output", "text", "text or json")
		parent.AddCommand(command)
	}
	return parent
}
