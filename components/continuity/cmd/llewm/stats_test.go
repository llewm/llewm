package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestStatsCodeGroupsLinesByLanguageAndRecords(t *testing.T) {
	root := testProject(t)
	runCLI(t, "", "init")
	commit := func(message string, files map[string]string) string {
		t.Helper()
		for name, body := range files {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		for _, args := range [][]string{{"add", "-A"}, {"-c", "user.name=Ada", "-c", "user.email=ada@example.invalid", "commit", "-q", "-m", message}, {"rev-parse", "HEAD"}} {
			command := exec.Command("git", args...)
			command.Dir = root
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("git %v: %v: %s", args, err, output)
			}
			if args[0] == "rev-parse" {
				return strings.TrimSpace(string(output))
			}
		}
		return ""
	}
	code := commit("Add main", map[string]string{"main.go": "package main\n\nfunc main() {}\n", "go.sum": "a v1\nb v1\n"})
	commit("Add guide", map[string]string{"docs/guide.md": "# Guide\n\nRead me.\n"})

	if got, want := runCLI(t, "", "stats", "code"), `language    commits  files  added  removed  net
Markdown          2      3     +9       -0   +9
Go                1      1     +3       -0   +3
.gitignore        1      1     +1       -0   +1
total             3      5    +13       -0  +13
excluded          1      1     +2       -0   +2
`; got != want {
		t.Fatalf("by language:\n%s\nwant:\n%s", got, want)
	}

	plan := `{"id":"code-plan","roadmap_node_id":"code-node","title":"Code","summary":"Code","status":"proposed","creator":"human","items":[{"id":"main","position":1,"title":"Main","status":"completed","evidence_refs":["commit:` + code[:9] + `"]}]}`
	if err := os.WriteFile(filepath.Join(root, "plan.json"), []byte(plan), 0o600); err != nil {
		t.Fatal(err)
	}
	runCLI(t, "", "track-plan", "record", "--file", filepath.Join(root, "plan.json"))
	if err := os.Remove(filepath.Join(root, "plan.json")); err != nil {
		t.Fatal(err)
	}
	if got := runCLI(t, "", "stats", "code", "--by", "plan-item"); !strings.Contains(got, "code-plan/main Main        1      1     +3       -0   +3") || !strings.Contains(got, "(no record)") {
		t.Fatalf("by plan item:\n%s", got)
	}
	runCLI(t, "", "narrative", "record", "--commit", code, "--no-note", "--title", "Main", "--creator", "ada",
		"--claims-json", `[{"kind":"outcome","text":"Main","authority":"caller_asserted"},{"kind":"intent","text":"Start","authority":"caller_asserted"},{"kind":"verification","text":"None","authority":"caller_asserted"},{"kind":"limitation","text":"None","authority":"caller_asserted"}]`,
		"--anchors-json", `[{"kind":"task","target":"code-task","authority":"caller_asserted"}]`)
	if got := runCLI(t, "", "stats", "code", "--by", "task"); !strings.Contains(got, "code-task          1      1     +3") {
		t.Fatalf("by task:\n%s", got)
	}
	if got := runCLI(t, "", "stats", "code", "--by", "node", "--output", "json"); !strings.Contains(got, `"key": "code-node"`) {
		t.Fatalf("by node:\n%s", got)
	}
}
