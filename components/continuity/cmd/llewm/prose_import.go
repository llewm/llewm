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
	c := &cobra.Command{
		Use:   "import-prose",
		Short: "Snapshot selected prose or preview/apply an agent-interpreted tracks-and-plans bundle",
		Long: `With --sources, return bounded redacted prose and exact source hashes as JSON.
The onboarding agent interprets these documents into a version-1 import bundle.
With --file, validate and preview the entire bundle; --apply commits it atomically.
Inferred plans follow existing review policy. This command edits no project docs,
calls no model, enables no sync and grants no permission to execute imported work.
See docs/prose-import.md for the bundle format and review/reconciliation workflow.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if (len(paths) > 0) == (file != "") || (apply && file == "") {
				return fmt.Errorf("choose --sources PATHS or --file BUNDLE [--apply]")
			}
			root, err := projectRoot(cmd)
			if err != nil {
				return err
			}
			if len(paths) > 0 {
				docs, err := proseimport.Snapshot(root, paths, budget)
				if err != nil {
					return err
				}
				return writeJSON(cmd.OutOrStdout(), map[string]any{"version": 1, "documents": docs, "next_action": "Interpret these sources once into source-linked tracks and plan items; preview --file BUNDLE before --apply. Imported interpretations require review under the existing plan policy."})
			}
			f, err := os.Open(file)
			if err != nil {
				return err
			}
			defer f.Close()
			b, err := io.ReadAll(io.LimitReader(f, proseimport.MaxBundleBytes+1))
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
	c.Flags().IntVar(&budget, "max-bytes", 48<<10, "total source byte budget (maximum 256 KiB, 64 KiB per file)")
	c.Flags().StringVar(&file, "file", "", "agent-interpreted JSON bundle; preview unless --apply")
	c.Flags().BoolVar(&apply, "apply", false, "apply the validated batch atomically; same id/content is a no-op")
	return c
}
