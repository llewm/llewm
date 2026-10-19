package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/llewm/llewm/components/continuity/internal/app"
	"github.com/llewm/llewm/components/continuity/internal/history"
	"github.com/llewm/llewm/components/continuity/internal/noteimport"
	"github.com/llewm/llewm/components/continuity/internal/topic"
)

func historyCommand() *cobra.Command {
	parent := &cobra.Command{Use: "history", Short: "Search and expand narrative claims, decisions, work items, checkpoints, procedures, and resources"}

	var opts history.SearchOptions
	var searchOutput string
	search := &cobra.Command{Use: "search [TEXT]", Short: "Search bounded history metadata and snippets without loading bodies", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		format, err := outputFormat(searchOutput)
		if err != nil {
			return err
		}
		if len(args) == 1 {
			opts.Query = args[0]
		}
		return withReadOnlyApp(cmd, func(application *app.App) error {
			page, err := application.History.Search(cmd.Context(), application.ProjectID, opts)
			if err != nil {
				return err
			}
			if format == "json" {
				return writeJSON(cmd.OutOrStdout(), page)
			}
			writeHistorySearchText(cmd, page)
			return nil
		})
	}}
	search.Flags().StringVar(&opts.SourceURL, "source-url", "", "exact URL: source observations and claims citing them")
	search.Flags().StringVar(&opts.Topic, "topic", "", "curated topic ID or alias, or a roadmap node ID (includes descendants)")
	search.Flags().StringVar(&opts.State, "state", "current", "narrative/decision correction state: current, superseded, or all")
	search.Flags().StringSliceVar(&opts.Kinds, "kind", nil, "claim kind or record kind; may be repeated")
	search.Flags().StringSliceVar(&opts.Families, "family", nil, "narrative_claim, decision, work_item, checkpoint, procedure, or resource; may be repeated")
	search.Flags().StringVar(&opts.Since, "since", "", "inclusive RFC 3339 lower bound on recorded time")
	search.Flags().StringVar(&opts.Until, "until", "", "exclusive RFC 3339 upper bound on recorded time")
	search.Flags().StringVar(&opts.Linked, "linked", "", "target such as commit:HASH, roadmap_node:ID, or file:PATH: claims of narratives anchored to it and decisions linking to it")
	search.Flags().IntVar(&opts.Limit, "limit", history.DefaultSearchLimit, fmt.Sprintf("maximum hits (1-%d)", history.MaxSearchLimit))
	search.Flags().IntVar(&opts.Offset, "offset", 0, fmt.Sprintf("zero-based hit offset (0-%d)", history.MaxOffset))
	search.Flags().IntVar(&opts.MaxBytes, "max-bytes", history.DefaultMaxBytes, fmt.Sprintf("maximum result bytes (256-%d)", history.MaxMaxBytes))
	search.Flags().StringVar(&searchOutput, "output", "text", "text or json")

	var maxBytes int
	var showOutput string
	show := &cobra.Command{Use: "show REF...", Short: "Expand exact history references within a byte budget", Args: cobra.RangeArgs(1, history.MaxShowRefs), RunE: func(cmd *cobra.Command, args []string) error {
		format, err := outputFormat(showOutput)
		if err != nil {
			return err
		}
		return withReadOnlyApp(cmd, func(application *app.App) error {
			page, err := application.History.Show(cmd.Context(), application.ProjectID, args, maxBytes)
			if err != nil {
				return err
			}
			if format == "json" {
				return writeJSON(cmd.OutOrStdout(), page)
			}
			writeHistoryShowText(cmd, page)
			return nil
		})
	}}
	show.Flags().IntVar(&maxBytes, "max-bytes", history.DefaultMaxBytes, fmt.Sprintf("maximum expanded bytes (256-%d)", history.MaxMaxBytes))
	show.Flags().StringVar(&showOutput, "output", "text", "text or json")

	check := &cobra.Command{Use: "check", Short: "Compare canonical record counts with the derived history index", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return withReadOnlyApp(cmd, func(application *app.App) error {
			counts, err := application.History.Check(cmd.Context())
			if err != nil {
				return err
			}
			return writeHistoryCounts(cmd, counts)
		})
	}}
	reindex := &cobra.Command{Use: "reindex", Short: "Rebuild derived claim chunks and the history index from canonical records", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return withApp(cmd, func(application *app.App) error {
			counts, err := application.History.Reindex(cmd.Context())
			if err != nil {
				return err
			}
			return writeHistoryCounts(cmd, counts)
		})
	}}
	parent.AddCommand(search, show, check, reindex)
	return parent
}

