package worknarrative

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func commitClaims(verified string) []Claim {
	return []Claim{
		{Kind: "outcome", Text: "Changed source.", Authority: "caller_asserted"},
		{Kind: "interface", Text: "MCP API 82 -> 83.", Authority: "caller_asserted"},
		{Kind: "intent", Text: "Needed for the test.", Authority: "caller_asserted"},
		{Kind: "verification", Text: verified, Authority: "caller_asserted"},
		{Kind: "verification", Text: "- Earlier session: go vet passed", Authority: "user_reported"},
		{Kind: "limitation", Text: "None; the change is complete.\nSecond line.", Authority: "caller_asserted"},
	}
}

func TestRecordCommitWritesGitNote(t *testing.T) {
	ctx, service, projectID, root := testService(t)
	git := func(args ...string) string {
		t.Helper()
		output, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	git("init", "-q")
	git("config", "user.name", "t")
	git("config", "user.email", "t@example.invalid")
	git("add", ".")
	git("commit", "-q", "-m", "Initial")
	head := git("rev-parse", "HEAD")

	missing := Record{ProjectID: projectID, Title: "Change source", Creator: "test", Claims: []Claim{{Kind: "outcome", Text: "Changed.", Authority: "caller_asserted"}}}
	if _, _, err := service.RecordCommit(ctx, missing, "HEAD", "", false); err == nil || !strings.Contains(err.Error(), "missing: intent, verification, limitation") {
		t.Fatalf("missing claims accepted: %v", err)
	}

	record := Record{ProjectID: projectID, Title: "Change source", Creator: "test", Claims: commitClaims("go test ./... passes")}
	created, note, err := service.RecordCommit(ctx, record, "HEAD", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if created.Anchors[0].Target != head || note.Action != "added" || note.Commit != head || note.Ref != NotesRef {
		t.Fatalf("created = %+v note = %+v", created, note)
	}
	want := "# " + head + " — Change source\n\n## What changed\n- Changed source.\n\n## Interface changes\n- MCP API 82 -> 83.\n\n## Why\n- Needed for the test.\n\n## Verification (run in this session)\n- go test ./... passes\n\n## Verification (inherited or reported, not rerun)\n- Earlier session: go vet passed\n\n## Limitations and remaining work\n- None; the change is complete.\n  Second line.\n"
	if got := git("notes", "show", head) + "\n"; got != want {
		t.Fatalf("note =\n%s\nwant\n%s", got, want)
	}

	// The same claims again leave the note alone.
	if _, note, err = service.RecordCommit(ctx, record, "HEAD", "", false); err != nil || note.Action != "unchanged" {
		t.Fatalf("repeat: note = %+v err = %v", note, err)
	}
	// Different claims are appended as an amendment; the original note stays.
	if _, note, err = service.RecordCommit(ctx, Record{ProjectID: projectID, Title: "Change source", Creator: "test", Claims: commitClaims("go test ./... passes again")}, "HEAD", "", false); err != nil || note.Action != "appended" {
		t.Fatalf("amend: note = %+v err = %v", note, err)
	}
	amended := git("notes", "show", head)
	if !strings.HasPrefix(amended+"\n", want) || !strings.Contains(amended, "# Amendment from narrative 2026-09-20\n\n## What changed") || strings.Count(amended, "# "+head) != 1 || !strings.Contains(amended, "passes again") {
		t.Fatalf("amended note =\n%s", amended)
	}
	// skipNote records the narrative only.
	if err = os.WriteFile(filepath.Join(root, "source.md"), []byte("second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("commit", "-q", "-am", "Second")
	if _, note, err = service.RecordCommit(ctx, record, "HEAD", "", true); err != nil || note.Action != "skipped" {
		t.Fatalf("skip: note = %+v err = %v", note, err)
	}
	if output, err := exec.Command("git", "-C", root, "notes", "show", "HEAD").CombinedOutput(); err == nil {
		t.Fatalf("note written despite skip: %s", output)
	}
}
