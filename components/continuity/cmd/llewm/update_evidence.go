package main

import (
	"fmt"
	"runtime"

	"github.com/llewm/llewm/components/continuity/internal/updatecheck"
	"github.com/spf13/cobra"
)

func updateEvidenceCommand() *cobra.Command {
	var receipt, commit, target, repository, artifact, output string
	cmd := &cobra.Command{Use: "evidence", Short: "Compare an upstream verification receipt offline; never run tests or install", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		format, err := outputFormat(output)
		if err != nil {
			return err
		}
		report, err := updatecheck.Evidence(receipt, commit, target, repository, artifact)
		if err != nil {
			return err
		}
		if format == "json" {
			return writeJSON(cmd.OutOrStdout(), report)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Reported evidence: %s\nCommit: %s\nPlatform: %s\nPublisher authentication: %s\n%s\n", report.State, report.Commit, report.Target, report.PublisherAuthentication, report.Reason)
		return nil
	}}
	cmd.Flags().StringVar(&receipt, "receipt", "", "downloaded verification receipt JSON; omission reports missing evidence")
	cmd.Flags().StringVar(&commit, "commit", "", "exact selected full source commit (required)")
	cmd.Flags().StringVar(&target, "target", runtime.GOOS+"/"+runtime.GOARCH, "required native test OS/architecture")
	cmd.Flags().StringVar(&repository, "repository", "https://github.com/llewm/llewm", "reviewed source repository URL")
	cmd.Flags().StringVar(&artifact, "artifact", "", "binary bundle whose digest must match the receipt")
	cmd.Flags().StringVar(&output, "output", "text", "text or json")
	return cmd
}
