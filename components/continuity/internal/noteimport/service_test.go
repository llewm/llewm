package noteimport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
)

func TestParseMapsObservedNoteStyles(t *testing.T) {
	commit := "1513acf96bbca95ea396d512b54b29b1db3fcd02"
	cases := map[string][]string{
		"# Commit 1513acf\n\n## What changed\n\nAdded X.\n\n## Why\n\nNeeded.\n\n## Design decisions\n\nChose Y.\n\n## Verification performed\n\nTests.\n\n## Known limitations and remaining work\n\nZ.\n\n## Post-commit amendment — 2026-09-26\n\nLater.":            {"outcome:What changed", "intent:Why", "decision:Design decisions", "verification:Verification performed", "limitation:Known limitations and remaining work", "note:Post-commit amendment — 2026-09-26"},
		"# Add reference cards\n\nPreamble text.\n\n## What changed and why\n\nBoth.\n\n## Failed attempts and recovery\n\nRetried.":                                                                                                                                    {"outcome:", "outcome:What changed and why", "alternative:Failed attempts and recovery"},
		"# 1513acf96bbca95ea396d512b54b29b1db3fcd02\n\n- What changed: Added.\n- Why: Needed.\n- Design: Chose.\n- Verification personally performed: Ran.\n- Limitations and remaining work: None.\n\n## Installed qualification amendment — 2026-09-26\n\nInstalled.": {"outcome:What changed", "intent:Why", "decision:Design", "verification:Verification personally performed", "limitation:Limitations and remaining work", "verification:Installed qualification amendment — 2026-09-26"},
		"Commit: 1513acf96bbca95ea396d512b54b29b1db3fcd02\n\nWhat changed:\n- Updated docs.\n- Result: kept.\n\nDesign decisions:\n- Kept gates.":                                                                                                                       {"outcome:What changed", "decision:Design decisions"},
		"# Why\n\nReason.\n\n# What changed\n\nChange.": {"intent:Why", "outcome:What changed"},
	}
	for note, want := range cases {
		claims, err := Parse(note, commit)
		if err != nil {
			t.Fatalf("%q: %v", note, err)
		}
		var got []string
		for _, claim := range claims {
			got = append(got, claim.Kind+":"+claim.Label)
		}
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Fatalf("note %q\n got %v\nwant %v", note, got, want)
		}
	}
	claims, _ := Parse("Commit: 1513acf\n\nWhat changed:\n- Updated docs.\n- Result: kept.", commit)
	if claims[0].Text != "- Updated docs.\n- Result: kept." {
		t.Fatalf("bullets with unknown labels must stay in their section: %q", claims[0].Text)
	}
	if _, err := Parse("# Commit deadbeef\n\n## Why\n\nX", commit); err == nil {
		t.Fatal("mismatched title hash accepted")
	}
	if _, err := Parse("\n\n", commit); err == nil {
		t.Fatal("empty note accepted")
	}
}

