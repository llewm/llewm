package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHistoryAndTopicCLI(t *testing.T) {
	root := testProject(t)
	runCLI(t, "", "init")
	path := filepath.Join(root, "decision.json")
	body := `{"id":"cards-reminders","title":"Keep reference cards as reminders","rationale":"Cards remain compact syntax reminders and never instructions.","disposition":"accepted","actor":"owner","actor_type":"human","authority":"caller_asserted","links":[{"kind":"subject","target_kind":"roadmap_node","target_id":"cards"}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	runCLI(t, "", "decision", "record", "--file", path)

	search := runCLI(t, "", "history", "search", "reminder*")
	for _, want := range []string{"History: 1 of 1 candidates at offset 0 (order relevance", "decision:cards-reminders", "[reminders]", "bodies_loaded=0", "llewm history show"} {
		if !strings.Contains(search, want) {
			t.Fatalf("search missing %q: %s", want, search)
		}
	}
	shown := runCLI(t, "", "history", "show", "decision:cards-reminders", "work_item:missing")
	for _, want := range []string{"== decision:cards-reminders (decision accepted", "never instructions", "Links: subject roadmap_node:cards", "error: work_item:missing not found"} {
		if !strings.Contains(shown, want) {
			t.Fatalf("show missing %q: %s", want, shown)
		}
	}
	if check := runCLI(t, "", "history", "check"); !strings.Contains(check, "decision\tcanonical=1\tindexed=1\tok") {
		t.Fatalf("check: %s", check)
	}
	if reindex := runCLI(t, "", "history", "reindex"); !strings.Contains(reindex, "checkpoint\tcanonical=") {
		t.Fatalf("reindex: %s", reindex)
	}

	runCLI(t, "", "topic", "record", "reference-cards", "--title", "Reference cards", "--creator", "owner")
	linked := runCLI(t, "", "topic", "record", "reference-cards", "--alias", "Cards", "--link", "decision:cards-reminders", "--creator", "owner")
	var recorded struct{ Links []struct{ Status string } }
	if err := json.Unmarshal([]byte(linked), &recorded); err != nil || len(recorded.Links) != 1 || recorded.Links[0].Status != "accepted" {
		t.Fatalf("link: %s %v", linked, err)
	}
	topics := runCLI(t, "", "topic", "list")
	if !strings.Contains(topics, "topic:reference-cards@1\tstatus=accepted\taliases=cards\taccepted_links=1") {
		t.Fatalf("topic list: %s", topics)
	}
	scoped := runCLI(t, "", "history", "search", "--topic", "cards", "--family", "decision")
	if !strings.Contains(scoped, "Topic: curated reference-cards") || !strings.Contains(scoped, "via=explicit_link") {
		t.Fatalf("topic search: %s", scoped)
	}
	if alias, format := runCLI(t, "", "history", "search", "reminder*", "--output", "json"), runCLI(t, "", "history", "search", "reminder*", "--output", "json"); alias != format {
		t.Fatal("JSON selector mismatch")
	}
}

func TestNarrativeImportGitNotesCLI(t *testing.T) {
	root := testProject(t)
	runCLI(t, "", "init")
	note := exec.Command("git", "-c", "user.name=t", "-c", "user.email=t@example.invalid", "notes", "add", "-m", "## Why\n\nCards should be compact reminders.\n\n## Design decisions\n\nSnapshot source hashes.", "HEAD")
	note.Dir = root
	if output, err := note.CombinedOutput(); err != nil {
		t.Fatalf("git notes: %v %s", err, output)
	}
	unreviewed := newRootCommand("llewm")
	unreviewed.SetArgs([]string{"narrative", "import-git-notes", "--apply"})
	if err := unreviewed.Execute(); err == nil || !strings.Contains(err.Error(), "--plan-sha256") {
		t.Fatalf("apply without a reviewed plan hash: %v", err)
	}
	plan := runCLI(t, "", "narrative", "import-git-notes", "--plan")
	if !strings.Contains(plan, "notes=1 new=1 changed=0 unchanged=0 narrated=0 unparseable=0") {
		t.Fatalf("plan: %s", plan)
	}
	hash := strings.TrimSpace(strings.SplitN(strings.SplitN(plan, "Plan SHA-256: ", 2)[1], "\n", 2)[0])
	applied := runCLI(t, "", "narrative", "import-git-notes", "--apply", "--plan-sha256", hash)
	if !strings.Contains(applied, "Appended: 1 narratives") {
		t.Fatalf("apply: %s", applied)
	}
	if again := runCLI(t, "", "narrative", "import-git-notes", "--plan"); !strings.Contains(again, "unchanged=1") {
		t.Fatalf("replan: %s", again)
	}
	found := runCLI(t, "", "history", "search", "--kind", "decision", "--family", "narrative_claim")
	if !strings.Contains(found, "#1\tdecision") || !strings.Contains(found, "Snapshot source hashes") {
		t.Fatalf("imported decision claim not searchable: %s", found)
	}
}

func TestNarrativeRecordWithCommitCLI(t *testing.T) {
	root := testProject(t)
	runCLI(t, "", "init")
	for _, args := range [][]string{{"config", "user.name", "t"}, {"config", "user.email", "t@example.invalid"}} {
		if output, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, output)
		}
	}
	created := runCLI(t, "", "narrative", "record", "--id", "commit-head", "--title", "Initialize", "--commit", "HEAD", "--claims-json", `[{"kind":"outcome","text":"Initialized.","authority":"caller_asserted"},{"kind":"intent","text":"Start the project.","authority":"caller_asserted"},{"kind":"verification","text":"Not applicable: nothing to check.","authority":"caller_asserted"},{"kind":"limitation","label":"Known limitations and remaining work","text":"None.","authority":"caller_asserted"}]`)
	for _, want := range []string{`"trigger": "commit"`, `"kind": "commit"`, `"target": "AGENTS.md"`, `"content_sha256"`, `"occurred_from"`, `"label": "Known limitations and remaining work"`, `"action": "added"`} {
		if !strings.Contains(created, want) {
			t.Fatalf("record missing %q: %s", want, created)
		}
	}
	note, err := exec.Command("git", "-C", root, "notes", "show", "HEAD").CombinedOutput()
	if err != nil || !strings.Contains(string(note), "— Initialize\n\n## What changed\n- Initialized.\n") || !strings.Contains(string(note), "## Verification (run in this session)\n- Not applicable") {
		t.Fatalf("note: %v %s", err, note)
	}
	found := runCLI(t, "", "history", "search", "--kind", "limitation")
	if !strings.Contains(found, "narrative:commit-head#3") {
		t.Fatalf("direct narrative not indexed: %s", found)
	}
}

func TestTrackPlanAcceptAndCompletionWarningCLI(t *testing.T) {
	root := testProject(t)
	runCLI(t, "", "init")
	runCLI(t, "", "checkpoint", "create", "--task", "Planned work", "--task-id", "planned-work", "--summary", "Work", "--next", "Finish")
	runCLI(t, "", "roadmap", "node", "--id", "planned", "--title", "Planned", "--status", "active", "--priority", "5", "--source-path", "AGENTS.md")
	runCLI(t, "", "roadmap", "link", "--kind", "task", "--node", "planned", "--task", "planned-work", "--reason", "Work", "--request-key", "bind-planned")
	if check := runCLI(t, "", "work", "check"); !strings.Contains(check, "missing_track_plan") {
		t.Fatalf("missing plan not reported: %s", check)
	}
	path := filepath.Join(root, "plan.json")
	if err := os.WriteFile(path, []byte(`{"id":"planned-plan","roadmap_node_id":"planned","task_id":"planned-work","title":"Planned","summary":"Two steps","status":"proposed","creator":"agent","model":"model-x","items":[{"id":"one","position":1,"title":"One","status":"completed"},{"id":"two","position":2,"title":"Two","status":"pending"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	runCLI(t, "", "track-plan", "record", "--file", path)
	if check := runCLI(t, "", "work", "check"); !strings.Contains(check, "track_plan_unreviewed") {
		t.Fatalf("unreviewed plan not reported: %s", check)
	}
	accepted := runCLI(t, "", "track-plan", "record", "planned-plan", "--creator", "owner")
	if !strings.Contains(accepted, `"revision": 2`) || !strings.Contains(accepted, `"creator": "owner"`) {
		t.Fatalf("accept: %s", accepted)
	}
	if check := runCLI(t, "", "work", "check"); strings.Contains(check, "track_plan") {
		t.Fatalf("reviewed plan still flagged: %s", check)
	}
	complete := newRootCommand("llewm")
	var stdout, stderr bytes.Buffer
	complete.SetOut(&stdout)
	complete.SetErr(&stderr)
	complete.SetArgs([]string{"roadmap", "node", "--id", "planned", "--title", "Planned", "--status", "completed", "--priority", "5", "--source-path", "AGENTS.md"})
	if err := complete.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "warning: track_plan_behind") || !strings.Contains(stdout.String(), `"status": "completed"`) {
		t.Fatalf("completion warning: stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
}
