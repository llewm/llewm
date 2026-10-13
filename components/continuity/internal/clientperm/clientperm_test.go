package clientperm

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/procedures"
	"github.com/llewm/llewm/components/continuity/internal/standinggrant"
)

func TestRenderAndRules(t *testing.T) {
	values := map[string]string{"bin": "/home/me/.local/bin/llewm", "host": "me@box"}
	for _, test := range []struct {
		argv   []string
		claude string
		codex  string
	}{
		{[]string{"go", "build", "-o", "{{bin}}.new", "./cmd/llewm"}, "Bash(go build -o /home/me/.local/bin/llewm.new ./cmd/llewm)", `prefix_rule(pattern=["go", "build", "-o", "/home/me/.local/bin/llewm.new", "./cmd/llewm"], decision="allow")`},
		{[]string{"{{bin}}", "database", "migrate", "--from", "{{from}}", "--to", "{{to}}"}, "Bash(/home/me/.local/bin/llewm database migrate --from *)", ""},
		{[]string{"cp", "backup-{{to}}.db"}, "Bash(cp backup-*)", ""},
		{[]string{"ssh", "{{host}}", "cd ~/Llewm && llewm database migrate --to {{to}}"}, "Bash(ssh me@box 'cd ~/Llewm && llewm database migrate --to *)", ""},
		{[]string{"ssh", "{{host}}", "it's"}, `Bash(ssh me@box 'it'\''s')`, `prefix_rule(pattern=["ssh", "me@box", "it's"], decision="allow")`},
	} {
		rendered := Render(test.argv, values)
		if got := rule(ClaudeCode, rendered); got != test.claude {
			t.Errorf("claude %v: %s, want %s", test.argv, got, test.claude)
		}
		if got := rule(Codex, rendered); got != test.codex {
			t.Errorf("codex %v: %s, want %s", test.argv, got, test.codex)
		}
	}
	if rendered := Render([]string{"{{out}}", "x"}, nil); len(rendered.Args) != 0 || rendered.Exact {
		t.Fatalf("leading placeholder: %+v", rendered)
	}
}

