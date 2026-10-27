package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestLogUsesNarrativesWithoutNotes(t *testing.T) {
	root := testProject(t)
	runCLI(t, "", "init")
	before := runCLI(t, "", "log", "-n", "1")
	if !strings.Contains(before, "commit ") || strings.Contains(before, "Narratives:") {
		t.Fatalf("unnarrated commit: %s", before)
	}
	claims := `[{"kind":"outcome","text":"Canonical change","authority":"caller_asserted"},{"kind":"intent","text":"Reason","authority":"caller_asserted"},{"kind":"verification","text":"Checked","authority":"caller_asserted"},{"kind":"limitation","text":"None","authority":"caller_asserted"}]`
	runCLI(t, "", "narrative", "record", "--id", "log-test", "--title", "Recorded change", "--commit", "HEAD", "--no-note", "--claims-json", claims)
	output := runCLI(t, "", "log", "-n", "1")
	for _, want := range []string{"Author:", "Date:", "Narratives:", "## What changed", "Canonical change", "## Verification (run in this session)"} {
		if !strings.Contains(output, want) {
			t.Fatalf("missing %q: %s", want, output)
		}
	}
	note := exec.Command("git", "-C", root, "notes", "show", "HEAD")
	if note.Run() == nil {
		t.Fatal("unexpected Git note")
	}
	if err, _ := runCLIError("log", "--max-count", "0"); err == nil {
		t.Fatal("accepted invalid limit")
	}
}

func TestLogFiltersAndCoverage(t *testing.T) {
	root := testProject(t)
	runCLI(t, "", "init")
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, output)
		}
	}
	git("-c", "user.name=Second Author", "-c", "user.email=second@example.invalid", "commit", "--allow-empty", "-m", "Second commit")
	claims := `[{"kind":"outcome","text":"Changed","authority":"caller_asserted"},{"kind":"intent","text":"Reason","authority":"caller_asserted"},{"kind":"verification","text":"Checked","authority":"caller_asserted"},{"kind":"limitation","text":"None","authority":"caller_asserted"}]`
	runCLI(t, "", "narrative", "record", "--title", "Second narrative", "--commit", "HEAD", "--no-note", "--claims-json", claims)
	compact := runCLI(t, "", "log", "--oneline", "--author", "Second Author")
	if !strings.Contains(compact, "Second commit") || strings.Contains(compact, "Narratives:") {
		t.Fatalf("compact: %s", compact)
	}
	expanded := runCLI(t, "", "log", "--oneline", "--expand", "--author", "Second Author")
	if !strings.Contains(expanded, "Second narrative") {
		t.Fatalf("expanded: %s", expanded)
	}
	missing := runCLI(t, "", "log", "--missing", "--oneline", "-n", "1")
	if strings.Contains(missing, "Second commit") || !strings.Contains(missing, "[no narrative]") {
		t.Fatalf("missing: %s", missing)
	}
	ranged := runCLI(t, "", "log", "--oneline", "HEAD~1..HEAD")
	if !strings.Contains(ranged, "Second commit") || len(strings.Split(strings.TrimSpace(ranged), "\n")) != 1 {
		t.Fatalf("range: %s", ranged)
	}
	path := runCLI(t, "", "log", "--oneline", "--path", "AGENTS.md")
	if strings.Contains(path, "Second commit") || strings.TrimSpace(path) == "" {
		t.Fatalf("path: %s", path)
	}
	if err, _ := runCLIError("log", "--expand"); err == nil {
		t.Fatal("accepted expand without oneline")
	}
}

func TestLogPagerWritesPlainTextToNonterminal(t *testing.T) {
	var out strings.Builder
	if err := pageLog(&out, "hello\n", false); err != nil || out.String() != "hello\n" {
		t.Fatalf("output %q: %v", out.String(), err)
	}
}
