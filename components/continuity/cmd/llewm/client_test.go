package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestClientPermissionsPrintWriteAndDoctor(t *testing.T) {
	testProject(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	runCLI(t, "", "init")
	recordProcedureFile(t, installRunbook("local-install", map[string]any{"id": "build", "description": "Build", "argv_template": []string{"go", "build", "-o", "{{bin}}.new", "./cmd/llewm"}}))

	// Without ~/.claude, Claude Code is not in use and doctor says nothing.
	if doctor := runCLI(t, "", "doctor"); strings.Contains(doctor, "client rules") {
		t.Fatalf("doctor without ~/.claude:\n%s", doctor)
	}
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if doctor := runCLI(t, "", "doctor"); !strings.Contains(doctor, "client rules warning  Claude Code lacks Continuity permission rules") {
		t.Fatalf("doctor without rules:\n%s", doctor)
	}
	var readiness struct {
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(runCLI(t, "", "doctor", "--readiness")), &readiness); err != nil || len(readiness.Warnings) != 1 || !strings.Contains(readiness.Warnings[0], "--write --scope user") {
		t.Fatalf("readiness warnings: %+v %v", readiness, err)
	}

	printed := runCLI(t, "", "client", "permissions")
	for _, want := range []string{`"mcp__plugin_llewm_continuity"`, `"Bash(llewm *)"`, `"Bash(llewm standing-grant create *)"`, "auto mode can still classify"} {
		if !strings.Contains(printed, want) {
			t.Fatalf("print lacks %q:\n%s", want, printed)
		}
	}
	codex := runCLI(t, "", "client", "permissions", "--client", "codex")
	if !strings.Contains(codex, `prefix_rule(pattern=["llewm"], decision="allow")`) || !strings.Contains(codex, "[plugins.\"llewm@llewm\".mcp_servers.continuity.tools.continuity_checkpoint]\napproval_mode = \"approve\"") {
		t.Fatalf("codex:\n%s", codex)
	}

	// --write needs a person at a terminal.
	if output, err := runGrantCLI("", "client", "permissions", "--write"); err == nil || !strings.Contains(err.Error(), "needs a person at a terminal") {
		t.Fatalf("non-interactive write: %v %s", err, output)
	}
	if _, err := runGrantCLI("yes\n", "client", "permissions", "--client", "codex", "--write"); err == nil {
		t.Fatal("noninteractive Codex writer accepted")
	}
	previous := interactiveInput
	interactiveInput = func(*cobra.Command) bool { return true }
	t.Cleanup(func() { interactiveInput = previous })

	// A grant adds its procedure's commands.
	if _, err := runGrantCLI("yes\n", "standing-grant", "create", "local-install", "--param", "bin=/b/llewm"); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(home, ".claude", "settings.json")
	if err := os.WriteFile(settings, []byte(`{"theme":"dark","permissions":{"allow":["Bash(git *)"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	written, err := runGrantCLI("", "client", "permissions", "--write", "--scope", "user")
	if err != nil || !strings.Contains(written, "Backed up "+settings+" to "+settings+".bak-") || !strings.Contains(written, "Added 4 rules to "+settings) || !strings.Contains(written, "- allow: Bash(go build -o /b/llewm.new ./cmd/llewm)") {
		t.Fatalf("write: %v\n%s", err, written)
	}
	data, err := os.ReadFile(settings)
	if err != nil || !strings.HasPrefix(string(data), "{\n  \"theme\": \"dark\",\n  \"permissions\": {\n    \"allow\": [\n      \"Bash(git *)\",\n      \"mcp__plugin_llewm_continuity\"") {
		t.Fatalf("settings:\n%s %v", data, err)
	}
	if again, err := runGrantCLI("", "client", "permissions", "--write", "--scope", "user"); err != nil || !strings.Contains(again, "already has every rule; nothing changed") {
		t.Fatalf("second write: %v\n%s", err, again)
	}
	if doctor := runCLI(t, "", "doctor"); !strings.Contains(doctor, "client rules ok       Claude Code: "+settings) {
		t.Fatalf("doctor with rules:\n%s", doctor)
	}
	if output, err := runGrantCLI("no\n", "client", "permissions", "--write", "--client", "codex"); err != nil || !strings.Contains(output, "Cancelled") {
		t.Fatalf("cancel: %s %v", output, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "config.toml")); !os.IsNotExist(err) {
		t.Fatal("cancel wrote config")
	}
	if output, err := runGrantCLI("yes\n", "client", "permissions", "--write", "--client", "codex"); err != nil || !strings.Contains(output, "Updated managed permissions") {
		t.Fatalf("codex write: %s %v", output, err)
	}
	if output, err := runGrantCLI("", "client", "permissions", "--write", "--client", "codex"); err != nil || !strings.Contains(output, "nothing changed") {
		t.Fatalf("codex repeat: %s %v", output, err)
	}

}