func writeHistoryCounts(cmd *cobra.Command, counts []history.FamilyCount) error {
	w := cmd.OutOrStdout()
	drift := false
	for _, count := range counts {
		state := "ok"
		if count.Canonical != count.Indexed {
			state, drift = "drift", true
		}
		fmt.Fprintf(w, "%s\tcanonical=%d\tindexed=%d\t%s\n", count.Family, count.Canonical, count.Indexed, state)
	}
	if drift {
		return fmt.Errorf("history index differs from canonical records; run llewm history reindex")
	}
	return nil
}

func writeHistorySearchText(cmd *cobra.Command, page history.SearchPage) {
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "History: %d of %d candidates at offset %d (order %s, truncated %t, content_bytes=%d, bodies_loaded=%d)\n", page.Returned, page.Candidates, page.Options.Offset, page.Order, page.Truncated, page.ContentBytes, page.BodiesLoaded)
	if page.Topic != nil {
		fmt.Fprintf(w, "Topic: %s %s nodes=%s refs=%d truncated=%t\n", page.Topic.Kind, page.Topic.ID, strings.Join(page.Topic.Nodes, ","), page.Topic.Refs, page.Topic.Truncated)
	}
	for _, hit := range page.Hits {
		derivation := ""
		if len(hit.Derivations) > 0 {
			derivation = "\tvia=" + strings.Join(hit.Derivations, ",")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s%s\tstate=%s\n  %s\n", hit.Ref, hit.Kind, shortTime(hit.RecordedAt), singleLineText(hit.Title), derivation, hit.State, singleLineText(hit.Snippet))
		if len(hit.Successors) > 0 {
			fmt.Fprintf(w, "  Successors: %s (truncated %t)\n", strings.Join(hit.Successors, ", "), hit.SuccessorsTruncated)
		}
	}
	fmt.Fprintln(w, "Expand with: llewm history show REF...")
	fmt.Fprintln(w, page.Disclaimer)
}

func writeHistoryShowText(cmd *cobra.Command, page history.ShowPage) {
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "History show: %d items, content_bytes=%d/%d, truncated %t\n", len(page.Items), page.ContentBytes, page.MaxBytes, page.Truncated)
	for _, item := range page.Items {
		if item.Error != "" {
			fmt.Fprintf(w, "\n== %s\nerror: %s\n", item.Ref, item.Error)
			continue
		}
		fmt.Fprintf(w, "\n== %s (%s %s, %s)\n%s\n%s\n", item.Ref, item.Family, item.Kind, item.RecordedAt, item.Title, item.Text)
	}
	fmt.Fprintln(w, page.Disclaimer)
}

