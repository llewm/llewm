package main

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/llewm/llewm/components/continuity/internal/app"
	"github.com/llewm/llewm/components/continuity/internal/release"
)

func releaseCommand() *cobra.Command {
	parent := &cobra.Command{Use: "release", Short: "Record reviewed releases and inspect the deterministic changelog projection"}

	var value release.Record
	var entriesJSON, publishedAt string
	record := &cobra.Command{Use: "record", Short: "Append a draft, reviewed, published, or superseded release revision", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := json.Unmarshal([]byte(entriesJSON), &value.Entries); err != nil {
			return fmt.Errorf("decode entries JSON: %w", err)
		}
		if publishedAt != "" {
			parsed, err := time.Parse(time.RFC3339, publishedAt)
			if err != nil {
				return fmt.Errorf("published time must be RFC3339: %w", err)
			}
			value.PublishedAt = &parsed
		}
		return withApp(cmd, func(application *app.App) error {
			value.ProjectID = application.ProjectID
			created, err := application.Releases.Append(cmd.Context(), value)
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), created)
		})
	}}
	record.Flags().StringVar(&value.ID, "id", "", "stable release ID (use unreleased for the draft projection)")
	record.Flags().IntVar(&value.Revision, "revision", 0, "explicit revision; defaults to next")
	record.Flags().StringVar(&value.Version, "version", "", "displayed version (Unreleased for the draft)")
	record.Flags().StringVar(&value.Title, "title", "", "reviewed release title")
	record.Flags().StringVar(&value.Status, "status", "draft", "draft, reviewed, published, or superseded")
	record.Flags().StringVar(&value.Reviewer, "reviewer", "", "human reviewer for reviewed or published records")
	record.Flags().StringVar(&publishedAt, "published-at", "", "explicit RFC3339 publication time")
	record.Flags().StringVar(&entriesJSON, "entries-json", "[]", "ordered release entries as JSON")

	var id, path, output string
	var limit int
	view := &cobra.Command{Use: "view [unreleased|history|render|check]", Short: "Read release history or the deterministic draft changelog", Args: cobra.MaximumNArgs(1), ValidArgs: []string{"unreleased", "history", "render", "check"}, RunE: func(cmd *cobra.Command, args []string) error {
		kind := "unreleased"
		if len(args) == 1 {
			kind = args[0]
		}
		format, err := outputFormat(output)
		if err != nil {
			return err
		}
		return withApp(cmd, func(application *app.App) error {
			switch kind {
			case "unreleased":
				result, err := application.Releases.History(cmd.Context(), application.ProjectID, "unreleased", 1)
				return firstErr(err, func() error { return writeJSON(cmd.OutOrStdout(), result) })
			case "history":
				result, err := application.Releases.History(cmd.Context(), application.ProjectID, id, limit)
				return firstErr(err, func() error { return writeJSON(cmd.OutOrStdout(), result) })
			case "render":
				result, err := application.Releases.Render(cmd.Context(), application.ProjectID)
				if err != nil || format == "json" {
					return firstErr(err, func() error { return writeJSON(cmd.OutOrStdout(), result) })
				}
				_, err = io.WriteString(cmd.OutOrStdout(), result.Markdown)
				return err
			case "check":
				result, err := application.Releases.Check(cmd.Context(), application.ProjectID, path)
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
			return nil
		})
	}}
	view.Flags().StringVar(&id, "id", "", "release ID for history")
	view.Flags().StringVar(&path, "path", "CHANGELOG.md", "project-relative changelog path for check")
	view.Flags().IntVar(&limit, "limit", release.DefaultLimit, "maximum release revisions")
	view.Flags().StringVar(&output, "output", "text", "text or json")
	parent.AddCommand(record, view)
	return parent
}
