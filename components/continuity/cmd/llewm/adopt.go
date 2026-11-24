package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/adoption"
	"github.com/llewm/llewm/components/continuity/internal/app"
	"github.com/llewm/llewm/components/continuity/internal/config"
	"github.com/llewm/llewm/components/continuity/internal/instructions"
	"github.com/llewm/llewm/components/continuity/internal/redact"
	"github.com/spf13/cobra"
)

// Full adoption records the engineer's choice independently of the client
// installation, hook mode, and verification of a running client.
func adoptCommand(trialOnly bool) *cobra.Command {
	var reviewed, existing bool
	var sources []string
	var workflow string
	cmd := &cobra.Command{Use: "adopt", Short: "Review project instructions and record full adoption in an initialized project", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		root, err := projectRoot(cmd)
		if err != nil {
			return err
		}
		dir := config.StateDir(root)
		s, err := adoption.Load(dir)
		if err != nil {
			return err
		}
		phase := s.Phase()
		if phase == "exited" {
			return errors.New("project has a reviewed exit; inspect trial status")
		}
		if trialOnly && phase != "trial" {
			return errors.New("trial adopt requires an active trial")
		}
		if !reviewed {
			if trialOnly {
				return errors.New("review project control and use --reviewed to adopt")
			}
			sections, err := instructions.Discover(root)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Review project recordkeeping before adoption. Existing project instruction sections:")
			for _, section := range sections {
				fmt.Fprintf(cmd.OutOrStdout(), "  %s — %s\n", filepath.Base(section.Source), section.HeadingPath)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Compare roadmap, handoff and evidence instructions with the intended Llewm workflow. Show the engineer the source section, competing destinations, effect and proposed edit. Review client-specific, nested and referenced instructions that apply. Record the approved split in instructions the client loads even without hooks. Use --reviewed to confirm adoption, --existing for established use, and --guidance PATH --workflow TEXT to attest the guidance review.")
			return nil
		}
		if existing && (phase == "trial" || phase == "adopted" && s.History[0].Entry != "existing") {
			return errors.New("--existing confirms prior use only for a project without a trial or direct-adoption receipt")
		}
		if err := withReadOnlyApp(cmd, func(*app.App) error { return nil }); err != nil {
			return fmt.Errorf("open initialized project before adopting: %w", err)
		}
		var guidance *adoption.Guidance
		workflow = redact.String(strings.TrimSpace(workflow))
		if len(sources) > 0 || workflow != "" {
			if len(sources) == 0 || len(sources) > 16 || workflow == "" || len(workflow) > 512 || strings.ContainsAny(workflow, "\r\n") {
				return errors.New("guidance review needs 1–16 project instruction files and a one-line --workflow of at most 512 bytes")
			}
			for _, source := range sources {
				if len(source) > 256 || strings.ContainsAny(source, "\n\r") {
					return errors.New("invalid guidance path")
				}
			}
			files, err := adoption.Snapshot(root, sources, false)
			if err != nil {
				return err
			}
			for _, f := range files {
				if !f.Exists {
					return fmt.Errorf("reviewed guidance file missing: %s", f.Path)
				}
			}
			guidance = &adoption.Guidance{Sources: files, Workflow: workflow}
		}
		transition := adoption.Transition{State: "adopted", At: time.Now().UTC(), Verification: "user reviewed adoption", Guidance: guidance}
		switch phase {
		case "unmanaged":
			transition.Entry = "direct"
			if existing {
				transition.Entry = "existing"
			}
			paths := append([]string{"AGENTS.md", "AGENTS.override.md", "CLAUDE.md", "README.md", "docs/roadmap.md", "docs/current-plan.md"}, sources...)
			files, err := adoption.Snapshot(root, paths, true)
			if err != nil {
				return err
			}
			s = &adoption.State{Version: 1, Baseline: files}
		case "trial":
			transition.Entry = "trial"
		case "adopted":
			if guidance == nil || reflect.DeepEqual(s.History[len(s.History)-1].Guidance, guidance) {
				fmt.Fprintln(cmd.OutOrStdout(), "Full adoption already recorded.")
				return nil
			}
			transition.Verification = "user reviewed project recordkeeping guidance"
		}
		s.History = append(s.History, transition)
		if err := adoption.Save(dir, *s); err != nil {
			return err
		}
		_, notice := s.GuidanceStatus(root)
		fmt.Fprintf(cmd.OutOrStdout(), "Full adoption recorded. Recordkeeping guidance: %s. Run llewm doctor --client CLIENT to check project setup and bootstrap delivery.\n", notice)
		return nil
	}}
	cmd.Flags().BoolVar(&reviewed, "reviewed", false, "record the engineer's explicit full-adoption choice or updated guidance review")
	if !trialOnly {
		cmd.Flags().BoolVar(&existing, "existing", false, "confirm established use as of today")
	}
	cmd.Flags().StringArrayVar(&sources, "guidance", nil, "reviewed project-relative instruction file (repeatable)")
	cmd.Flags().StringVar(&workflow, "workflow", "", "engineer-reviewed summary of where this project records work")
	return cmd
}
