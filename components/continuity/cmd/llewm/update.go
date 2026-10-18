package main

import (
	"context"
	"fmt"
	"runtime"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/buildinfo"
	"github.com/llewm/llewm/components/continuity/internal/updatecheck"
	"github.com/spf13/cobra"
)

func updateCommand() *cobra.Command {
	parent := &cobra.Command{Use: "update", Short: "Check update targets and guide verified source/binary updates; never auto-migrate"}
	var source, manifest, artifact, remote, branch, target, output string
	var fetch bool
	check := &cobra.Command{Use: "check", Short: "Compare installed identity with a source checkout or distribution manifest", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		format, err := outputFormat(output)
		if err != nil {
			return err
		}
		if (source != "") == (manifest != "") {
			return fmt.Errorf("choose --source-root CHECKOUT or --manifest FILE")
		}
		if manifest != "" && fetch || source != "" && artifact != "" {
			return fmt.Errorf("--fetch applies only to source checks; --artifact only to manifest checks")
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
		defer cancel()
		var report updatecheck.Report
		if source != "" {
			report, err = updatecheck.Source(ctx, buildinfo.Current(), source, remote, branch, fetch)
		} else {
			report, err = updatecheck.Release(buildinfo.Current(), manifest, target, artifact)
		}
		if err != nil {
			return err
		}
		if format == "json" {
			return writeJSON(cmd.OutOrStdout(), report)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Method: %s\nState: %s\nInstalled: %s\nTarget: %s\nSource dirty: %t\nArtifact verified: %t\nPublisher authentication: %s\nNext: %s\n", report.Method, report.State, report.Installed.GitCommit, report.TargetCommit, report.SourceDirty, report.ArtifactVerified, report.PublisherAuthentication, report.NextAction)
		return nil
	}}
	check.Flags().StringVar(&source, "source-root", "", "explicit Llewm source checkout (no project store required)")
	check.Flags().BoolVar(&fetch, "fetch", false, "fetch selected configured remote branch; never checkout/merge/reset")
	check.Flags().StringVar(&remote, "remote", "origin", "configured source remote name for --fetch")
	check.Flags().StringVar(&branch, "branch", "main", "source branch for --fetch")
	check.Flags().StringVar(&manifest, "manifest", "", "publisher's distribution manifest JSON")
	check.Flags().StringVar(&artifact, "artifact", "", "downloaded platform bundle to verify against manifest")
	check.Flags().StringVar(&target, "target", runtime.GOOS+"/"+runtime.GOARCH, "binary platform in manifest")
	check.Flags().StringVar(&output, "output", "text", "text or json")
	parent.AddCommand(check)
	return parent
}
