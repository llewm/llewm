package clientperm

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func codexFixture(t *testing.T) (string, Rules) {
	t.Helper()
	home := t.TempDir()
	data := "# keep this comment\nmodel = \"test\"\n[plugins.\"llewm@llewm\".mcp_servers.continuity.tools.continuity_checkpoint]\napproval_mode = \"prompt\"\n"
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(data), 0640); err != nil {
		t.Fatal(err)
	}
	return home, Rules{Client: Codex, CodexTools: []string{"continuity_info", "continuity_checkpoint"}, Allow: []string{`prefix_rule(pattern=["llewm"], decision="allow")`}, Deny: []string{`prefix_rule(pattern=["llewm", "standing-grant", "create"], decision="forbidden")`}}
}
func TestCodexReconcile(t *testing.T) {
	home, rules := codexFixture(t)
	p, err := PlanCodex(home, rules)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Preserved) != 1 || !bytes.HasPrefix(p.Files[0].After, p.Files[0].Before) {
		t.Fatalf("lost user config: %+v", p)
	}
	backups, err := p.Apply()
	if err != nil || len(backups) != 1 {
		t.Fatalf("backups: %v %v", backups, err)
	}
	backup, _ := os.ReadFile(backups[0])
	if !bytes.Equal(backup, p.Files[0].Before) {
		t.Fatal("bad backup")
	}
	again, err := PlanCodex(home, rules)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range again.Files {
		if !bytes.Equal(c.Before, c.After) {
			t.Fatal("not idempotentent")
		}
	}
	if b, err := again.Apply(); err != nil || len(b) != 0 {
		t.Fatalf("no-op: %v %v", b, err)
	}
	rules.CodexTools = []string{"continuity_checkpoint", "continuity_usage"}
	rules.Allow = nil
	changed, err := PlanCodex(home, rules)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(changed.Files[0].After), "continuity_info") || !strings.Contains(string(changed.Files[0].After), `approval_mode = "prompt"`) || strings.Contains(string(changed.Files[1].After), `decision="allow"`) {
		t.Fatal("bad reconciliation")
	}
	if _, err := changed.Apply(); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(filepath.Join(home, "config.toml"))
	if info.Mode().Perm() != 0640 {
		t.Fatal("mode changed")
	}
}
func TestCodexRefusesEditsAndStalePreview(t *testing.T) {
	home, rules := codexFixture(t)
	p, err := PlanCodex(home, rules)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Apply(); err != nil {
		t.Fatal(err)
	}
	p, err = PlanCodex(home, rules)
	if err != nil {
		t.Fatal(err)
	}
	edited := bytes.Replace(p.Files[0].Before, []byte(`approval_mode = "approve"`), []byte(`approval_mode = "prompt"`), 1)
	if err := os.WriteFile(p.Files[0].Path, edited, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Apply(); err == nil {
		t.Fatal("accepted stale preview")
	}
	if _, err := PlanCodex(home, rules); err == nil || !strings.Contains(err.Error(), "were edited") {
		t.Fatalf("accepted edited managed restriction: %v", err)
	}
}
func TestCodexRollbackSecondReplacement(t *testing.T) {
	home, rules := codexFixture(t)
	p, err := PlanCodex(home, rules)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	backups, err := p.apply(func(a, b string) error {
		calls++
		if calls == 2 {
			return fmt.Errorf("injected failure")
		}
		return os.Rename(a, b)
	})
	if err == nil || len(backups) != 1 {
		t.Fatalf("expected failure and backup: %v %v", backups, err)
	}
	actual, _ := os.ReadFile(p.Files[0].Path)
	if !bytes.Equal(actual, p.Files[0].Before) {
		t.Fatal("config not rolled back")
	}
	if _, err := os.Stat(p.Files[1].Path); !os.IsNotExist(err) {
		t.Fatal("rules file created")
	}
}
func TestCodexRefusesUnownedRulesAndInvalidTOML(t *testing.T) {
	for _, test := range []struct{ path, body string }{{"config.toml", "[bad"}, {"rules/llewm.rules", `prefix_rule(pattern=["ssh"], decision="allow")`}} {
		t.Run(test.path, func(t *testing.T) {
			home, rules := codexFixture(t)
			path := filepath.Join(home, test.path)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(test.body), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := PlanCodex(home, rules); err == nil {
				t.Fatal("unsafe merge accepted")
			}
		})
	}
}

func TestCodexPreservesInheritedRestriction(t *testing.T) {
	home, rules := codexFixture(t)
	original := []byte("# restriction\n[plugins.\"llewm@llewm\"]\napproval_mode = \"prompt\"\n")
	if err := os.WriteFile(filepath.Join(home, "config.toml"), original, 0600); err != nil {
		t.Fatal(err)
	}
	p, err := PlanCodex(home, rules)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(p.Files[0].After, []byte(`approval_mode = "approve"`)) {
		t.Fatal("overrode inherited prompt")
	}
}

func TestMalformedManagedBlockDoesNotPanic(t *testing.T) {
	for _, data := range []string{managedStart + managedEnd, managedEnd, managedStart + "x\n", managedBlock("x\n") + "# appended\n"} {
		if _, err := splitManaged([]byte(data)); err == nil {
			t.Fatalf("accepted malformed block %q", data)
		}
	}
}