func topicCommand() *cobra.Command {
	parent := &cobra.Command{Use: "topic", Short: "Record curated history topics, aliases, and explicit topic links"}

	var value topic.Topic
	var aliases, links, accepts []string
	var linkStatus string
	record := &cobra.Command{Use: "record ID", Short: "Curate one topic: append a revision (--title), add aliases, append links, or accept proposed links", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		value.ID = args[0]
		if value.Title == "" && len(aliases) == 0 && len(links) == 0 && len(accepts) == 0 {
			return fmt.Errorf("give --title, --alias, --link, or --accept")
		}
		targets := make([]topic.Link, 0, len(links))
		for _, ref := range links {
			kind, id, ok := strings.Cut(ref, ":")
			if !ok || kind == "" || id == "" {
				return fmt.Errorf("--link %q must look like KIND:ID or KIND:ID@REVISION", ref)
			}
			link := topic.Link{TopicID: value.ID, TargetKind: kind, TargetID: id, Status: linkStatus, Creator: value.Creator, Model: value.Model, Authority: value.Authority}
			if kind != "file" {
				link.TargetID, link.TargetRevision, _ = strings.Cut(id, "@")
			}
			targets = append(targets, link)
		}
		return withApp(cmd, func(application *app.App) error {
			var result struct {
				Topic *topic.Topic `json:"topic,omitempty"`
				Links []topic.Link `json:"links,omitempty"`
			}
			if value.Title != "" {
				value.ProjectID = application.ProjectID
				created, err := application.Topics.Record(cmd.Context(), value)
				if err != nil {
					return err
				}
				result.Topic = &created
			}
			for _, alias := range aliases {
				if err := application.Topics.Alias(cmd.Context(), application.ProjectID, value.ID, alias, value.Creator); err != nil {
					return err
				}
			}
			for _, link := range targets {
				link.ProjectID = application.ProjectID
				created, err := application.Topics.AddLink(cmd.Context(), link)
				if err != nil {
					return err
				}
				result.Links = append(result.Links, created)
			}
			for _, id := range accepts {
				created, err := application.Topics.AcceptLink(cmd.Context(), application.ProjectID, id, value.Creator)
				if err != nil {
					return err
				}
				result.Links = append(result.Links, created)
			}
			return writeJSON(cmd.OutOrStdout(), result)
		})
	}}
	record.Flags().StringVar(&value.Title, "title", "", "topic title; appends a revision")
	record.Flags().StringVar(&value.Summary, "summary", "", "bounded topic summary")
	record.Flags().StringVar(&value.Status, "status", "", "revision: proposed, accepted, or retired (default accepted, or proposed with --model)")
	record.Flags().StringVar(&value.Creator, "creator", "", "creator identity (required)")
	record.Flags().StringVar(&value.Model, "model", "", "model that proposed the revision or links; keeps them proposed")
	record.Flags().StringVar(&value.Authority, "authority", "", "caller_asserted, user_reported, or model_suggested")
	record.Flags().StringSliceVar(&aliases, "alias", nil, "unique lowercase alias to add; may be repeated")
	record.Flags().StringSliceVar(&links, "link", nil, "link target as KIND:ID or KIND:ID@REVISION ("+strings.Join(topic.TargetKinds, ", ")+"); may be repeated")
	record.Flags().StringVar(&linkStatus, "link-status", "", "links: proposed, accepted, or retired (default accepted, or proposed with --model)")
	record.Flags().StringSliceVar(&accepts, "accept", nil, "proposed topic link ID to accept; may be repeated")

	var listOutput string
	list := &cobra.Command{Use: "list", Short: "List topics with aliases and link counts", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		format, err := outputFormat(listOutput)
		if err != nil {
			return err
		}
		return withReadOnlyApp(cmd, func(application *app.App) error {
			topics, err := application.Topics.List(cmd.Context(), application.ProjectID)
			if err != nil {
				return err
			}
			if format == "json" {
				return writeJSON(cmd.OutOrStdout(), topics)
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "Topics: %d\n", len(topics))
			for _, t := range topics {
				fmt.Fprintf(w, "topic:%s@%d\tstatus=%s\taliases=%s\taccepted_links=%d\tproposed_links=%d\ttitle=%s\n", t.ID, t.Revision, t.Status, strings.Join(t.Aliases, ","), t.AcceptedLinks, t.ProposedLinks, singleLineText(t.Title))
			}
			fmt.Fprintln(w, topic.Disclaimer)
			return nil
		})
	}}
	list.Flags().StringVar(&listOutput, "output", "text", "text or json")

	var showOutput string
	show := &cobra.Command{Use: "show TOPIC", Short: "Show a topic by ID or alias with its current links", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		format, err := outputFormat(showOutput)
		if err != nil {
			return err
		}
		return withReadOnlyApp(cmd, func(application *app.App) error {
			detail, err := application.Topics.Show(cmd.Context(), application.ProjectID, args[0])
			if err != nil {
				return err
			}
			if format == "json" {
				return writeJSON(cmd.OutOrStdout(), detail)
			}
			w := cmd.OutOrStdout()
			t := detail.Topic
			fmt.Fprintf(w, "Topic: topic:%s@%d\nTitle: %s\nStatus: %s\nAuthority: %s by %s\nAliases: %s\nSummary: %s\nLinks: %d (truncated %t)\n", t.ID, t.Revision, t.Title, t.Status, t.Authority, t.Creator, strings.Join(detail.Aliases, ", "), t.Summary, len(detail.Links), detail.LinksTruncated)
			for _, l := range detail.Links {
				revision := ""
				if l.TargetRevision != "" {
					revision = "@" + l.TargetRevision
				}
				fmt.Fprintf(w, "%s\t%s:%s%s\tstatus=%s\tauthority=%s\n", l.ID, l.TargetKind, l.TargetID, revision, l.Status, l.Authority)
			}
			fmt.Fprintln(w, detail.Disclaimer)
			return nil
		})
	}}
	show.Flags().StringVar(&showOutput, "output", "text", "text or json")
	parent.AddCommand(record, list, show)
	return parent
}

