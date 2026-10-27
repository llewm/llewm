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
