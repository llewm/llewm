package main

import (
	"fmt"
	"io"
	"os"

	"github.com/llewm/llewm/components/continuity/internal/app"
	"github.com/llewm/llewm/components/continuity/internal/proseimport"
	"github.com/spf13/cobra"
)

func proseImportCommand() *cobra.Command {
	var paths []string
	var file string
	var apply bool
	var budget int
	var cursor string
	c := &cobra.Command{
		Use:   "import-prose",
		Short: "Snapshot selected prose or preview/apply an agent-interpreted tracks-and-plans bundle",
		Long: `With --sources, return a bounded page of redacted prose and exact source hashes.
Follow next_cursor with --cursor until complete is true; source files are unchanged.
The onboarding agent interprets these documents into a version-1 import bundle.
With --file, validate and preview the entire bundle; --apply commits it atomically.
Inferred plans follow existing review policy. This command edits no project docs,
calls no model, enables no sync and grants no permission to execute imported work.
See docs/prose-import.md for the bundle format and review/reconciliation workflow.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if (len(paths) > 0) == (file != "") || (apply && file == "") || (cursor != "" && len(paths) == 0) {
				return fmt.Errorf("choose --sources PATHS or --file BUNDLE [--apply]")
			}
			root, err := projectRoot(cmd)
			if err != nil {
				return err
			}
			if len(paths) > 0 {
				page, err := proseimport.Snapshot(root, paths, budget, cursor)
				if err != nil {
					return err
				}
				nextAction := "Interpret the complete sources into source-linked tracks and plans; preview --file BUNDLE (or --file - for stdin) before --apply. Imported interpretations follow existing review policy."
				if !page.Complete {
					nextAction = "Continue with the same --sources and --cursor NEXT_CURSOR until complete is true. These are chunks of the original files; do not skip large sources or split/edit project docs."
				}
				return writeJSON(cmd.OutOrStdout(), map[string]any{"version": 1, "sources": page.Sources, "documents": page.Documents, "complete": page.Complete, "next_cursor": page.NextCursor, "next_action": nextAction})
			}
			var input io.Reader = cmd.InOrStdin()
			if file != "-" {
				f, err := os.Open(file)
				if err != nil {
					return err
				}
				defer f.Close()
				input = f
			}
			b, err := io.ReadAll(io.LimitReader(input, proseimport.MaxBundleBytes+1))
			if err != nil {
				return err
			}
			bundle, err := proseimport.Decode(b)
			if err != nil {
				return err
			}
			return withApp(cmd, func(a *app.App) error {
				if err := a.CheckWriter(cmd.Context()); err != nil {
					return err
				}
				s := proseimport.Service{DB: a.DB, Root: a.Root, ProjectID: a.ProjectID, Plans: a.TrackPlans}
				r, err := s.Import(cmd.Context(), bundle, apply)
				if err != nil {
					return err
				}
				return writeJSON(cmd.OutOrStdout(), r)
			})
		},
	}
	c.Flags().StringSliceVar(&paths, "sources", nil, "explicit project-relative prose files; repeat or comma separate")
	c.Flags().IntVar(&budget, "max-bytes", 48<<10, "redacted text bytes per page (4 bytes..256 KiB, at most 64 KiB per chunk)")
	c.Flags().StringVar(&cursor, "cursor", "", "next_cursor from the previous source page; keep the same ordered --sources")
	c.Flags().StringVar(&file, "file", "", "agent-interpreted JSON bundle path, or - for stdin; preview unless --apply")
	c.Flags().BoolVar(&apply, "apply", false, "apply the validated batch atomically; same id/content is a no-op")
	return c
}