func git(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func TestPlanApplyIsIdempotentAndSupersedesChangedNotes(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	git(t, root, "init", "-q")
	write := func(name, body string) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("docs/cards.md", "cards\n")
	write("old.txt", "old\n")
	git(t, root, "add", ".")
	git(t, root, "commit", "-q", "-m", "Add reference cards")
	first := git(t, root, "rev-parse", "HEAD")
	git(t, root, "notes", "add", "-m", "# Commit "+first+"\n\n## Why\n\nFrequently requested reminders.\n\n## Design decisions\n\nSnapshot hashes.", first)
	write("docs/cards.md", "cards v2\n")
	git(t, root, "rm", "-q", "old.txt")
	git(t, root, "commit", "-q", "-am", "Revise cards")
	second := git(t, root, "rev-parse", "HEAD")
	git(t, root, "notes", "add", "-m", "- What changed: Revised.\n- Known limitations: None.", second)
	git(t, root, "commit", "-q", "--allow-empty", "-m", "Mismatched note")
	third := git(t, root, "rev-parse", "HEAD")
	git(t, root, "notes", "add", "-m", "# Commit 0000000\n\n## Why\n\nWrong hash.", third)

	db, err := database.Initialize(ctx, filepath.Join(t.TempDir(), "continuity.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	projectID, err := (activity.Store{DB: db}).EnsureProject(ctx, root, "test")
	if err != nil {
		t.Fatal(err)
	}
	service := Service{DB: db, Root: root, Narratives: worknarrative.Service{DB: db, Root: root}}
	plan, err := service.Plan(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Notes != 3 || plan.Counts["new"] != 2 || plan.Counts["unparseable"] != 1 {
		t.Fatalf("plan = %+v", plan.Counts)
	}
	byCommit := map[string]Item{}
	for _, item := range plan.Items {
		byCommit[item.Commit] = item
	}
	if item := byCommit[third]; !strings.Contains(item.Error, "does not match") {
		t.Fatalf("mismatch item = %+v", item)
	}
	firstItem := byCommit[first]
	sum := sha256.Sum256([]byte("cards\n"))
	anchors := firstItem.record.Anchors
	if firstItem.NarrativeID != "gitnote-"+first[:12] || firstItem.FileAnchors != 2 || anchors[0].Kind != "commit" || anchors[1].Target != "docs/cards.md" || anchors[1].ContentSHA256 != hex.EncodeToString(sum[:]) || firstItem.record.Title != "Add reference cards" {
		t.Fatalf("first item = %+v anchors %+v", firstItem, anchors)
	}
	if claims := byCommit[second].record.Claims; claims[len(claims)-1].Label != "Import" || !strings.Contains(claims[len(claims)-1].Text, "deleted paths without content anchors: 1") {
		t.Fatalf("deleted path not recorded: %+v", claims)
	}
	if _, err = service.Apply(ctx, projectID, "stale"); err == nil {
		t.Fatal("stale plan hash accepted")
	}
	result, err := service.Apply(ctx, projectID, plan.PlanSHA256)
	if err != nil || len(result.Appended) != 2 {
		t.Fatalf("apply = %+v %v", result, err)
	}
	var chunks int
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM history_chunks WHERE family='narrative_claim' AND ref LIKE 'narrative:gitnote-%'`).Scan(&chunks); err != nil || chunks != 5 {
		t.Fatalf("indexed claims = %d %v", chunks, err)
	}
	again, _ := service.Plan(ctx, projectID)
	if again.Counts["unchanged"] != 2 || again.Counts["new"] != 0 {
		t.Fatalf("replan = %+v", again.Counts)
	}
	git(t, root, "notes", "append", "-m", "## Installation amendment — 2026-09-26\n\nInstalled.", first)
	changed, _ := service.Plan(ctx, projectID)
	for _, item := range changed.Items {
		if item.Commit == first && (item.Action != "changed" || item.NarrativeID != "gitnote-"+first[:12]+"-r2" || item.Supersedes != "gitnote-"+first[:12]) {
			t.Fatalf("changed item = %+v", item)
		}
	}
	if _, err = service.Apply(ctx, projectID, changed.PlanSHA256); err != nil {
		t.Fatal(err)
	}
	final, _ := service.Plan(ctx, projectID)
	if final.Counts["unchanged"] != 2 {
		t.Fatalf("final = %+v", final.Counts)
	}
}

func TestPlanSkipsCommitsAlreadyNarrated(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	git(t, root, "init", "-q")
	var commits []string
	for _, subject := range []string{"Narrated directly", "Only a note"} {
		git(t, root, "commit", "-q", "--allow-empty", "-m", subject)
		commit := git(t, root, "rev-parse", "HEAD")
		git(t, root, "notes", "add", "-m", "## Why\n\nBecause.", commit)
		commits = append(commits, commit)
	}
	db, err := database.Initialize(ctx, filepath.Join(t.TempDir(), "continuity.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	projectID, err := (activity.Store{DB: db}).EnsureProject(ctx, root, "test")
	if err != nil {
		t.Fatal(err)
	}
	narratives := worknarrative.Service{DB: db, Root: root}
	if _, err = narratives.Append(ctx, worknarrative.Record{ProjectID: projectID, Title: "Narrated directly", Trigger: "commit", Creator: "claude-code",
		Claims:  []worknarrative.Claim{{Kind: "intent", Text: "Because.", Authority: "caller_asserted"}},
		Anchors: []worknarrative.Anchor{{Kind: "commit", Target: commits[0], Authority: "machine_observed"}}}); err != nil {
		t.Fatal(err)
	}
	service := Service{DB: db, Root: root, Narratives: narratives}
	plan, err := service.Plan(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	actions := map[string]string{}
	for _, item := range plan.Items {
		actions[item.Commit] = item.Action
	}
	if actions[commits[0]] != "narrated" || actions[commits[1]] != "new" {
		t.Fatalf("actions = %v", actions)
	}
	result, err := service.Apply(ctx, projectID, plan.PlanSHA256)
	if err != nil || len(result.Appended) != 1 || result.Appended[0] != "gitnote-"+commits[1][:12] {
		t.Fatalf("apply = %+v %v", result, err)
	}
	// The import itself does not count as a direct narrative on replan.
	if again, _ := service.Plan(ctx, projectID); again.Counts["narrated"] != 1 || again.Counts["unchanged"] != 1 {
		t.Fatalf("replan = %+v", again.Counts)
	}
}

func TestCheckComparesNotesWithDirectNarratives(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	git(t, root, "init", "-q")
	commit := func(message string) string {
		git(t, root, "commit", "-q", "--allow-empty", "-m", message)
		return git(t, root, "rev-parse", "HEAD")
	}
	base := commit("Base")
	both := commit("Both")
	git(t, root, "notes", "add", "-m", "## What changed\n\nX.\n\n## Why\n\nY.\n\n## Verification\n\nZ.", both)
	noteOnly := commit("Note only")
	git(t, root, "notes", "add", "-m", "## What changed\n\nX.", noteOnly)
	differ := commit("Differ")
	git(t, root, "notes", "add", "-m", "## What changed\n\nX.\n\n## Design decisions\n\nD.", differ)
	narrOnly := commit("Narrative only")
	imported := commit("Imported only")
	git(t, root, "notes", "add", "-m", "## What changed\n\nX.", imported)

	db, err := database.Initialize(ctx, filepath.Join(t.TempDir(), "continuity.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	projectID, err := (activity.Store{DB: db}).EnsureProject(ctx, root, "test")
	if err != nil {
		t.Fatal(err)
	}
	narratives := worknarrative.Service{DB: db, Root: root}
	record := func(id, creator, commit string, kinds ...string) {
		t.Helper()
		var claims []worknarrative.Claim
		for _, kind := range kinds {
			claims = append(claims, worknarrative.Claim{Kind: kind, Text: kind, Authority: "caller_asserted"})
		}
		if _, err := narratives.Append(ctx, worknarrative.Record{ID: id, ProjectID: projectID, Title: id, Trigger: "commit", Creator: creator, Claims: claims, Anchors: []worknarrative.Anchor{{Kind: "commit", Target: commit, Authority: "machine_observed"}}}); err != nil {
			t.Fatal(err)
		}
	}
	record("n-both", "agent", both, "outcome", "intent", "verification", "note")
	record("n-differ", "agent", differ, "outcome", "limitation")
	record("n-only", "agent", narrOnly, "outcome")
	record("gitnote-imported", Creator, imported, "outcome")

	report, err := Service{DB: db, Root: root}.Check(ctx, projectID, base)
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]CheckItem{}
	for _, item := range report.Items {
		states[item.Commit] = item
	}
	if len(report.Items) != 5 || states[both].State != "consistent" || states[noteOnly].State != "note_only" || states[narrOnly].State != "narrative_only" || states[imported].State != "note_only" {
		t.Fatalf("report = %+v", report.Items)
	}
	if item := states[differ]; item.State != "sections_differ" || strings.Join(item.MissingInNarr, ",") != "decision" || strings.Join(item.MissingInNote, ",") != "limitation" {
		t.Fatalf("differ = %+v", item)
	}
	if text := report.Text(); !strings.Contains(text, "consistent=1 sections_differ=1 note_only=2 narrative_only=1") || !strings.Contains(text, "narrative lacks decision note lacks limitation") {
		t.Fatalf("text = %s", text)
	}
}

// TestParseReadsRenderedCommitNotes keeps narrative-written notes readable by the
// importer and the note check: every section maps back to its claim kind.
func TestParseReadsRenderedCommitNotes(t *testing.T) {
	commit := "1513acf96bbca95ea396d512b54b29b1db3fcd02"
	claims := []worknarrative.Claim{
		{Kind: "outcome", Text: "Changed.", Authority: "caller_asserted"},
		{Kind: "interface", Text: "MCP API 82 -> 83.", Authority: "caller_asserted"},
		{Kind: "intent", Text: "Why.", Authority: "caller_asserted"},
		{Kind: "decision", Text: "Chose.", Authority: "caller_asserted"},
		{Kind: "verification", Text: "Ran.", Authority: "caller_asserted"},
		{Kind: "verification", Text: "Seen.", Authority: "machine_observed"},
		{Kind: "verification", Text: "Inherited.", Authority: "user_reported"},
		{Kind: "limitation", Text: "None.", Authority: "caller_asserted"},
		{Kind: "note", Text: "Aside.", Authority: "caller_asserted"},
	}
	parsed, err := Parse(worknarrative.RenderNote(commit, worknarrative.Record{Title: "Change 2026-10-01", Claims: claims}), commit)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, claim := range parsed {
		kinds = append(kinds, claim.Kind)
	}
	if got := strings.Join(kinds, ","); got != "outcome,interface,intent,decision,verification,verification,verification,limitation,note" {
		t.Fatalf("kinds = %s", got)
	}
}