func TestBuildIncludesGrantRules(t *testing.T) {
	ctx := context.Background()
	db, err := database.Initialize(ctx, filepath.Join(t.TempDir(), "continuity.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	projectID, err := (activity.Store{DB: db}).EnsureProject(ctx, filepath.Join(t.TempDir(), "repo"), "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []procedures.Procedure{
		{ProjectID: projectID, ID: "local-install", Title: "Local", Purpose: "Install", Kind: procedures.KindRunbook, Status: "accepted", Platforms: []string{"linux/amd64"},
			Parameters: []procedures.Parameter{{Name: "bin", Description: "binary", Required: true}}, DeclaredEffects: []string{"replaces the binary"},
			Steps: []procedures.Step{{ID: "build", Description: "Build", ArgvTemplate: []string{"go", "build", "-o", "{{bin}}.new", "./cmd/llewm"}}, {ID: "swap", Description: "Swap", ArgvTemplate: []string{"mv", "{{bin}}.new", "{{bin}}"}}}, Source: "docs/installation.md"},
		{ProjectID: projectID, ID: "both", Title: "Both", Purpose: "Install", Kind: procedures.KindRunbook, Status: "accepted", Platforms: []string{"linux/amd64"},
			Parameters: []procedures.Parameter{{Name: "host", Description: "host", Required: true, Default: "me@box"}}, DeclaredEffects: []string{"replaces binaries"},
			Steps: []procedures.Step{{ID: "local", Description: "Run procedure:local-install"}, {ID: "check", Description: "Check", ArgvTemplate: []string{"ssh", "{{host}}", "llewm version"}}}, Source: "docs/installation.md"},
	} {
		if _, err := (procedures.Service{DB: db}).Append(ctx, value); err != nil {
			t.Fatal(err)
		}
	}
	grant, err := (standinggrant.Service{DB: db}).Create(ctx, standinggrant.Request{ProjectID: projectID, ProcedureID: "both", Parameters: map[string]string{"bin": "/b/llewm"}, Creator: "engineer"})
	if err != nil {
		t.Fatal(err)
	}
	rules, err := Build(ctx, db, projectID, ClaudeCode, nil)
	if err != nil {
		t.Fatal(err)
	}
	wantGrant := []string{"Bash(ssh me@box 'llewm version')", "Bash(go build -o /b/llewm.new ./cmd/llewm)", "Bash(mv /b/llewm.new /b/llewm)"}
	if len(rules.Grants) != 1 || rules.Grants[0].GrantID != grant.ID || !reflect.DeepEqual(rules.Grants[0].Rules, wantGrant) {
		t.Fatalf("grant rules: %+v", rules.Grants)
	}
	if !reflect.DeepEqual(rules.Allow, append([]string{ClaudeMCPRule, "Bash(llewm *)"}, wantGrant...)) || !reflect.DeepEqual(rules.Deny, []string{"Bash(llewm standing-grant create *)"}) {
		t.Fatalf("rules: %+v", rules)
	}
	codex, err := Build(ctx, db, projectID, Codex, []string{"continuity_info"})
	if err != nil || codex.CodexTools[0] != "continuity_info" || !strings.Contains(codex.Deny[0], `decision="forbidden"`) {
		t.Fatalf("codex: %+v %v", codex, err)
	}
	if _, err := Build(ctx, db, projectID, "cursor", nil); err == nil {
		t.Fatal("unknown client accepted")
	}
}

func TestMergeClaudeKeepsOrderAndIsIdempotent(t *testing.T) {
	input := []byte(`{"theme":"dark","permissions":{"allow":["Bash(git *)"],"defaultMode":"auto"},"zeta":{"b":1,"a":2}}`)
	merged, added, err := MergeClaude(input, []string{"Bash(git *)", ClaudeMCPRule}, []string{"Bash(llewm standing-grant create *)"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(added, []string{"allow: " + ClaudeMCPRule, "deny: Bash(llewm standing-grant create *)"}) {
		t.Fatalf("added: %v", added)
	}
	want := `{
  "theme": "dark",
  "permissions": {
    "allow": [
      "Bash(git *)",
      "mcp__plugin_llewm_continuity"
    ],
    "defaultMode": "auto",
    "deny": [
      "Bash(llewm standing-grant create *)"
    ]
  },
  "zeta": {
    "b": 1,
    "a": 2
  }
}
`
	if string(merged) != want {
		t.Fatalf("merged:\n%s", merged)
	}
	again, added, err := MergeClaude(merged, []string{ClaudeMCPRule}, []string{"Bash(llewm standing-grant create *)"})
	if err != nil || len(added) != 0 || string(again) != string(merged) {
		t.Fatalf("second merge changed: %v %v", added, err)
	}
	if escaped, _, err := MergeClaude(nil, []string{"Bash(a && b <c>)"}, nil); err != nil || !strings.Contains(string(escaped), `"Bash(a && b <c>)"`) {
		t.Fatalf("escaped: %s %v", escaped, err)
	}
	if snippet, err := ClaudeSnippet(Rules{Allow: []string{"Bash(a && b)"}}); err != nil || !strings.Contains(string(snippet), `"Bash(a && b)"`) {
		t.Fatalf("snippet: %s %v", snippet, err)
	}
	if fresh, added, err := MergeClaude(nil, []string{"x"}, nil); err != nil || len(added) != 1 || string(fresh) != "{\n  \"permissions\": {\n    \"allow\": [\n      \"x\"\n    ]\n  }\n}\n" {
		t.Fatalf("fresh: %s %v %v", fresh, added, err)
	}
	for _, bad := range []string{`[]`, `{"permissions":[]}`, `{"permissions":{"allow":"x"}}`, `{} {}`, `{`} {
		if _, _, err := MergeClaude([]byte(bad), []string{"x"}, nil); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestDetect(t *testing.T) {
	homes := Homes{Home: t.TempDir(), CodexHome: filepath.Join(t.TempDir(), "codex")}
	root := t.TempDir()
	if statuses := Detect(homes, root, nil); len(statuses) != 0 {
		t.Fatalf("no clients installed: %+v", statuses)
	}
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(homes.Home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(homes.CodexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	statuses := Detect(homes, root, []string{"continuity_info", "continuity_checkpoint"})
	if len(statuses) != 2 || statuses[0].Client != ClaudeCode || statuses[0].OK() || statuses[1].Client != Codex || len(statuses[1].Missing) != 2 {
		t.Fatalf("nothing configured: %+v", statuses)
	}
	warnings := Warnings(statuses)
	if len(warnings) != 2 || !strings.Contains(warnings[0], "--client claude-code` in a terminal to see them and add them with --write --scope user") || !strings.Contains(warnings[1], "Codex lacks Continuity permission rules (no exec-policy rule allows the llewm CLI; no Continuity MCP tool is approved)") {
		t.Fatalf("warnings: %q", warnings)
	}

	local := filepath.Join(root, ".claude", "settings.local.json")
	write(local, `{"permissions":{"allow":["mcp__plugin_llewm_continuity","Bash(llewm:*)"]}}`)
	rules := filepath.Join(homes.CodexHome, "rules", "default.rules")
	write(rules, "prefix_rule(pattern=[\"go\"], decision=\"allow\")\nprefix_rule(pattern=[\"llewm\"], decision=\"allow\")\n")
	config := filepath.Join(homes.CodexHome, "config.toml")
	write(config, "[plugins.\"llewm@llewm\".mcp_servers.continuity.tools.continuity_info]\napproval_mode = \"approve\"\n")
	statuses = Detect(homes, root, []string{"continuity_info", "continuity_checkpoint"})
	if !statuses[0].OK() || !reflect.DeepEqual(statuses[0].Configured, []string{local}) {
		t.Fatalf("claude configured: %+v", statuses[0])
	}
	if !reflect.DeepEqual(statuses[1].Missing, []string{"1 of 2 Continuity MCP tools are not approved"}) || !reflect.DeepEqual(statuses[1].Configured, []string{rules, config}) {
		t.Fatalf("codex partial: %+v", statuses[1])
	}
	// Without tool names (readiness), any approval counts.
	if statuses = Detect(homes, root, nil); !statuses[1].OK() {
		t.Fatalf("codex without tool names: %+v", statuses[1])
	}
}
