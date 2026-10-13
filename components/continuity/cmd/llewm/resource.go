package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/llewm/llewm/components/continuity/internal/app"
	"github.com/llewm/llewm/components/continuity/internal/resource"
)

func resourceCommand() *cobra.Command {
	parent := &cobra.Command{Use: "resource", Short: "Record and read the hosts, repositories, and services the project works with (never credentials)"}

	var value resource.Resource
	var attributes []string
	var note resource.Note
	record := &cobra.Command{Use: "record ID", Short: "Append a resource revision, a state note, or both", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		revise := value.Kind != "" || value.Title != "" || len(attributes) > 0 || value.Summary != "" || value.Status != ""
		if !revise && note.Text == "" {
			return fmt.Errorf("give --kind and --title for a revision, --note for a state note, or both")
		}
		return withApp(cmd, func(application *app.App) error {
			result := map[string]any{}
			if revise {
				value.ID, value.ProjectID, value.Attributes = args[0], application.ProjectID, map[string]string{}
				for _, attribute := range attributes {
					key, text, ok := strings.Cut(attribute, "=")
					if !ok {
						return fmt.Errorf("--attr must be key=value, not %q", attribute)
					}
					value.Attributes[strings.TrimSpace(key)] = text
				}
				created, err := application.Resources.Record(cmd.Context(), value)
				if err != nil {
					return err
				}
				result["resource"] = created
			}
			if note.Text != "" {
				note.ResourceID, note.ProjectID, note.Creator = args[0], application.ProjectID, value.Creator
				added, err := application.Resources.AddNote(cmd.Context(), note)
				if err != nil {
					return err
				}
				result["note"] = added
			}
			return writeJSON(cmd.OutOrStdout(), result)
		})
	}}
	record.Flags().StringVar(&value.Kind, "kind", "", "host, repository, or service (required for a revision)")
	record.Flags().StringVar(&value.Title, "title", "", "resource title (required for a revision)")
	record.Flags().StringVar(&value.Summary, "summary", "", "bounded description")
	record.Flags().StringArrayVar(&attributes, "attr", nil, "attribute key=value; repeatable; never a credential")
	record.Flags().StringVar(&value.Status, "status", "", "proposed, accepted, or retired (default accepted, or proposed with --model)")
	record.Flags().StringVar(&value.Creator, "creator", "", "creator identity (required)")
	record.Flags().StringVar(&value.Model, "model", "", "model that proposed the revision; keeps it proposed")
	record.Flags().StringVar(&value.Authority, "authority", "", "revision authority: caller_asserted, user_reported, or model_suggested")
	record.Flags().StringVar(&note.Text, "note", "", "dated observation of the resource's state")
	record.Flags().StringVar(&note.Authority, "note-authority", "", "machine_observed, user_reported, or caller_asserted (default caller_asserted)")

	var revision, notes, limit int
	var kind, status, output string
	show := &cobra.Command{Use: "show [ID]", Short: "Show a resource and its recent notes, or list the latest revision of each", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		format, err := outputFormat(output)
		if err != nil {
			return err
		}
		return withApp(cmd, func(application *app.App) error {
			if len(args) == 0 {
				values, err := application.Resources.List(cmd.Context(), application.ProjectID, kind, status, limit)
				if err != nil {
					return err
				}
				if format == "json" {
					return writeJSON(cmd.OutOrStdout(), values)
				}
				for _, value := range values {
					fmt.Fprintf(cmd.OutOrStdout(), "resource:%s@%d\tkind=%s\tstatus=%s\ttitle=%s\n", value.ID, value.Revision, value.Kind, value.Status, singleLineText(value.Title))
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Resources: %d. %s\n", len(values), resource.Disclaimer)
				return nil
			}
			detail, err := application.Resources.Show(cmd.Context(), application.ProjectID, args[0], revision, notes)
			if err != nil {
				return err
			}
			if format == "json" {
				return writeJSON(cmd.OutOrStdout(), detail)
			}
			writeResourceText(cmd, detail)
			return nil
		})
	}}
	show.Flags().IntVar(&revision, "revision", 0, "exact revision; zero selects the latest")
	show.Flags().IntVar(&notes, "notes", resource.DefaultNotes, "most recent notes to show")
	show.Flags().StringVar(&kind, "kind", "", "list filter: host, repository, or service")
	show.Flags().StringVar(&status, "status", "", "list filter: proposed, accepted, or retired")
	show.Flags().IntVar(&limit, "limit", 50, "list limit (1-100)")
	show.Flags().StringVar(&output, "output", "text", "text or json")
	parent.AddCommand(record, show)
	return parent
}

func writeResourceText(cmd *cobra.Command, detail resource.Detail) {
	w := cmd.OutOrStdout()
	value := detail.Resource
	fmt.Fprintf(w, "Resource: resource:%s@%d\nKind: %s\nTitle: %s\nStatus: %s (%s)\n", value.ID, value.Revision, value.Kind, singleLineText(value.Title), value.Status, value.Authority)
	if value.Summary != "" {
		fmt.Fprintf(w, "Summary: %s\n", singleLineText(value.Summary))
	}
	keys := make([]string, 0, len(value.Attributes))
	for key := range value.Attributes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Fprintf(w, "%s: %s\n", key, singleLineText(value.Attributes[key]))
	}
	for _, note := range detail.Notes {
		fmt.Fprintf(w, "Note %s (%s): %s\n", note.ObservedAt.Format(time.RFC3339), note.Authority, singleLineText(note.Text))
	}
	if detail.NotesTruncated {
		fmt.Fprintln(w, "Older notes omitted; raise --notes to see more.")
	}
	fmt.Fprintln(w, resource.Disclaimer)
}
