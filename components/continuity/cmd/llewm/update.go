package main

import (
	"context"
	"fmt"
	"runtime"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/buildinfo"
	"github.com/llewm/llewm/components/continuity/internal/config"
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
	parent.AddCommand(check, updateConfigureCommand(), updateStatusCommand())
	return parent
}

func updateConfigureCommand() *cobra.Command {
	c := updatecheck.DefaultAutomaticConfig()
	cmd := &cobra.Command{Use: "configure", Short: "Configure engineer-wide opt-in session-start update checks", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		dir, err := config.GlobalDir()
		if err != nil {
			return err
		}
		previous, err := updatecheck.LoadAutomaticConfig(dir)
		if err != nil {
			return err
		}
		if !cmd.Flags().Changed("enabled") {
			c.Enabled = previous.Enabled
		}
		if !cmd.Flags().Changed("source-root") {
			c.SourceRoot = previous.SourceRoot
		}
		if !cmd.Flags().Changed("fetch") {
			c.Fetch = previous.Fetch
		}
		if !cmd.Flags().Changed("remote") {
			c.Remote = previous.Remote
		}
		if !cmd.Flags().Changed("branch") {
			c.Branch = previous.Branch
		}
		if !cmd.Flags().Changed("interval") {
			c.Interval = previous.Interval
		}
		if !cmd.Flags().Changed("timeout") {
			c.Timeout = previous.Timeout
		}
		if err := updatecheck.SaveAutomaticConfig(dir, c); err != nil {
			return err
		}
		return writeJSON(cmd.OutOrStdout(), c)
	}}
	cmd.Flags().BoolVar(&c.Enabled, "enabled", false, "enable session-start checks; false disables them")
	cmd.Flags().StringVar(&c.SourceRoot, "source-root", "", "absolute Llewm checkout, independent of active project")
	cmd.Flags().BoolVar(&c.Fetch, "fetch", false, "contact the selected configured remote branch")
	cmd.Flags().StringVar(&c.Remote, "remote", c.Remote, "configured source remote")
	cmd.Flags().StringVar(&c.Branch, "branch", c.Branch, "source branch for fetch")
	cmd.Flags().StringVar(&c.Interval, "interval", c.Interval, "minimum time between checks (1m to 720h)")
	cmd.Flags().StringVar(&c.Timeout, "timeout", c.Timeout, "check timeout (100ms to 10s)")
	return cmd
}

func updateStatusCommand() *cobra.Command {
	return &cobra.Command{Use: "status", Short: "Read automatic-check settings without checking or contacting a remote", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		dir, err := config.GlobalDir()
		if err != nil {
			return err
		}
		c, err := updatecheck.LoadAutomaticConfig(dir)
		if err != nil {
			return err
		}
		return writeJSON(cmd.OutOrStdout(), c)
	}}
}

func sessionStartUpdate(ctx context.Context) string {
	dir, err := config.GlobalDir()
	if err != nil {
		return "Llewm automatic update check unavailable: global configuration could not be resolved."
	}
	r, err := updatecheck.Automatic(ctx, dir, buildinfo.Current(), time.Now())
	if err != nil {
		return "Llewm automatic update check unavailable: inspect llewm update status and global update-check configuration."
	}
	return r.Line()
}
