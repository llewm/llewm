package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/clientperm"
	"github.com/spf13/cobra"
)

func recordProcedureFile(t *testing.T, value map[string]any) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "procedure.json")
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	runCLI(t, "", "procedure", "record", "--file", path)
}

func TestGrantCommandPreviewPreservesBindingsAndNeverEmitsPartialCommands(t *testing.T) {
	testProject(t)
	runCLI(t, "", "init")
	sentinel := filepath.Join(t.TempDir(), "must-not-exist")
	procedure := installRunbook("preview-install",
		map[string]any{"id": "copy", "description": "Copy only after checks pass", "argv_template": []string{"scp", "{{bin}}", "{{host}}:binary"}},
		map[string]any{"id": "migrate", "description": "Only when migration is pending", "argv_template": []string{"ssh", "{{host}}", "migrate --from {{from}} --to {{to}}"}},
		map[string]any{"id": "inert", "description": "Do not execute", "argv_template": []string{"touch", sentinel}},
		map[string]any{"id": "manual", "description": "Review the result"})
	procedure["parameters"] = []map[string]any{
		{"name": "bin", "description": "binary", "default": "/wrong/default"},
		{"name": "host", "description": "host", "default": "test-host"},
		{"name": "from", "description": "old schema"},
		{"name": "to", "description": "new schema"},
	}
	procedure["working_directory"] = "components/continuity"
	recordProcedureFile(t, procedure)
	previous := interactiveInput
	interactiveInput = func(*cobra.Command) bool { return true }
	t.Cleanup(func() { interactiveInput = previous })
	bound := "/some path/it's-$(literal)"
	created, err := runGrantCLI("yes\n", "standing-grant", "create", "preview-install", "--param", "bin="+bound)
	if err != nil {
		t.Fatal(err)
	}
	id := strings.TrimSpace(strings.SplitN(strings.SplitN(created, "Standing grant: ", 2)[1], "\n", 2)[0])
	var preview clientperm.GrantPreview
	read := func() {
		t.Helper()
		preview = clientperm.GrantPreview{}
		data := runCLI(t, "", "standing-grant", "show", id, "--commands", "--codex-rules", "/rules one", "--codex-rules", "/rules-two", "--output", "json")
		if err := json.Unmarshal([]byte(data), &preview); err != nil {
			t.Fatal(err)
		}
	}
	read()
	p := preview.Procedures[0]
	if p.WorkingDirectory != "components/continuity" || len(p.Steps) != 4 {
		t.Fatalf("preview: %+v", p)
	}
	copy := p.Steps[0]
	if !reflect.DeepEqual(copy.Argv, []string{"scp", bound, "test-host:binary"}) || copy.State != "bound" {
		t.Fatalf("bindings changed: %+v", copy)
	}
	wantCheck := []string{"codex", "execpolicy", "check", "--rules", "/rules one", "--rules", "/rules-two", "--", "scp", bound, "test-host:binary"}
	if !reflect.DeepEqual(copy.CheckArgv, wantCheck) || !strings.Contains(copy.Command, `'\''`) {
		t.Fatalf("check/quoting: %+v", copy)
	}
	migration := p.Steps[1]
	if migration.State != "unresolved" || !reflect.DeepEqual(migration.Missing, []string{"from", "to"}) || len(migration.Argv) != 0 || migration.Command != "" || len(migration.CheckArgv) != 0 {
		t.Fatalf("partial command leaked: %+v", migration)
	}
	if p.Steps[3].State != "description_only" {
		t.Fatalf("description lost: %+v", p.Steps[3])
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("preview executed command: %v", err)
	}
	text := runCLI(t, "", "standing-grant", "show", id, "--commands", "--codex-rules", "/rules")
	if !strings.Contains(text, "Only when migration is pending") || !strings.Contains(text, "Rule check (not run):") {
		t.Fatal(text)
	}
	runCLI(t, "", "standing-grant", "revoke", id)
	read()
	for _, step := range preview.Procedures[0].Steps {
		if len(step.Argv) != 0 || step.Command != "" || len(step.CheckArgv) != 0 {
			t.Fatalf("inactive command emitted: %+v", step)
		}
	}
}

func installRunbook(id string, steps ...map[string]any) map[string]any {
	return map[string]any{"id": id, "title": "Install " + id, "purpose": "Install the build", "kind": "runbook", "status": "accepted",
		"platforms": []string{"linux/amd64"}, "parameters": []map[string]any{{"name": "bin", "description": "binary", "required": true}},
		"declared_effects": []string{"Replaces the binary"}, "steps": steps, "source": "docs/installation.md"}
}