func narrativeImportCommand() *cobra.Command {
	var planOnly, apply bool
	var planSHA256, output string
	command := &cobra.Command{Use: "import-git-notes", Short: "Plan or apply a read-only import of sectioned Git commit notes as work narratives", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if planOnly == apply {
			return fmt.Errorf("choose exactly one of --plan or --apply")
		}
		if apply && planSHA256 == "" {
			return fmt.Errorf("--apply requires --plan-sha256 from a reviewed --plan")
		}
		format, err := outputFormat(output)
		if err != nil {
			return err
		}
		run := func(application *app.App) error {
			service := noteimport.Service{DB: application.DB, Root: application.Root, Narratives: application.Narratives}
			if planOnly {
				plan, err := service.Plan(cmd.Context(), application.ProjectID)
				if err != nil {
					return err
				}
				if format == "json" {
					return writeJSON(cmd.OutOrStdout(), plan)
				}
				writeImportPlanText(cmd, plan)
				return nil
			}
			result, err := service.Apply(cmd.Context(), application.ProjectID, planSHA256)
			if format == "json" {
				if writeErr := writeJSON(cmd.OutOrStdout(), result); writeErr != nil {
					return writeErr
				}
			} else {
				writeImportPlanText(cmd, result.Plan)
				fmt.Fprintf(cmd.OutOrStdout(), "Appended: %d narratives\n", len(result.Appended))
			}
			return err
		}
		if planOnly {
			return withReadOnlyApp(cmd, run)
		}
		return withApp(cmd, run)
	}}
	command.Flags().BoolVar(&planOnly, "plan", false, "read notes and report the import plan without writing")
	command.Flags().BoolVar(&apply, "apply", false, "append new and changed narratives from a reviewed plan")
	command.Flags().StringVar(&planSHA256, "plan-sha256", "", "plan hash from a reviewed --plan; apply refuses a changed plan")
	command.Flags().StringVar(&output, "output", "text", "text or json")
	return command
}

func writeImportPlanText(cmd *cobra.Command, plan noteimport.Plan) {
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "Git-note import plan: notes=%d new=%d changed=%d unchanged=%d narrated=%d unparseable=%d\n", plan.Notes, plan.Counts["new"], plan.Counts["changed"], plan.Counts["unchanged"], plan.Counts["narrated"], plan.Counts["unparseable"])
	fmt.Fprintf(w, "Plan SHA-256: %s\n", plan.PlanSHA256)
	for _, item := range plan.Items {
		switch item.Action {
		case "unparseable":
			fmt.Fprintf(w, "unparseable\t%s\t%s\n", item.Commit, item.Error)
		case "changed":
			fmt.Fprintf(w, "changed\t%s\t%s supersedes %s\n", item.Commit, item.NarrativeID, item.Supersedes)
		}
	}
	fmt.Fprintln(w, plan.Disclaimer)
}
