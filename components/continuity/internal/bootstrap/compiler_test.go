package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/instructions"
	"github.com/llewm/llewm/components/continuity/internal/pin"
)

func TestBuildIncludesLatestCheckpoint(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("# Rules\n\nRun tests."), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := database.Initialize(context.Background(), filepath.Join(root, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO projects(id, root, name, created_at) VALUES ('p', ?, 'test', '2026-01-01T00:00:00Z')`, root); err != nil {
		t.Fatal(err)
	}
	checkpoints := checkpoint.Service{DB: db}
	if _, err := checkpoints.Create(context.Background(), checkpoint.Checkpoint{ProjectID: "p", TaskID: "work", Task: "Continue work", Summary: "Half complete", NextAction: "Run tests"}); err != nil {
		t.Fatal(err)
	}
	packet, err := (Compiler{Checkpoints: checkpoints, MaxBytes: 10_000}).Build(context.Background(), "p", root, "test")
	if err != nil {
		t.Fatal(err)
	}
	got := Markdown(packet)
	for _, want := range []string{"Suggested conversation title: test — Continue work", "Continue work", "Run tests", "Active project instructions"} {
		if !strings.Contains(got, want) {
			t.Fatalf("bootstrap missing %q:\n%s", want, got)
		}
	}
	for _, budget := range []int{1, 600, 2000, 10_000, 0} {
		packet, err := (Compiler{Checkpoints: checkpoints, MaxBytes: budget}).Build(context.Background(), "p", root, "test")
		if budget == 1 || budget == 600 {
			if err == nil {
				t.Fatal("accepted an impossible budget")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if budget == 0 {
			budget = 12_000
		}
		body, err := json.MarshalIndent(packet, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if len(body)+1 > budget || len(Markdown(packet)) > budget {
			t.Fatalf("exceeded %d-byte budget", budget)
		}
	}
	if _, err := checkpoints.Create(context.Background(), checkpoint.Checkpoint{ProjectID: "p", TaskID: "large", Task: strings.Repeat("large", 3000), NextAction: "Review"}); err != nil {
		t.Fatal(err)
	}
	if _, err := (Compiler{Checkpoints: checkpoints, MaxBytes: 1000}).Build(context.Background(), "p", root, "test"); err == nil {
		t.Fatal("oversized checkpoint bypassed budget")
	}
	global := t.TempDir()
	if err := os.WriteFile(filepath.Join(global, "AGENTS.md"), []byte("# Commits\n\nUse descriptive commit messages."), 0o600); err != nil {
		t.Fatal(err)
	}
	compiler := Compiler{Checkpoints: checkpoints, GlobalRoot: global, MaxBytes: 30_000}
	packet, err = compiler.Build(context.Background(), "p", root, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(packet.Global) != 1 || len(packet.Rules) != 1 || packet.Truncated {
		t.Fatal("lost a guidance layer")
	}
	compiler.MaxBytes = 1000
	if _, err := compiler.Build(context.Background(), "p", root, "test"); err == nil {
		t.Fatal("silently dropped global or project guidance")
	}
	if _, err := checkpoints.Create(context.Background(), checkpoint.Checkpoint{ProjectID: "p", TaskID: "small", Task: "Small task", NextAction: "Review"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("# Project override\n"+strings.Repeat("project instruction ", 300)), 0o600); err != nil {
		t.Fatal(err)
	}
	compiler.MaxBytes = 2000
	if _, err := compiler.Build(context.Background(), "p", root, "test"); err == nil {
		t.Fatal("dropped project override to retain global defaults")
	}
}

func TestCheckpointReadinessPreflightAccountsWithoutPersisting(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("# Rules\n\nRun tests."), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := database.Initialize(context.Background(), filepath.Join(root, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO projects(id, root, name, created_at) VALUES ('p', ?, 'test', '2026-01-01T00:00:00Z')`, root); err != nil {
		t.Fatal(err)
	}
	checkpoints := checkpoint.Service{DB: db}
	prior, err := checkpoints.Create(context.Background(), checkpoint.Checkpoint{ProjectID: "p", TaskID: "work", Task: "Prior", NextAction: "Resolve predecessor"})
	if err != nil {
		t.Fatal(err)
	}
	candidate := checkpoint.Checkpoint{
		TaskID: "work", PredecessorCheckpointID: prior.ID, Task: "Continue work", Status: "in_progress", Summary: "Ready to continue", NextAction: "Run tests",
		Completed: []string{"Implemented preflight"}, Changes: []string{"internal/bootstrap/compiler.go"},
		Verified: []string{"Focused tests passed"}, EvidenceRefs: []string{"commit:fixture"},
		Attempts: []checkpoint.Attempt{{Description: "First approach", Outcome: "failure", Reason: "Too large"}},
	}
	result, err := (Compiler{Checkpoints: checkpoints, MaxBytes: 10_000}).Preflight(context.Background(), "p", root, "test", candidate)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Ready || result.JSONBytes <= 0 || result.MarkdownBytes <= 0 || result.ProjectGuidanceBytes <= 0 || result.CheckpointKernelBytes <= 0 || result.CheckpointDetailBytes <= 0 || result.UnresolvedActionBytes <= 0 || result.UnresolvedActionCount != 1 || len(result.LimitingFormats) != 0 {
		t.Fatalf("unexpected ready preflight: %#v", result)
	}
	if total := result.BaseBytes + result.GlobalGuidanceBytes + result.ProjectGuidanceBytes + result.CheckpointKernelBytes + result.CheckpointDetailBytes + result.UnresolvedActionBytes; total != result.MarkdownBytes {
		t.Fatalf("markdown accounting mismatch: total=%d result=%#v", total, result)
	}
	repeated, err := (Compiler{Checkpoints: checkpoints, MaxBytes: 10_000}).Preflight(context.Background(), "p", root, "test", candidate)
	if err != nil {
		t.Fatal(err)
	}
	if repeated.JSONBytes != result.JSONBytes || repeated.MarkdownBytes != result.MarkdownBytes {
		t.Fatalf("identical preflight byte accounting changed: first=%#v repeated=%#v", result, repeated)
	}
	result, err = (Compiler{Checkpoints: checkpoints, MaxBytes: 600}).Preflight(context.Background(), "p", root, "test", candidate)
	if err != nil {
		t.Fatal(err)
	}
	if result.Ready || result.OversizeByBytes <= 0 || len(result.LimitingFormats) == 0 {
		t.Fatalf("unexpected oversized preflight: %#v", result)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM checkpoints`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("preflight persisted a checkpoint: count=%d err=%v", count, err)
	}
}

func TestConversationTitleIsSingleLineAndBounded(t *testing.T) {
	got := conversationTitle("Llewm\nproject", strings.Repeat("task ", 40))
	if strings.Contains(got, "\n") {
		t.Fatalf("title contains a newline: %q", got)
	}
	if len([]rune(got)) != maxConversationTitleRunes || !strings.HasSuffix(got, "…") {
		t.Fatalf("title is not bounded to %d runes: %q", maxConversationTitleRunes, got)
	}
}

func TestReadyPreflightFitsSavedPacketWithNoticesAndCarriedActions(t *testing.T) {
	for _, notices := range []bool{false, true} {
		t.Run(fmt.Sprintf("notices=%v", notices), func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			db, err := database.Initialize(ctx, filepath.Join(root, "test.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec(`INSERT INTO projects(id,root,name,created_at) VALUES ('p',?,'test','2026-01-01')`, root); err != nil {
				t.Fatal(err)
			}
			checkpoints := checkpoint.Service{DB: db}
			prior, err := checkpoints.Create(ctx, checkpoint.Checkpoint{ProjectID: "p", TaskID: "task", Task: "Prior", NextAction: "Retain this unresolved work"})
			if err != nil {
				t.Fatal(err)
			}
			compiler := Compiler{Checkpoints: checkpoints, MaxBytes: 100_000, GlobalRoot: t.TempDir()}
			if notices {
				compiler.GrantCheck = func(context.Context, string) string { return strings.Repeat("standing grant ", 30) }
				compiler.WorkItemCheck = func(context.Context, string) string { return "24 open work items" }
				compiler.ForkCheck = func(context.Context, string, string) string { return "existing fork notice" }
				compiler.LeaseCheck = func(context.Context, string, string) string { return "existing lease notice" }
				compiler.ContractCheck = func(context.Context, string) string { return "existing contract notice" }
				compiler.Choose = func(_ context.Context, _, _ string, selection checkpoint.Selection) *Choice {
					if selection != (checkpoint.Selection{}) {
						return &Choice{Selection: selection, Rule: RuleExplicit}
					}
					return &Choice{Rule: RuleBranch, Detail: "its latest checkpoint was on the current branch `main`"}
				}
			}
			candidate := checkpoint.Checkpoint{ProjectID: "p", TaskID: "task", Task: "Continue work", Summary: "Ready", NextAction: "Continue qualification", PredecessorCheckpointID: prior.ID}
			readiness, err := compiler.Preflight(ctx, "p", root, "test", candidate)
			if err != nil || !readiness.Ready {
				t.Fatalf("preflight: %#v %v", readiness, err)
			}
			if len(readiness.ProjectedPredecessorActions) != 1 || readiness.ProjectedPredecessorActions[0].Status != "carried_forward" || readiness.ProjectedPredecessorActions[0].CarriedToCheckpoint == "" {
				t.Fatalf("preflight did not project persisted carry state: %#v", readiness)
			}
			if total := readiness.BaseBytes + readiness.GlobalGuidanceBytes + readiness.ProjectGuidanceBytes + readiness.CheckpointKernelBytes + readiness.CheckpointDetailBytes + readiness.UnresolvedActionBytes; total != readiness.MarkdownBytes {
				t.Fatalf("section costs=%d, markdown=%d", total, readiness.MarkdownBytes)
			}
			compiler.MaxBytes = max(readiness.JSONBytes, readiness.MarkdownBytes)
			if _, err := checkpoints.Create(ctx, candidate); err != nil {
				t.Fatal(err)
			}
			for _, selection := range []checkpoint.Selection{{}, {TaskID: "task"}} {
				packet, err := compiler.BuildSelected(ctx, "p", root, "test", selection)
				if err != nil {
					t.Fatalf("ready preflight rejected saved bootstrap: %v", err)
				}
				body, err := json.MarshalIndent(packet, "", "  ")
				if err != nil {
					t.Fatal(err)
				}
				if len(body)+1 > readiness.JSONBytes || len(Markdown(packet)) > readiness.MarkdownBytes {
					t.Fatalf("saved packet exceeds preflight: json=%d/%d markdown=%d/%d", len(body)+1, readiness.JSONBytes, len(Markdown(packet)), readiness.MarkdownBytes)
				}
			}
		})
	}
}

func TestBuildSeparatesUnresolvedPredecessorActions(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("# Rules\n\nRun tests."), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := database.Initialize(context.Background(), filepath.Join(root, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO projects(id,root,name,created_at) VALUES ('p',?,'test','2026-01-01')`, root); err != nil {
		t.Fatal(err)
	}
	s := checkpoint.Service{DB: db}
	if _, err := s.Create(context.Background(), checkpoint.Checkpoint{ProjectID: "p", TaskID: "work", Task: "Old", NextAction: "Finish old action"}); err != nil {
		t.Fatal(err)
	}
	old, err := s.LatestSelected(context.Background(), "p", checkpoint.Selection{TaskID: "work"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(context.Background(), checkpoint.Checkpoint{ProjectID: "p", TaskID: "work", PredecessorCheckpointID: old.ID, Task: "Current", NextAction: "Do current action"}); err != nil {
		t.Fatal(err)
	}
	packet, err := (Compiler{Checkpoints: s, MaxBytes: 10000}).Build(context.Background(), "p", root, "test")
	if err != nil {
		t.Fatal(err)
	}
	got := Markdown(packet)
	if !strings.Contains(got, "### Unresolved predecessor actions") || !strings.Contains(got, "Finish old action") || strings.Count(got, "Do current action") != 1 {
		t.Fatalf("actions not separated:\n%s", got)
	}
}

func TestBuildUsesFactualFallbackWithoutCheckpoint(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("# Rules\n\nRun tests."), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := database.Initialize(context.Background(), filepath.Join(root, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO projects(id,root,name,created_at) VALUES ('p',?,'test','2026-01-01')`, root); err != nil {
		t.Fatal(err)
	}
	event := activity.NewEvent("p", "codex", "sessionstart", "session")
	if err := (activity.Store{DB: db}).Append(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	packet, err := (Compiler{Checkpoints: checkpoint.Service{DB: db}, MaxBytes: 10_000}).BuildSelected(context.Background(), "p", root, "test", checkpoint.Selection{TaskID: "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if packet.SchemaVersion != 3 || packet.Checkpoint != nil || packet.Fallback == nil || packet.Fallback.State != "no_matching_checkpoint" || packet.Fallback.Selection != "task_id" || packet.Fallback.EventsObserved != 1 || len(packet.Fallback.RecentActivity) != 1 || packet.Fallback.Authority != "machine_observed" {
		t.Fatalf("unexpected fallback: %#v", packet)
	}
	got := Markdown(packet)
	for _, want := range []string{"Factual recovery fallback", "task intent or completion", event.ID, "checkpoint show --query"} {
		if !strings.Contains(got, want) {
			t.Fatalf("fallback missing %q:\n%s", want, got)
		}
	}
}

func TestMarkdownGolden(t *testing.T) {
	unsynced, _, _ := ResolveCoreRules(CoreFacts{})
	packet := Packet{SchemaVersion: 1, Project: "example", Truncated: true, CoreRules: unsynced,
		Checkpoint: &checkpoint.Checkpoint{ID: "checkpoint-1", CreatedAt: time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC), Task: "Continue scaffold", Status: "in_progress", Summary: "CLI verified", NextAction: "Test MCP", Completed: []string{"Implemented CLI"}, Changes: []string{"cmd/llewm/main.go"}, Verified: []string{"go test ./..."}, EvidenceRefs: []string{"event-1"}, Attempts: []checkpoint.Attempt{{Description: "Run without Git metadata", Outcome: "failure", Reason: "No project root"}}},
		Rules:      []instructions.Section{{Source: "AGENTS.md", HeadingPath: "Engineering", Content: "Run tests.", Hash: "fixture-hash", Precedence: 100}},
	}
	want, err := os.ReadFile("../../testdata/bootstrap/startup.md")
	if err != nil {
		t.Fatal(err)
	}
	if got := Markdown(packet); got != string(want) {
		t.Fatalf("bootstrap mismatch\nwant:\n%sgot:\n%s", want, got)
	}
	packet.Global = []instructions.Section{{Source: "global/AGENTS.md", HeadingPath: "Commits", Content: "Use descriptive commit messages.", Hash: "global-fixture-hash", Precedence: 50}}
	want, err = os.ReadFile("../../testdata/bootstrap/global-startup.md")
	if err != nil {
		t.Fatal(err)
	}
	if got := Markdown(packet); got != string(want) {
		t.Fatalf("global bootstrap mismatch\nwant:\n%sgot:\n%s", want, got)
	}
}

func TestMarkdownIncludesCheckpointTaskID(t *testing.T) {
	packet := Packet{SchemaVersion: 3, Project: "example", Checkpoint: &checkpoint.Checkpoint{
		ID: "checkpoint-1", TaskID: "task-1", Task: "Continue work", Status: "paused", Summary: "Partial edit", NextAction: "Finish edit",
	}}
	got := Markdown(packet)
	if !strings.Contains(got, "- Task: Continue work\n- Task ID: task-1\n- Status: paused\n") {
		t.Fatalf("bootstrap omitted stable task ID: %s", got)
	}
}

func TestMarkdownMarksGlobalSectionsReplacedByProjectOverride(t *testing.T) {
	packet := Packet{Project: "p",
		Global: []instructions.Section{
			{Source: "/g/AGENTS.md", HeadingPath: "Commit policy", Content: "GLOBAL COMMIT RULES", Hash: "g1"},
			{Source: "/g/AGENTS.md", HeadingPath: "Guidance precedence", Content: "GLOBAL PRECEDENCE", Hash: "g2"},
		},
		Rules: []instructions.Section{
			{Source: "/p/AGENTS.override.md", HeadingPath: "Commit policy", Content: "PROJECT COMMIT RULES", Hash: "p1"},
			{Source: "/p/AGENTS.md", HeadingPath: "Guidance precedence", Content: "PROJECT NOTE", Hash: "p2"},
		},
	}
	markdown := Markdown(packet)
	if strings.Contains(markdown, "GLOBAL COMMIT RULES") {
		t.Fatal("replaced global section still printed")
	}
	if !strings.Contains(markdown, "Project override: this global section is replaced by the section with the same heading in /p/AGENTS.override.md (sha256: p1)") {
		t.Fatalf("override not announced:\n%s", markdown)
	}
	if !strings.Contains(markdown, "PROJECT COMMIT RULES") {
		t.Fatal("override content missing")
	}
	// A same-named section in the ordinary AGENTS.md is not a replacement.
	if !strings.Contains(markdown, "GLOBAL PRECEDENCE") || strings.Count(markdown, "Project override:") != 1 {
		t.Fatalf("non-override heading treated as a replacement:\n%s", markdown)
	}
}

func TestPinsAreBoundedAndDroppedFirst(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("# Rules\n\nRun tests."), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := database.Initialize(context.Background(), filepath.Join(root, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO projects(id, root, name, created_at) VALUES ('p', ?, 'test', '2026-01-01T00:00:00Z')`, root); err != nil {
		t.Fatal(err)
	}
	checkpoints := checkpoint.Service{DB: db}
	if _, err := checkpoints.Create(context.Background(), checkpoint.Checkpoint{ProjectID: "p", Task: "Continue work", TaskID: "task-a", Summary: "Half complete", NextAction: "Run tests"}); err != nil {
		t.Fatal(err)
	}
	var askedTask string
	lines := make([]pin.Line, 12)
	for i := range lines {
		lines[i] = pin.Line{Ref: fmt.Sprintf("resource:host-%02d", i), Title: "A pinned host", Scope: "task:task-a"}
	}
	compiler := Compiler{Checkpoints: checkpoints, MaxBytes: 10_000, Pins: func(_ context.Context, _, taskID string) ([]pin.Line, error) {
		askedTask = taskID
		return lines, nil
	}}
	packet, err := compiler.Build(context.Background(), "p", root, "test")
	if err != nil {
		t.Fatal(err)
	}
	if askedTask != "task-a" || len(packet.Pins) != maxPinLines || packet.PinsOmitted != 4 {
		t.Fatalf("pins for %q: %d shown, %d omitted", askedTask, len(packet.Pins), packet.PinsOmitted)
	}
	markdown := Markdown(packet)
	if !strings.Contains(markdown, "## Pinned") || !strings.Contains(markdown, "- resource:host-00 — A pinned host (task:task-a)") || !strings.Contains(markdown, "- 4 more pinned; list them with llewm pin.") {
		t.Fatalf("pinned section:\n%s", markdown)
	}
	if strings.Index(markdown, "## Pinned") > strings.Index(markdown, "## Active project instructions") {
		t.Fatal("pins should come before project instructions")
	}

	// A budget that fits everything but the pins drops pins, not instructions.
	withoutPins := packet
	withoutPins.Pins, withoutPins.PinsOmitted = nil, len(lines)
	body, _ := json.MarshalIndent(withoutPins, "", "  ")
	compiler.MaxBytes = max(len(body)+1, len(Markdown(withoutPins))) + 1
	tight, err := compiler.Build(context.Background(), "p", root, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(tight.Pins) >= maxPinLines || tight.PinsOmitted <= 4 || len(tight.Rules) != 1 || tight.Truncated {
		t.Fatalf("tight budget: %d pins, %d omitted, %d rules, truncated %v", len(tight.Pins), tight.PinsOmitted, len(tight.Rules), tight.Truncated)
	}
}

func TestBriefsNameSlicesAndDropFirst(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("# Rules\n\nRun tests."), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := database.Initialize(context.Background(), filepath.Join(root, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO projects(id, root, name, created_at) VALUES ('p', ?, 'test', '2026-01-01T00:00:00Z')`, root); err != nil {
		t.Fatal(err)
	}
	checkpoints := checkpoint.Service{DB: db}
	if _, err := checkpoints.Create(context.Background(), checkpoint.Checkpoint{ProjectID: "p", Task: "Continue work", TaskID: "task-a", Summary: "Half complete", NextAction: "Choose 4e or 5b"}); err != nil {
		t.Fatal(err)
	}
	var askedNext string
	compiler := Compiler{Checkpoints: checkpoints, MaxBytes: 10_000, Briefs: func(_ context.Context, _, next string) []string {
		askedNext = next
		return []string{"4e squash [pending]: Report squash merges. (llewm brief 4e)", "5b fencing [pending]: Record lease tokens. (llewm brief 5b)", "x", "y", "z"}
	}}
	packet, err := compiler.Build(context.Background(), "p", root, "test")
	if err != nil {
		t.Fatal(err)
	}
	if askedNext != "Choose 4e or 5b" || len(packet.Briefs) != maxBriefLines {
		t.Fatalf("briefs for %q: %v", askedNext, packet.Briefs)
	}
	markdown := Markdown(packet)
	if !strings.Contains(markdown, "**Named slices (plan records):**\n- 4e squash [pending]: Report squash merges. (llewm brief 4e)\n") || strings.Index(markdown, "Named slices") < strings.Index(markdown, "- Next action: Choose 4e or 5b") {
		t.Fatalf("named slices:\n%s", markdown)
	}

	// Briefs are the first thing dropped when over budget.
	without := packet
	without.Briefs = nil
	body, _ := json.MarshalIndent(without, "", "  ")
	compiler.MaxBytes = max(len(body)+1, len(Markdown(without))) + 40
	tight, err := compiler.Build(context.Background(), "p", root, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(tight.Briefs) >= maxBriefLines || len(tight.Rules) != 1 || tight.Truncated {
		t.Fatalf("tight budget: %d briefs, %d rules, truncated %v", len(tight.Briefs), len(tight.Rules), tight.Truncated)
	}
}

func TestFitTrimsCoordinationNoticesBeforeFailing(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("# Rules\n\nRun tests."), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := database.Initialize(context.Background(), filepath.Join(root, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO projects(id,root,name,created_at) VALUES ('p',?,'test','2026-01-01')`, root); err != nil {
		t.Fatal(err)
	}
	checkpoints := checkpoint.Service{DB: db}
	if _, err := checkpoints.Create(context.Background(), checkpoint.Checkpoint{ProjectID: "p", TaskID: "work", Task: "Work", NextAction: "Continue"}); err != nil {
		t.Fatal(err)
	}
	long := "Since this task's last checkpoint, 9 commit(s) by others changed files it touched: " + strings.Repeat("abc1234 \"subject\" (task review): file.go; ", 80)
	compiler := Compiler{Checkpoints: checkpoints, MaxBytes: 3000, Coordination: func(context.Context, string, *checkpoint.Checkpoint) (string, string) {
		return long, strings.Repeat("shared.go ", 300)
	}}
	packet, err := compiler.Build(context.Background(), "p", root, "test")
	if err != nil {
		t.Fatalf("advisory notices failed the bootstrap: %v", err)
	}
	if packet.OverlapNotice != "" || packet.ChangeNotice != "Since this task's last checkpoint, 9 commit(s) by others changed files it touched. Review the log since that checkpoint before continuing." {
		t.Fatalf("notices not compacted: overlap=%q change=%q", packet.OverlapNotice, packet.ChangeNotice)
	}
}