func runGrantCLI(input string, args ...string) (string, error) {
	cmd := newRootCommand("llewm")
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(input))
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestStandingGrantsThroughTheCLI(t *testing.T) {
	testProject(t)
	runCLI(t, "", "init")
	recordProcedureFile(t, installRunbook("local-install", map[string]any{"id": "build", "description": "Build", "argv_template": []string{"go", "build", "-o", "{{bin}}"}}))
	recordProcedureFile(t, installRunbook("both-install", map[string]any{"id": "local", "description": "Run procedure:local-install"}))
	if output, err := runGrantCLI("", "help", "standing-grant", "create"); err != nil || !strings.Contains(output, "llewm standing-grant create PROCEDURE[@REVISION]") {
		t.Fatalf("safe create help: %v\n%s", err, output)
	}

	// An agent's shell is not a terminal, so it cannot grant itself anything.
	if output, err := runGrantCLI("yes\n", "standing-grant", "create", "both-install"); err == nil || !strings.Contains(err.Error(), "needs a person at a terminal") {
		t.Fatalf("non-interactive create: %v %s", err, output)
	}
	previous := interactiveInput
	interactiveInput = func(*cobra.Command) bool { return true }
	t.Cleanup(func() { interactiveInput = previous })
	if output, err := runGrantCLI("yes\n", "standing-grant", "create", "missing"); err == nil || !strings.Contains(err.Error(), "llewm procedure show --status accepted") || !strings.Contains(err.Error(), "continuity help standing-grant create") {
		t.Fatalf("missing procedure guidance: %v\n%s", err, output)
	}
	if output, err := runGrantCLI("no\n", "standing-grant", "create", "both-install"); err == nil || !strings.Contains(err.Error(), "not granted") {
		t.Fatalf("declined create: %v %s", err, output)
	}
	created, err := runGrantCLI("yes\n", "standing-grant", "create", "procedure:both-install@1", "--param", "bin=/opt/continuity", "--expires", "30d", "--creator", "engineer", "--reason", "routine installs")
	if err != nil || !strings.Contains(created, "Grant agents permission to run procedure:both-install@1 (Install both-install) after slice commit, until ") || !strings.Contains(created, "Covers: procedure:both-install@1, procedure:local-install") || !strings.Contains(created, "Effect: Replaces the binary") || !strings.Contains(created, "State: active") || !strings.Contains(created, "- bin=/opt/continuity") {
		t.Fatalf("create: %v\n%s", err, created)
	}
	id := strings.TrimSpace(strings.SplitN(strings.SplitN(created, "Standing grant: ", 2)[1], "\n", 2)[0])

	if list := runCLI(t, "", "standing-grant", "list"); !strings.Contains(list, id+"\tprocedure:both-install@1\tafter_slice_commit\tactive\tuntil ") || !strings.Contains(list, "Standing grants: 1.") {
		t.Fatalf("list:\n%s", list)
	}
	runCLI(t, "", "checkpoint", "create", "--task", "Build", "--task-id", "build", "--next", "Continue")
	hook := runCLI(t, `{"session_id":"s","hook_event_name":"SessionStart","source":"startup"}`, "hook", "claude-code")
	if !strings.Contains(hook, "**Standing grants (user-granted):** procedure:both-install@1 after slice commit until ") || !strings.Contains(hook, "(covers local-install@1), by engineer. Run these without asking") {
		t.Fatalf("bootstrap without a grant notice:\n%s", hook)
	}

	// Revising a covered procedure voids the grant.
	revised := installRunbook("local-install", map[string]any{"id": "build", "description": "Build it", "argv_template": []string{"go", "build"}})
	revised["revision"] = 2
	recordProcedureFile(t, revised)
	if show := runCLI(t, "", "standing-grant", "show", id); !strings.Contains(show, "State: void (procedure:local-install is now at revision 2)") || !strings.Contains(show, "- procedure:local-install@1: Install local-install (now at revision 2)") {
		t.Fatalf("show void:\n%s", show)
	}
	if list := runCLI(t, "", "standing-grant", "list"); !strings.Contains(list, "Standing grants: 0.") {
		t.Fatalf("list after void:\n%s", list)
	}
	if revoked := runCLI(t, "", "standing-grant", "revoke", id, "--creator", "engineer"); !strings.Contains(revoked, "Revoked "+id+" (procedure:both-install@1).") {
		t.Fatalf("revoke: %s", revoked)
	}
	var grants []struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal([]byte(runCLI(t, "", "standing-grant", "list", "--all", "--output", "json")), &grants); err != nil || len(grants) != 1 || grants[0].State != "revoked" {
		t.Fatalf("list --all --json: %+v %v", grants, err)
	}
}
