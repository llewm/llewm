package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/adoption"
	"github.com/llewm/llewm/components/continuity/internal/app"
	"github.com/llewm/llewm/components/continuity/internal/config"
	"github.com/llewm/llewm/components/continuity/internal/gitenv"
	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/redact"
	"github.com/spf13/cobra"
)

type handbackReview struct {
	Version           int                  `json:"version"`
	SnapshotSHA256    string               `json:"snapshot_sha256"`
	ReportSHA256      string               `json:"report_sha256"`
	MissingReferences []portable.Reference `json:"missing_references"`
}

func trialCommand() *cobra.Command {
	parent := &cobra.Command{Use: "trial", Short: "Try Continuity with prose in control, adopt explicitly, or prepare a reviewed handback"}
	var prose []string
	var period time.Duration
	start := &cobra.Command{Use: "start", Short: "Capture a redacted prose baseline before client setup; sync must be unconfigured", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		root, err := projectRoot(cmd)
		if err != nil {
			return err
		}
		cfg, err := config.Load(root)
		if err != nil {
			return err
		}
		if cfg.Sync.Remote != "" {
			return errors.New("trial requires History sync to be unconfigured; preserve existing settings and use a disposable demo instead")
		}
		if rel, e := filepath.Rel(root, config.StateDir(root)); e == nil && filepath.IsLocal(rel) {
			if e := gitenv.Command(cmd.Context(), "-C", root, "check-ignore", "-q", "--", filepath.Join(rel, "adoption.json")).Run(); e != nil {
				return fmt.Errorf("ignore %s in Git before starting a trial; no files changed", rel)
			}
		}
		if period <= 0 {
			return errors.New("review-after must be positive; it is a reminder, never automatic adoption or deletion")
		}
		paths := append([]string{"AGENTS.md", "AGENTS.override.md", "CLAUDE.md", "README.md", "docs/roadmap.md", "docs/current-plan.md", ".gitignore", ".mcp.json", ".claude/settings.json", ".claude/settings.local.json", ".codex/config.toml"}, prose...)
		if err = adoption.Start(config.StateDir(root), root, paths, time.Now().UTC().Add(period)); err != nil {
			return err
		}
		if err = initCommand().RunE(cmd, nil); err != nil {
			return err
		}
		if err = withApp(cmd, func(a *app.App) error { _, err := a.Portable.Initialize(cmd.Context(), a.ProjectID, ""); return err }); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Trial started. Existing project prose remains in control; automatic task bootstrap and History sync are disabled for this trial. No project prose or client configuration was changed. Record shared client setup separately. Run trial status to review, trial adopt to adopt, or trial prepare to leave.")
		return nil
	}}
	start.Flags().StringSliceVar(&prose, "prose", nil, "additional project-relative instruction/plan files to baseline (repeat or comma separate)")
	start.Flags().DurationVar(&period, "review-after", 14*24*time.Hour, "review reminder; no automatic transition")
	parent.AddCommand(start)
	parent.AddCommand(&cobra.Command{Use: "status", Short: "Show phase, baseline/current prose and review reminder", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		root, s, err := trialState(cmd)
		if err != nil {
			return err
		}
		current, err := adoption.Current(root, s)
		if err != nil {
			return err
		}
		return writeJSON(cmd.OutOrStdout(), map[string]any{"state": s, "phase": s.Phase(), "current": current, "review_due": time.Now().After(s.ReviewAfter), "shared_client_configuration": "not owned by the project trial; inspect plugin, hook, MCP, permission and global guidance changes separately"})
	}})
	var adoptReviewed bool
	adopt := &cobra.Command{Use: "adopt", Short: "Explicitly let Continuity supply task continuity; preserve prose and data", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		root, s, err := trialState(cmd)
		if err != nil {
			return err
		}
		if s.Phase() != "trial" {
			return errors.New("adoption requires an active trial")
		}
		if !adoptReviewed {
			return errors.New("review project control and use --reviewed to adopt; sync and shared setup remain separate")
		}
		s.History = append(s.History, adoption.Transition{State: "adopted", At: time.Now().UTC(), Verification: "user reviewed adoption"})
		if err = adoption.Save(config.StateDir(root), *s); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Adopted. Project instruction prose retains its instruction authority. Restart clients; verify exact checkpoint recovery. History sync remains a separate choice. The original prose baseline is retained for handback.")
		return nil
	}}
	adopt.Flags().BoolVar(&adoptReviewed, "reviewed", false, "confirm explicit adoption after reviewing project control")
	parent.AddCommand(adopt)
	var out string
	prepare := &cobra.Command{Use: "prepare", Short: "Write a new read-only review bundle with original/current prose and all curated records", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		root, s, err := trialState(cmd)
		if err != nil {
			return err
		}
		path, err := adoption.SafePath(root, out)
		if err != nil {
			return err
		}
		if _, err = os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			return errors.New("review output must not exist")
		}
		return withReadOnlyApp(cmd, func(a *app.App) error {
			snapshot, report, missing, err := trialReview(cmd, a, s)
			if err != nil {
				return err
			}
			if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				return err
			}
			stage, err := os.MkdirTemp(filepath.Dir(path), ".handback-*")
			if err != nil {
				return err
			}
			defer os.RemoveAll(stage)
			receipt, _ := json.MarshalIndent(handbackReview{Version: 1, SnapshotSHA256: snapshot, ReportSHA256: adoption.Digest(report), MissingReferences: missing}, "", "  ")
			if err = os.WriteFile(filepath.Join(stage, "review.md"), report, 0600); err != nil {
				return err
			}
			if err = os.WriteFile(filepath.Join(stage, "review.json"), receipt, 0600); err != nil {
				return err
			}
			if err = os.Rename(stage, path); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Review: %s/review.md\nSnapshot: %s\nMissing references: %d\nWrite a separate standalone prose handback, reconcile all differences and missing references, then disable client integrations and verify a fresh session without Continuity. Exit requires that explicit verification; this bundle is evidence, not adopted instructions.\n", out, snapshot, len(missing))
			return nil
		})
	}}
	prepare.Flags().StringVar(&out, "out", "docs/llewm-handback-review", "new project-relative review directory")
	parent.AddCommand(prepare)
	var review, handback, verification string
	var reviewed, writersStopped bool
	exit := &cobra.Command{Use: "exit", Short: "Disconnect this project after reviewing a current handback and verifying a fresh prose-only session", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		root, s, err := trialState(cmd)
		if err != nil {
			return err
		}
		if s.Phase() == "exited" {
			return errors.New("project already exited")
		}
		if !writersStopped {
			return errors.New("drain all project writers before exit, then attest --writers-stopped; the tool cannot prove processes are stopped")
		}
		if !reviewed || strings.TrimSpace(verification) == "" {
			return errors.New("requires --reviewed and --verification describing the observed fresh session with plugin/MCP/hooks/skills disabled and prose handback loaded")
		}
		reviewPath, err := adoption.SafePath(root, review)
		if err != nil {
			return err
		}
		rp, err := adoption.SafePath(root, filepath.Join(review, "review.json"))
		if err != nil {
			return err
		}
		b, err := os.ReadFile(rp)
		if err != nil {
			return err
		}
		var receipt handbackReview
		if err = json.Unmarshal(b, &receipt); err != nil {
			return err
		}
		reportPath, err := adoption.SafePath(root, filepath.Join(review, "review.md"))
		if err != nil {
			return err
		}
		report, err := os.ReadFile(reportPath)
		if err != nil {
			return err
		}
		if receipt.Version != 1 || adoption.Digest(report) != receipt.ReportSHA256 {
			return errors.New("review bundle changed; prepare a new review")
		}
		hp, err := adoption.SafePath(root, handback)
		if err != nil {
			return err
		}
		if hp == reviewPath || strings.HasPrefix(hp, reviewPath+string(filepath.Separator)) {
			return errors.New("handback must be a separate reviewed prose file, outside generated evidence")
		}
		files, err := adoption.Snapshot(root, []string{handback}, true)
		if err != nil {
			return err
		}
		if !files[0].Exists || strings.TrimSpace(files[0].Text) == "" {
			return errors.New("handback must be nonempty standalone prose")
		}
		err = withReadOnlyApp(cmd, func(a *app.App) error {
			snapshot, _, _, err := trialReview(cmd, a, s)
			if err != nil {
				return err
			}
			if snapshot != receipt.SnapshotSHA256 {
				return errors.New("project prose or curated knowledge changed since review; prepare and reconcile a new review")
			}
			return nil
		})
		if err != nil {
			return err
		}
		s.History = append(s.History, adoption.Transition{State: "exited", At: time.Now().UTC(), Review: review, Handback: handback, HandbackSHA256: files[0].SHA256, Verification: redact.String(verification)})
		if err = adoption.Save(config.StateDir(root), *s); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Project disconnected. Fresh-session verification is your attributed report, not machine-proven semantic completeness. Hooks are inert and new CLI/MCP opens are refused. Stop remaining old processes and restart clients. All project knowledge, backups and shared client configuration are retained; no prose was overwritten and no data was deleted.")
		return nil
	}}
	exit.Flags().StringVar(&review, "review", "", "review directory produced by trial prepare")
	exit.Flags().StringVar(&handback, "handback", "", "reviewed standalone prose file outside generated evidence")
	exit.Flags().StringVar(&verification, "verification", "", "observed fresh-session result with Continuity integrations disabled")
	exit.Flags().BoolVar(&writersStopped, "writers-stopped", false, "attest project writers are stopped or drained before final snapshot verification")
	exit.Flags().BoolVar(&reviewed, "reviewed", false, "confirm conflicts, missing references, integration changes and prose completeness reviewed")
	parent.AddCommand(exit)
	return parent
}
func trialState(cmd *cobra.Command) (string, *adoption.State, error) {
	root, err := projectRoot(cmd)
	if err != nil {
		return "", nil, err
	}
	s, err := adoption.Load(config.StateDir(root))
	if err != nil {
		return "", nil, err
	}
	if s == nil {
		return "", nil, errors.New("no trial receipt; start a trial before adoption, or use the documented manual handback for an existing project")
	}
	return root, s, nil
}
func trialReview(cmd *cobra.Command, a *app.App, s *adoption.State) (string, []byte, []portable.Reference, error) {
	current, err := adoption.Current(a.Root, s)
	if err != nil {
		return "", nil, nil, err
	}
	records, err := portable.LoadOperationalRecords(cmd.Context(), a.DB, a.ProjectID)
	if err != nil {
		return "", nil, nil, err
	}
	// Snapshot includes all curated records/revisions, missing refs, original/current
	// prose, phase and project identity. No per-request usage or transcript capture.
	raw, err := json.Marshal(struct {
		State   *adoption.State
		Current []adoption.File
		Records portable.OperationalRecordSet
	}{s, current, records})
	if err != nil {
		return "", nil, nil, err
	}
	var b bytes.Buffer
	b.WriteString("# Continuity handback evidence\n\nThis is evidence for human/agent reconciliation, not project instructions. Preserve concurrent changes. Do not blindly restore the original prose. Historical revisions below are not all current decisions.\n\nWrite standalone project prose naming current work, plans, decisions, unresolved issues, procedures and authoritative instructions. Resolve conflicts and missing references explicitly. Verify a fresh client session with Continuity plugin/MCP/hooks/skills disabled can continue from that prose. Inspect shared client settings and global guidance separately; project exit never removes them.\n\nOmissions: raw events, per-request usage, transcripts, credentials, global configuration bodies, and unreferenced external sources. Retained SQLite backups are the recovery source for device-private data. Redaction is pattern-based; inspect before sharing.\n\n")
	for i, f := range s.Baseline {
		fmt.Fprintf(&b, "## Prose: %s\n\nOriginal exists: %t; SHA256: %s\n\n", f.Path, f.Exists, f.SHA256)
		quoteProse(&b, f.Text)
		fmt.Fprintf(&b, "\nCurrent exists: %t; SHA256: %s; changed: %t\n\n", current[i].Exists, current[i].SHA256, current[i].Exists != f.Exists || current[i].SHA256 != f.SHA256)
		quoteProse(&b, current[i].Text)
	}
	fmt.Fprintf(&b, "\n## Curated knowledge\n\nProject: %s\n\nRecords (all revisions): %d\n\nMissing references: %d\n\n", a.ProjectID, len(records.Records), len(records.MissingReferences))
	for _, ref := range records.MissingReferences {
		fmt.Fprintf(&b, "- Missing %s:%s\n", ref.Kind, ref.ID)
	}
	for _, r := range records.Records {
		fmt.Fprintf(&b, "\n### %s:%s revision %d\n\nRecorded: %s; authority: %s\n\n", r.RecordType, r.RecordID, r.RecordRevision, r.RecordedAt.UTC().Format(time.RFC3339Nano), r.Authority)
		var value any
		if err = json.Unmarshal(r.Payload, &value); err != nil {
			return "", nil, nil, err
		}
		renderFields(&b, redact.Value(value), 0)
	}
	return adoption.Digest(raw), []byte(redact.String(b.String())), records.MissingReferences, nil
}
func quoteProse(b *bytes.Buffer, text string) {
	for _, line := range strings.Split(text, "\n") {
		fmt.Fprintf(b, "> %s\n", line)
	}
}
func renderFields(b *bytes.Buffer, value any, depth int) {
	indent := strings.Repeat("  ", depth)
	switch v := value.(type) {
	case map[string]any:
		keys := []string{}
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(b, "%s- %s:\n", indent, k)
			renderFields(b, v[k], depth+1)
		}
	case []any:
		for _, x := range v {
			fmt.Fprintf(b, "%s- Item:\n", indent)
			renderFields(b, x, depth+1)
		}
	default:
		for _, line := range strings.Split(fmt.Sprint(v), "\n") {
			fmt.Fprintf(b, "%s  %s\n", indent, line)
		}
	}
}
