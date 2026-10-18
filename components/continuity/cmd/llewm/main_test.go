package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/llewm/llewm/components/continuity/internal/buildinfo"
	"github.com/llewm/llewm/components/continuity/internal/database"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/llewm/llewm/components/continuity/internal/app"
	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
	"github.com/llewm/llewm/components/continuity/internal/roadmap"
)

// expectedMCPToolCount is the number of tools the MCP server registers.
const expectedMCPToolCount = 26

// latestSchema is the newest migration this build embeds.
var latestSchema = func() string {
	name, err := database.CompiledLatestMigration()
	if err != nil {
		panic(err)
	}
	return name
}()

func testProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("CONTINUITY_GLOBAL_HOME", t.TempDir())
	t.Setenv("CONTINUITY_HOME", filepath.Join(root, ".llewm/continuity"))
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("# Rules\n\nRun tests.\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("/.llewm/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "AGENTS.md", "CLAUDE.md", ".gitignore"}, {"-c", "user.name=Continuity Test", "-c", "user.email=test@example.invalid", "commit", "-q", "-m", "Initialize test project"}} {
		command := exec.Command("git", args...)
		command.Dir = root
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	t.Chdir(root)
	return root
}

// writeJSONFile writes value to a temporary JSON file and returns its path.
func writeJSONFile(t *testing.T, value any) string {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "record.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// taskTip reads a task's current checkpoint tip, as a caller continuing it does.
func taskTip(t *testing.T, taskID string) string {
	t.Helper()
	var lineage struct {
		CurrentTip string `json:"current_tip"`
	}
	if err := json.Unmarshal([]byte(runCLI(t, "", "checkpoint", "task-lineage", "--task-id", taskID)), &lineage); err != nil {
		t.Fatal(err)
	}
	return lineage.CurrentTip
}

func runCLI(t *testing.T, input string, args ...string) string {
	t.Helper()
	return runCLIAs(t, "llewm", input, args...)
}

// runCLIAs builds the root command under the given invocation name, so tests
// can verify behavior that depends on it (such as executable-specific completion registration) without changing every other call site's default.
func runCLIAs(t *testing.T, name, input string, args ...string) string {
	t.Helper()
	cmd := newRootCommand(name)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(input))
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out.String())
	}
	return out.String()
}

func TestInitReportsCreatedAndExistingState(t *testing.T) {
	root := testProject(t)
	projectName := filepath.Base(root)

	initialized := runCLI(t, "", "init")
	for _, expected := range []string{
		"Initialized " + projectName + "\n",
		"Config: " + filepath.Join(root, ".llewm/continuity", "config.toml") + " (created)\n",
		"Database: " + filepath.Join(root, ".llewm/continuity", "continuity.db") + " (created)\n",
	} {
		if !strings.Contains(initialized, expected) {
			t.Fatalf("initial init output missing %q:\n%s", expected, initialized)
		}
	}

	repeated := runCLI(t, "", "init")
	for _, expected := range []string{
		"Already initialized " + projectName + "\n",
		"Config: " + filepath.Join(root, ".llewm/continuity", "config.toml") + " (existing)\n",
		"Database: " + filepath.Join(root, ".llewm/continuity", "continuity.db") + " (existing)\n",
		"No initialization files were created.\n",
		"Run `llewm doctor` to verify project health.\n",
	} {
		if !strings.Contains(repeated, expected) {
			t.Fatalf("repeated init output missing %q:\n%s", expected, repeated)
		}
	}
}

func TestDoctorTreatsClaudeInstructionsAsOptional(t *testing.T) {
	root := testProject(t)
	runCLI(t, "", "init")
	if err := os.Remove(filepath.Join(root, "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}
	output := runCLI(t, "", "doctor")
	if !strings.Contains(output, "CLAUDE.md    absent") || !strings.Contains(output, "optional project instruction source") {
		t.Fatalf("doctor did not label missing optional instructions: %s", output)
	}
	if err := os.Remove(filepath.Join(root, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	command := newRootCommand("llewm")
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	command.SetArgs([]string{"doctor"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "checks need attention") {
		t.Fatalf("doctor accepted missing required project instructions: %v", err)
	}
}

func TestCardCommandsAreRetired(t *testing.T) {
	testProject(t)
	runCLI(t, "", "init")
	retired := newRootCommand("llewm")
	retired.SetOut(io.Discard)
	retired.SetErr(io.Discard)
	retired.SetArgs([]string{"card", "list"})
	if err := retired.Execute(); err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("card command still exists: %v", err)
	}
}

func TestWorkSummaryAndStateCheckSupportTextAndJSON(t *testing.T) {
	testProject(t)
	runCLI(t, "", "init")
	checkpointID := strings.TrimSpace(runCLI(t, "", "checkpoint", "create", "--task", "Coherent work", "--task-id", "coherent-work", "--summary", "Keep state current", "--next", "Run the hygiene check"))
	runCLI(t, "", "roadmap", "node", "--id", "coherent-work", "--title", "Coherent work", "--status", "active", "--priority", "5", "--source-path", "AGENTS.md", "--source-ref", "HEAD")
	runCLI(t, "", "roadmap", "link", "--kind", "task", "--node", "coherent-work", "--task", "coherent-work", "--reason", "Exact managed work", "--request-key", "bind-coherent-work")
	planPath := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(planPath, []byte(`{"id":"coherent-work-plan","roadmap_node_id":"coherent-work","task_id":"coherent-work","title":"Coherent work","summary":"Keep state current","status":"accepted","creator":"human","items":[{"id":"check","position":1,"title":"Run the hygiene check","status":"pending"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	runCLI(t, "", "track-plan", "record", "--file", planPath)

	textOutput := runCLI(t, "", "work", "--summary")
	for _, expected := range []string{"Current task: Coherent work [in_progress]", "Checkpoint: " + checkpointID, "Next: Run the hygiene check", "Work: focus=1 active=1"} {
		if !strings.Contains(textOutput, expected) {
			t.Fatalf("work text missing %q:\n%s", expected, textOutput)
		}
	}

	jsonOutput := runCLI(t, "", "work", "--summary", "--output", "json")
	var summary struct {
		SchemaVersion int `json:"schema_version"`
		Counts        struct {
			Active int `json:"active"`
		} `json:"counts"`
	}
	if err := json.Unmarshal([]byte(jsonOutput), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.SchemaVersion != 1 || summary.Counts.Active != 1 {
		t.Fatalf("unexpected work JSON: %s", jsonOutput)
	}

	clean := runCLI(t, "", "work", "check")
	if !strings.Contains(clean, "State: current\n") || !strings.Contains(clean, "Findings: 0\n") {
		t.Fatalf("clean work requires a current-plan projection: %s", clean)
	}
	runCLI(t, "", "roadmap", "node", "--id", "coherent-work", "--title", "Coherent work", "--status", "active", "--priority", "5", "--source-path", "AGENTS.md", "--source-ref", "HEAD")
	drifted := runCLI(t, "", "work", "check", "--output", "json")
	if !strings.Contains(drifted, `"state": "findings"`) || !strings.Contains(drifted, `"code": "stale_task_binding"`) {
		t.Fatalf("stale binding was not reported: %s", drifted)
	}

	listed := runCLI(t, "", "work", "list", "--view", "stale")
	if !strings.Contains(listed, "View: stale\nTracks: 1\n") || !strings.Contains(listed, "node:coherent-work") || !strings.Contains(listed, "stale=true") {
		t.Fatalf("unexpected stale work list: %s", listed)
	}
	detail := runCLI(t, "", "work", "show", "coherent-work")
	for _, expected := range []string{"Matched by: roadmap_node_and_task", "Roadmap node: coherent-work rev=2 [active]", "Task: coherent-work", "Findings: stale_task_binding"} {
		if !strings.Contains(detail, expected) {
			t.Fatalf("work detail missing %q: %s", expected, detail)
		}
	}
	listJSON := runCLI(t, "", "work", "list", "--view", "active", "--output", "json")
	if !strings.Contains(listJSON, `"view": "active"`) || !strings.Contains(listJSON, `"inventory_total": 1`) {
		t.Fatalf("unexpected work list JSON: %s", listJSON)
	}
}

func TestPlanProjectionChecksAreExplicit(t *testing.T) {
	root := testProject(t)
	runCLI(t, "", "init")
	projection := filepath.Join(root, "components", "continuity", "docs", "current-plan.md")
	for _, state := range []string{"missing", "drifted", "directory"} {
		t.Run(state, func(t *testing.T) {
			if state != "missing" {
				if err := os.MkdirAll(filepath.Dir(projection), 0o700); err != nil {
					t.Fatal(err)
				}
				if state == "drifted" {
					if err := os.WriteFile(projection, []byte("Unrelated export\n"), 0o600); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.Remove(projection); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(projection, 0o700); err != nil {
						t.Fatal(err)
					}
				}
			}
			for _, args := range [][]string{{"work", "--summary", "--output", "json"}, {"work", "check", "--output", "json"}} {
				output := runCLI(t, "", args...)
				var result struct {
					Findings []json.RawMessage `json:"findings"`
				}
				if err := json.Unmarshal([]byte(output), &result); err != nil {
					t.Fatal(err)
				}
				if len(result.Findings) != 0 {
					t.Fatalf("work read depends on %s projection: %s", state, output)
				}
			}
			if state == "directory" {
				command := newRootCommand("llewm")
				command.SetOut(io.Discard)
				command.SetErr(io.Discard)
				command.SetArgs([]string{"track-plan", "check"})
				if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "regular file") {
					t.Fatalf("explicit projection check accepted a directory: %v", err)
				}
			} else {
				output := runCLI(t, "", "track-plan", "check", "--output", "json")
				if !strings.Contains(output, `"state": "`+state+`"`) {
					t.Fatalf("explicit projection check lost %s state: %s", state, output)
				}
			}
		})
	}
}

func TestRoadmapCoverageSupportsBoundedJSONAndText(t *testing.T) {
	testProject(t)
	runCLI(t, "", "init")
	for _, args := range [][]string{
		{"roadmap", "node", "--id", "parent", "--title", "Parent", "--status", "active", "--priority", "5", "--source-path", "AGENTS.md", "--source-ref", "HEAD"},
		{"roadmap", "node", "--id", "child", "--title", "Child", "--status", "active", "--priority", "10", "--source-path", "AGENTS.md", "--source-ref", "HEAD"},
		{"roadmap", "link", "--kind", "file", "--node", "parent", "--path", "AGENTS.md", "--relationship", "contract"},
		{"roadmap", "link", "--node", "parent", "--target", "child", "--relationship", "parent_of"},
	} {
		runCLI(t, "", args...)
	}
	var coverage roadmap.Coverage
	if err := json.Unmarshal([]byte(runCLI(t, "", "roadmap", "view", "coverage", "--limit", "1", "--output", "json")), &coverage); err != nil {
		t.Fatal(err)
	}
	if coverage.CurrentNodes != 2 || coverage.NodesWithCurrentBindings != 1 || coverage.NodesWithoutCurrentBindings != 1 || coverage.CurrentRevisionBindings != 1 || coverage.CurrentRelationships != 1 || coverage.HierarchyRoots != 1 || coverage.Limit != 1 || coverage.Total != 2 || coverage.Returned != 1 || coverage.Omitted != 1 || !coverage.Truncated || len(coverage.Nodes) != 1 || coverage.Nodes[0].ID != "parent" || len(coverage.Nodes[0].Children) != 1 || coverage.Nodes[0].Children[0].ID != "child" {
		t.Fatalf("unexpected coverage JSON: %#v", coverage)
	}
	textOutput := runCLI(t, "", "roadmap", "view", "coverage", "--offset", "1", "--limit", "1", "--output", "text")
	for _, want := range []string{"Nodes with current-revision bindings: 1", "Nodes without current-revision bindings: 1", "node:child@1 [active] Child source=AGENTS.md source_state=regular_file", "source_comparison=unsupported_reference", "bindings=0", "Scope evidence truncated: false", "no_current_revision_binding", "Source paths checked: true", "observed touches come only from file anchors"} {
		if !strings.Contains(textOutput, want) {
			t.Fatalf("coverage text missing %q: %s", want, textOutput)
		}
	}
	var tree roadmap.RoadmapTree
	if err := json.Unmarshal([]byte(runCLI(t, "", "roadmap", "view", "tree", "--limit", "1", "--output", "json")), &tree); err != nil {
		t.Fatal(err)
	}
	if tree.Nodes != 2 || tree.Total != 1 || tree.Returned != 1 || tree.Truncated || len(tree.Entries) != 2 || tree.Entries[0].ID != "parent" || tree.Entries[1].ID != "child" || tree.Entries[1].ParentID != "parent" || tree.Entries[1].Depth != 1 {
		t.Fatalf("unexpected roadmap tree: %#v", tree)
	}
	treeText := runCLI(t, "", "roadmap", "view", "tree", "--output", "text")
	if !strings.Contains(treeText, "node:parent@1") || !strings.Contains(treeText, "  node:child@1") {
		t.Fatalf("unexpected roadmap tree text: %s", treeText)
	}
}

func TestRoadmapHistorySupportsBoundedRevisionTextAndJSON(t *testing.T) {
	testProject(t)
	runCLI(t, "", "init")
	runCLI(t, "", "roadmap", "node", "--id", "history-node", "--title", "First title", "--status", "proposed", "--source-path", "AGENTS.md", "--source-ref", "HEAD")
	runCLI(t, "", "roadmap", "node", "--id", "history-node", "--revision", "2", "--title", "Second title", "--status", "active", "--priority", "20", "--source-path", "CLAUDE.md", "--source-ref", "HEAD")
	var history roadmap.NodeHistory
	if err := json.Unmarshal([]byte(runCLI(t, "", "roadmap", "view", "history", "--id", "history-node", "--limit", "1", "--output", "json")), &history); err != nil {
		t.Fatal(err)
	}
	if history.NodeID != "history-node" || history.Total != 2 || history.Returned != 1 || history.Omitted != 1 || !history.Truncated || history.Revisions[0].Revision != 1 || !slices.Equal(history.Revisions[0].ChangedFields, []string{"initial_revision"}) {
		t.Fatalf("unexpected first history page: %#v", history)
	}
	textOutput := runCLI(t, "", "roadmap", "view", "history", "--id", "history-node", "--offset", "1", "--limit", "1", "--output", "text")
	for _, want := range []string{"Node: history-node", "rev:2 current [active]", "changed=priority,source_path,status,title", "Revision"} {
		if !strings.Contains(textOutput, want) {
			t.Fatalf("roadmap history text missing %q: %s", want, textOutput)
		}
	}
}

func TestPortableMemoryLocalFilesystemWorkflow(t *testing.T) {
	root := testProject(t)
	runCLI(t, "", "init")
	identityJSON := runCLI(t, "", "sync", "init")
	var identity struct {
		LineageID string `json:"lineage_id"`
	}
	if err := json.Unmarshal([]byte(identityJSON), &identity); err != nil {
		t.Fatal(err)
	}
	runCLI(t, "", "checkpoint", "create", "--task-id", "cli-task-1", "--task", "Portable CLI checkpoint", "--summary", "Preserve current work", "--next", "Exercise local recovery")
	evidenceJSON := runCLI(t, "", "narrative", "record", "--title", "Portable CLI", "--trigger", "manual", "--claims-json", `[{"kind":"outcome","text":"Exercise local export and drilldown.","authority":"caller_asserted"}]`)
	var recorded struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(evidenceJSON), &recorded); err != nil {
		t.Fatal(err)
	}
	bundleDirectory := filepath.Join(root, "portable-export")
	exported := runCLI(t, "", "memory", "export", "--directory", bundleDirectory)
	if !strings.Contains(exported, `"record_count": 6`) || !strings.Contains(exported, `"source_database_schema": "`+latestSchema+`"`) {
		t.Fatalf("unexpected export: %s", exported)
	}
	inspected := runCLI(t, "", "memory", "inspect", "--directory", bundleDirectory)
	if !strings.Contains(inspected, `"bodies_loaded": 0`) || !strings.Contains(inspected, identity.LineageID) {
		t.Fatalf("unexpected inspection: %s", inspected)
	}
	plannedJSON := runCLI(t, "", "memory", "plan", "--directory", bundleDirectory)
	var planned struct {
		ProjectID    string         `json:"project_id"`
		ApplyAllowed bool           `json:"apply_allowed"`
		Counts       map[string]int `json:"counts"`
	}
	if err := json.Unmarshal([]byte(plannedJSON), &planned); err != nil {
		t.Fatal(err)
	}
	if !planned.ApplyAllowed || planned.Counts["already_present"] != 6 {
		t.Fatalf("unexpected plan: %#v", planned)
	}
	refused := newRootCommand("llewm")
	refused.SetArgs([]string{"memory", "apply", "--directory", bundleDirectory})
	if err := refused.Execute(); err == nil || !strings.Contains(err.Error(), "explicit confirmation required") {
		t.Fatalf("apply did not require exact identities: %v", err)
	}
	applied := runCLI(t, "", "memory", "apply", "--directory", bundleDirectory, "--confirm-lineage", identity.LineageID, "--confirm-project", planned.ProjectID)
	if !strings.Contains(applied, `"already_present_records": 6`) || !strings.Contains(applied, `"writes_performed": 1`) {
		t.Fatalf("unexpected apply receipt: %s", applied)
	}
}

func TestScaffoldLifecycle(t *testing.T) {
	fixtures, err := filepath.Abs("../../testdata/sessions")
	if err != nil {
		t.Fatal(err)
	}
	configured := map[string]map[string]json.RawMessage{}
	for client, path := range map[string]string{"codex": "../../plugins/llewm/hooks/hooks.json", "claude": "../../plugins/claude-code/hooks/hooks.json"} {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var config struct {
			Hooks map[string]json.RawMessage `json:"hooks"`
		}
		if err := json.Unmarshal(body, &config); err != nil {
			t.Fatal(err)
		}
		configured[client] = config.Hooks
	}
	golden, err := os.ReadFile("../../testdata/audit/lifecycle.txt")
	if err != nil {
		t.Fatal(err)
	}
	root := testProject(t)
	runCLI(t, "", "init")
	runCLI(t, "", "doctor")
	runCLI(t, "", "checkpoint", "create", "--task-id", "cli-task-2", "--task", "Scaffold smoke test", "--next", "Verify startup receipt")
	for _, args := range [][]string{{"work"}, {"bootstrap"}, {"bootstrap", "--output", "json"}} {
		if got := runCLI(t, "", args...); !strings.Contains(got, "Scaffold smoke test") {
			t.Fatalf("missing checkpoint: %s", got)
		}
	}
	if text, explicit := runCLI(t, "", "bootstrap"), runCLI(t, "", "bootstrap", "--output", "text"); text != explicit {
		t.Fatalf("bootstrap --output text changed the default:\n%s\n%s", text, explicit)
	}
	if alias, explicit := runCLI(t, "", "bootstrap", "--output", "json"), runCLI(t, "", "bootstrap", "--output", "json"); alias != explicit {
		t.Fatalf("bootstrap JSON selectors differ:\n%s\n%s", alias, explicit)
	}
	invalidOutput := newRootCommand("llewm")
	invalidOutput.SetArgs([]string{"bootstrap", "--output", "yaml"})
	if err := invalidOutput.Execute(); err == nil || !strings.Contains(err.Error(), "output must be text or json") {
		t.Fatalf("bootstrap accepted invalid output: %v", err)
	}
	if got := runCLI(t, "", "work", "--output", "json"); !strings.Contains(got, `"recommended_next": "Verify startup receipt"`) || !strings.Contains(got, `"recommendation_source": "project_latest_checkpoint.next_action"`) || !strings.Contains(got, `"repository_authority": "machine_observed"`) {
		t.Fatalf("unexpected deterministic next output: %s", got)
	}
	if got := runCLI(t, "", "work", "--output", "json"); !strings.Contains(got, `"recommended_next": "Verify startup receipt"`) {
		t.Fatalf("next --output json did not preserve machine output: %s", got)
	}
	if got := runCLI(t, "", "work", "--output", "json"); !strings.Contains(got, `"task": "Scaffold smoke test"`) {
		t.Fatalf("status --output json did not expose the checkpoint: %s", got)
	}
	paths, err := filepath.Glob(filepath.Join(fixtures, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 15 {
		t.Fatalf("expected 15 lifecycle fixtures, got %d", len(paths))
	}
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		client := strings.Split(filepath.Base(path), "-")[0]
		out := runCLI(t, string(body), "hook", client)
		var event struct {
			Name string `json:"hook_event_name"`
		}
		if err := json.Unmarshal(body, &event); err != nil {
			t.Fatal(err)
		}
		delete(configured[client], event.Name)
		if event.Name == "SessionStart" {
			var receipt struct {
				Output struct {
					Name    string `json:"hookEventName"`
					Context string `json:"additionalContext"`
				} `json:"hookSpecificOutput"`
			}
			if err := json.Unmarshal([]byte(out), &receipt); err != nil {
				t.Fatal(err)
			}
			if receipt.Output.Name != "SessionStart" || !strings.Contains(receipt.Output.Context, "Scaffold smoke test") {
				t.Fatalf("invalid startup receipt: %s", out)
			}
		} else if out != "" {
			t.Fatalf("unexpected hook stdout: %s", out)
		}
	}
	for client, missing := range configured {
		if len(missing) != 0 {
			t.Fatalf("%s hook events without fixtures: %v", client, missing)
		}
	}
	if got := runCLI(t, "", "activity", "--limit", "5"); got != string(golden)+"Invocations: 5\n" {
		t.Fatalf("activity mismatch\nwant:\n%sInvocations: 5\ngot:\n%s", golden, got)
	}
	if got := runCLI(t, "", "activity", "--limit", "1", "--output", "json"); !strings.Contains(got, `"identity_source": "client_hook_claim"`) || !strings.Contains(got, `"summary"`) || strings.Contains(got, `"incidents"`) {
		t.Fatalf("unexpected activity JSON: %s", got)
	}
	invalid := newRootCommand("llewm")
	invalid.SetArgs([]string{"activity", "--limit", "101"})
	if err := invalid.Execute(); err == nil {
		t.Fatal("attribution command accepted an oversized read")
	}
	application, err := app.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	invocations, err := application.Attribution.RecentInvocations(context.Background(), application.ProjectID, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(invocations) != len(paths) {
		t.Fatalf("expected one attributable invocation per hook observation, got %d", len(invocations))
	}
	for _, invocation := range invocations {
		if invocation.IdentitySource != "client_hook_claim" {
			t.Fatalf("unexpected invocation provenance: %#v", invocation)
		}
	}
}

func TestCheckpointSkillsRequireToolDiscoveryAndControlledFallback(t *testing.T) {
	paths := []string{
		"../../plugins/llewm/skills/checkpoint/SKILL.md",
		"../../plugins/claude-code/skills/checkpoint/SKILL.md",
	}
	var first []byte
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, required := range []string{
			"inspect the client's complete or\nlazy-loaded tool catalog",
			"only when the user explicitly\nauthorizes it",
			"same structured checkpoint and\naction-reconciliation semantics as MCP",
			"never\n  collapse structured fields into the summary",
			"structured save receipt with `--output json`",
			"checkpoint is not degraded",
			"returns `deferred: true`",
			"does not require a second permission request",
			"`readback_verified: true` and `recovery_removed: true`",
		} {
			if !strings.Contains(string(body), required) {
				t.Fatalf("%s lacks checkpoint recovery contract %q", path, required)
			}
		}
		if first == nil {
			first = body
		} else if !bytes.Equal(first, body) {
			t.Fatalf("checkpoint skill safety contract differs between client packages")
		}
	}
}

func TestCheckpointCreateCLIPreservesStructuredMCPFields(t *testing.T) {
	testProject(t)
	runCLI(t, "", "init")
	first := strings.TrimSpace(runCLI(t, "", "checkpoint", "create", "--task-id", "parity", "--task", "Checkpoint parity", "--summary", "Initial state", "--next", "Finish parity", "--request-key", "parity-1"))

	var actions struct {
		Actions []checkpoint.Action `json:"actions"`
	}
	if err := json.Unmarshal([]byte(runCLI(t, "", "checkpoint", "actions", "--output", "json")), &actions); err != nil {
		t.Fatal(err)
	}
	if len(actions.Actions) != 1 {
		t.Fatalf("expected one predecessor action, got %#v", actions.Actions)
	}
	updates, err := json.Marshal([]checkpoint.ActionUpdate{{ID: actions.Actions[0].ID, Status: "completed", Reason: "parity slice finished"}})
	if err != nil {
		t.Fatal(err)
	}
	attempts := `[{"description":"Used an incomplete adapter","outcome":"rejected","reason":"lost structured fields"}]`
	args := []string{
		"checkpoint", "create", "--task-id", "parity", "--predecessor-checkpoint", first,
		"--task", "Checkpoint parity", "--status", "completed", "--summary", "All transports preserve semantics", "--next", "Task complete; await the next objective.",
		"--completed", "Added CLI parity, flags, and receipt", "--change", "cmd/llewm/main.go", "--verified", "CLI parity test passed, with commas",
		"--attempts-json", attempts, "--evidence-ref", "decision:checkpoint-cli-mcp-semantic-parity",
		"--action-updates-json", string(updates), "--request-key", "parity-2", "--output", "json",
	}
	dryRun := runCLI(t, "", append(args, "--dry-run")...)
	for _, want := range []string{`"ready": true`, `"reconciled_action_count": 1`, `"projected_unresolved_action_count": 0`} {
		if !strings.Contains(dryRun, want) {
			t.Fatalf("CLI dry run lacks %q: %s", want, dryRun)
		}
	}
	var receipt struct {
		ID            string              `json:"id"`
		Reconciled    []checkpoint.Action `json:"reconciled"`
		ActionCreated bool                `json:"action_created"`
	}
	if err := json.Unmarshal([]byte(runCLI(t, "", args...)), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.ID == "" || len(receipt.Reconciled) != 1 || receipt.ActionCreated {
		t.Fatalf("unexpected structured save receipt: %#v", receipt)
	}
	readback := runCLI(t, "", "checkpoint", "show", receipt.ID)
	for _, want := range []string{"- Added CLI parity, flags, and receipt\n", "cmd/llewm/main.go", "- CLI parity test passed, with commas\n", "Used an incomplete adapter", "lost structured fields", "decision:checkpoint-cli-mcp-semantic-parity"} {
		if !strings.Contains(readback, want) {
			t.Fatalf("CLI checkpoint readback lacks %q: %s", want, readback)
		}
	}
	if unresolved := runCLI(t, "", "checkpoint", "actions", "--output", "json"); !strings.Contains(unresolved, `"actions": []`) {
		t.Fatalf("completed CLI checkpoint left unresolved actions: %s", unresolved)
	}
}

func TestTaskLineageCLIAndCheckpointRetry(t *testing.T) {
	testProject(t)
	runCLI(t, "", "init")
	first := strings.TrimSpace(runCLI(t, "", "checkpoint", "create", "--task-id", "task-a", "--task", "Task A", "--next", "Continue A", "--request-key", "checkpoint-a-1"))
	transition := runCLI(t, "", "checkpoint", "task-transition", "--operation", "start", "--task-id", "task-a", "--checkpoint-id", first, "--request-key", "start-task-a")
	if !strings.Contains(transition, `"operation": "start"`) || !strings.Contains(transition, `"authority": "caller_asserted"`) {
		t.Fatalf("unexpected task transition: %s", transition)
	}
	lineage := runCLI(t, "", "checkpoint", "task-lineage", "--task-id", "task-a")
	if !strings.Contains(lineage, `"status": "active"`) || !strings.Contains(lineage, first) {
		t.Fatalf("unexpected task lineage: %s", lineage)
	}
	if alias := runCLI(t, "", "checkpoint", "task-lineage", "--task-id", "task-a", "--output", "json"); alias != lineage {
		t.Fatalf("task lineage default and --json differ:\n%s\n%s", lineage, alias)
	}
	if explicit := runCLI(t, "", "checkpoint", "task-lineage", "--task-id", "task-a", "--output", "json"); explicit != lineage {
		t.Fatalf("task lineage default and --output json differ:\n%s\n%s", lineage, explicit)
	}
	lineageText := runCLI(t, "", "checkpoint", "task-lineage", "--task-id", "task-a", "--limit", "1", "--output", "text")
	for _, want := range []string{"Task: task:task-a", "State: active managed=true origin=start", "Current tip: checkpoint:" + first, "Events: 1 (limit 1, truncated false)", "lifecycle:", "start", "task=task:task-a", "checkpoint=checkpoint:" + first, "Authority: mixed", "does not change selection or task state"} {
		if !strings.Contains(lineageText, want) {
			t.Fatalf("task lineage text missing %q: %s", want, lineageText)
		}
	}
	args := []string{"checkpoint", "create", "--task-id", "task-a", "--predecessor-checkpoint", first, "--task", "Task A", "--summary", "Strict continuation", "--next", "Continue again", "--request-key", "checkpoint-a-2"}
	second := strings.TrimSpace(runCLI(t, "", args...))
	replayed := strings.TrimSpace(runCLI(t, "", args...))
	if second == "" || replayed != second {
		t.Fatalf("checkpoint retry mismatch: first=%q replay=%q", second, replayed)
	}
	report := runCLI(t, "", "checkpoint", "task-lineage", "--limit", "5")
	if !strings.Contains(report, `"authority": "machine_observed"`) || !strings.Contains(report, `"anomalies": []`) {
		t.Fatalf("unexpected lineage report: %s", report)
	}
	if alias := runCLI(t, "", "checkpoint", "task-lineage", "--limit", "5", "--output", "json"); alias != report {
		t.Fatalf("lineage report default and --json differ:\n%s\n%s", report, alias)
	}
	if explicit := runCLI(t, "", "checkpoint", "task-lineage", "--limit", "5", "--output", "json"); explicit != report {
		t.Fatalf("lineage report default and --output json differ:\n%s\n%s", report, explicit)
	}
	reportText := runCLI(t, "", "checkpoint", "task-lineage", "--limit", "5", "--output", "text")
	for _, want := range []string{"Anomalous tasks: 0 (limit 5, truncated false)", "Authority: machine_observed", "does not infer intent, repair lineage, or change checkpoint selection"} {
		if !strings.Contains(reportText, want) {
			t.Fatalf("lineage report text missing %q: %s", want, reportText)
		}
	}
}

func TestLineageReportTextPreservesAnomalyReferences(t *testing.T) {
	var output bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&output)
	writeLineageReportText(cmd, checkpoint.LineageReport{
		Anomalies: []checkpoint.LineageAnomaly{{
			TaskID: "legacy", Codes: []string{"orphan", "multiple_tip"}, CheckpointCount: 3,
			RootCount: 2, TipCount: 2, SelectedCheckpointID: "tip", SelectedStatus: "paused",
			LifecycleStatus: "superseded", ReplacementTaskID: "replacement",
		}},
		Limit: 1, Truncated: true, Authority: "machine_observed", Disclaimer: "read only",
	})
	for _, want := range []string{"Anomalous tasks: 1 (limit 1, truncated true)", "task:legacy", "codes=orphan,multiple_tip", "checkpoints=3 roots=2 tips=2", "lifecycle=superseded", "selected=checkpoint:tip(paused)", "replacement=task:replacement", "Authority: machine_observed", "read only"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("lineage report text missing %q: %s", want, output.String())
		}
	}
}

func TestProcedureRecordAndShowCLI(t *testing.T) {
	root := testProject(t)
	runCLI(t, "", "init")
	tip := strings.TrimSpace(runCLI(t, "", "checkpoint", "create", "--task-id", "monitor-task", "--task", "Monitor task", "--next", "Run checks"))
	runCLI(t, "", "checkpoint", "task-transition", "--operation", "start", "--task-id", "monitor-task", "--checkpoint-id", tip, "--request-key", "start-monitor-task")
	procedurePath := filepath.Join(root, "procedure.json")
	procedureBody := `{"id":"go-checks","title":"Go checks","purpose":"Verify source","status":"accepted","platforms":["linux"],"working_directory":"components/continuity","steps":[{"id":"test","description":"Run Go tests","stage_kind":"source_check","stage_id":"go-test","argv_template":["go","test","./..."]}],"source":"docs/procedure-monitoring.md","source_ref":"HEAD"}`
	if err := os.WriteFile(procedurePath, []byte(procedureBody), 0o600); err != nil {
		t.Fatal(err)
	}
	recorded := runCLI(t, "", "procedure", "record", "--file", procedurePath)
	if !strings.Contains(recorded, `"revision": 1`) || !strings.Contains(recorded, `"kind": "check"`) {
		t.Fatalf("unexpected procedure record: %s", recorded)
	}
	shown := runCLI(t, "", "procedure", "show", "go-checks")
	if !strings.Contains(shown, `"stage_id": "go-test"`) || !strings.Contains(shown, `"authority": "caller_asserted"`) {
		t.Fatalf("unexpected procedure show: %s", shown)
	}
	if explicit := runCLI(t, "", "procedure", "show", "go-checks", "--output", "json"); explicit != shown {
		t.Fatal("procedure show JSON default changed")
	}
	if human := runCLI(t, "", "procedure", "show", "go-checks", "--output", "text"); !strings.Contains(human, "Procedure: procedure:go-checks@1") || !strings.Contains(human, "stage=source_check:go-test") || !strings.Contains(human, "does not execute") {
		t.Fatalf("procedure show text lost exact stage or execution boundary: %s", human)
	}
}

func TestMarkdownContractsCLI(t *testing.T) {
	root := testProject(t)
	runCLI(t, "", "init")
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, "RULES.md"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("# Rules\n\n<!-- continuity:contract pre-release -->\n## Pre-release changes\n\nkind: directive\nexpiry: public release\n\nPrefer redesign over compatibility.\n\n## Other\n\nNot part of it.\n")
	list := runCLI(t, "", "contract", "list")
	if !strings.Contains(list, "pre-release\tdirective\tnew\tRULES.md:3\tPre-release changes") {
		t.Fatalf("list:\n%s", list)
	}
	show := runCLI(t, "", "contract", "show", "pre-release")
	if !strings.Contains(show, "Expiry: public release") || !strings.HasSuffix(strings.TrimSpace(show), "Prefer redesign over compatibility.") || strings.Contains(show, "Not part of it") {
		t.Fatalf("show:\n%s", show)
	}
	if accepted := runCLI(t, "", "contract", "check", "--accept"); !strings.Contains(accepted, "Accepted 1 contract changes") {
		t.Fatalf("accept: %s", accepted)
	}
	if check := runCLI(t, "", "contract", "check"); !strings.Contains(check, "Contracts: 0, drifted: 0.") {
		t.Fatalf("clean check: %s", check)
	}
	write("# Rules\n\n<!-- continuity:contract pre-release -->\n## Pre-release changes\n\nkind: directive\n\nPrefer correctness over compatibility.\n")
	if check := runCLI(t, "", "contract", "check"); !strings.Contains(check, "pre-release\tdirective\tchanged") {
		t.Fatalf("changed check: %s", check)
	}
	hook := runCLI(t, `{"session_id":"s","hook_event_name":"SessionStart","source":"startup"}`, "hook", "claude-code")
	if !strings.Contains(hook, "**Contracts (machine-observed):** Contracts drifted since last indexed: 1 changed (pre-release)") {
		t.Fatalf("bootstrap without the drift line:\n%s", hook)
	}
	write("# Rules\n")
	if check := runCLI(t, "", "contract", "check"); !strings.Contains(check, "pre-release\tdirective\tmissing\tRULES.md (not found)") {
		t.Fatalf("missing check: %s", check)
	}
}

func TestReminderHookClientContracts(t *testing.T) {
	type handler struct {
		Command                string `json:"command"`
		Async                  *bool  `json:"async"`
		AdditionalContextLimit *int   `json:"additionalContextLimit"`
	}
	type group struct {
		Hooks []handler `json:"hooks"`
	}
	type hookConfig struct {
		Hooks map[string][]group `json:"hooks"`
	}
	read := func(path string) handler {
		t.Helper()
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var config hookConfig
		if err := json.Unmarshal(body, &config); err != nil {
			t.Fatal(err)
		}
		groups := config.Hooks["PostToolUse"]
		if len(groups) != 1 || len(groups[0].Hooks) != 1 {
			t.Fatalf("%s must define exactly one PostToolUse handler", path)
		}
		return groups[0].Hooks[0]
	}

	codex := read("../../plugins/llewm/hooks/hooks.json")
	if codex.Command != "llewm hook codex" || codex.Async == nil || !*codex.Async || codex.AdditionalContextLimit == nil || *codex.AdditionalContextLimit != 128 {
		t.Fatalf("unexpected Codex reminder hook contract: %#v", codex)
	}
	claude := read("../../plugins/claude-code/hooks/hooks.json")
	if claude.Command != "llewm hook claude-code" || claude.Async != nil || claude.AdditionalContextLimit != nil {
		t.Fatalf("unexpected Claude reminder hook contract: %#v", claude)
	}
}

func TestHookSurfacesOneConfiguredCheckpointReminder(t *testing.T) {
	root := testProject(t)
	runCLI(t, "", "init")
	configPath := filepath.Join(root, ".llewm/continuity", "config.toml")
	if err := os.WriteFile(configPath, []byte("[checkpoint_reminder]\nremind_after_events = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runCLI(t, "", "checkpoint", "create", "--task-id", "cli-task-3", "--session", "session-a", "--task", "Reminder hook", "--next", "Continue")
	payload := `{"session_id":"session-a","hook_event_name":"PostToolUse","tool_name":"Read"}`
	first := runCLI(t, payload, "hook", "codex")
	for _, want := range []string{`"hookEventName": "PostToolUse"`, `"additionalContext": "Continuity advisory:`, `event_threshold`} {
		if !strings.Contains(first, want) {
			t.Fatalf("reminder output missing %q: %s", want, first)
		}
	}
	if second := runCLI(t, payload, "hook", "codex"); second != "" {
		t.Fatalf("repeated reminder before a new checkpoint: %s", second)
	}
	runCLI(t, "", "checkpoint", "create", "--task-id", "cli-task-3", "--predecessor-checkpoint", taskTip(t, "cli-task-3"), "--session", "session-a", "--task", "Reminder hook", "--next", "Continue again")
	if rearmed := runCLI(t, payload, "hook", "claude-code"); !strings.Contains(rearmed, `"hookEventName": "PostToolUse"`) {
		t.Fatalf("new checkpoint did not rearm hook reminder: %s", rearmed)
	}
}

func TestSessionStartRoadmapScanIsOptInAndFailureIsolated(t *testing.T) {
	root := testProject(t)
	setTestAuthor(t, root)
	runCLI(t, "", "init")
	runCLI(t, "", "checkpoint", "create", "--task-id", "cli-task-4", "--task", "Roadmap scan hook", "--next", "Continue")
	sessionStart := `{"session_id":"session-roadmap","hook_event_name":"SessionStart","source":"startup"}`

	out := runCLI(t, sessionStart, "hook", "codex")
	if strings.Contains(out, "Continuity roadmap scan") {
		t.Fatalf("roadmap scan context leaked while disabled: %s", out)
	}
	if !strings.Contains(out, "Narrative coverage (machine-observed): 1 recent commit has no work narrative:") || !strings.Contains(out, "Initialize test project") {
		t.Fatalf("session start lacks narrative coverage: %s", out)
	}

	configPath := filepath.Join(root, ".llewm/continuity", "config.toml")
	if err := os.WriteFile(configPath, []byte("[roadmap_scan]\non_session_start = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runCLI(t, "", "roadmap", "node", "--id", "continuity.scan", "--title", "Roadmap scan", "--status", "active", "--priority", "10", "--source-path", "AGENTS.md", "--source-ref", "HEAD")
	runCLI(t, "", "roadmap", "link", "--kind", "file", "--node", "continuity.scan", "--path", "AGENTS.md", "--relationship", "contract")
	runCLI(t, "", "roadmap", "sync", "--client", "codex", "--model", "model-a", "--reviewed-node", "continuity.scan", "--reviewed-file", "AGENTS.md")

	if out := runCLI(t, sessionStart, "hook", "codex"); strings.Contains(out, "Continuity roadmap scan") {
		t.Fatalf("roadmap scan surfaced current, not-due state: %s", out)
	}
	runCLI(t, "", "roadmap", "sync", "--routine", "session-start", "--title", "SessionStart review", "--status", "accepted", "--changed-files-threshold", "1", "--source", "docs/roadmap.md")

	agents := filepath.Join(root, "AGENTS.md")
	body, err := os.ReadFile(agents)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agents, append(body, []byte("\nMore rules.\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	changed := runCLI(t, sessionStart, "hook", "claude-code")
	for _, want := range []string{"Continuity roadmap scan (machine-observed)", "freshness=changed", "review_due=true", "due_reasons=changed_files_threshold", "Roadmap meaning was not reevaluated", "review packet is available on request"} {
		if !strings.Contains(changed, want) {
			t.Fatalf("changed roadmap state missing %q: %s", want, changed)
		}
	}

	gitDir := filepath.Join(root, ".git")
	realGitDir := gitDir + "-real"
	if err := os.Rename(gitDir, realGitDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(gitDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		os.RemoveAll(gitDir)
		os.Rename(realGitDir, gitDir)
	})
	failed := runCLI(t, sessionStart, "hook", "codex")
	if strings.Contains(failed, "Continuity roadmap scan") {
		t.Fatalf("roadmap scan context surfaced despite a failed scan: %s", failed)
	}
	var receipt struct {
		Output struct {
			Name    string `json:"hookEventName"`
			Context string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(failed), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Output.Name != "SessionStart" || !strings.Contains(receipt.Output.Context, "Roadmap scan hook") {
		t.Fatalf("bootstrap delivery not preserved when the roadmap scan failed: %s", failed)
	}
}

func TestVersionDoesNotRequireProject(t *testing.T) {
	t.Chdir(t.TempDir())
	text := runCLI(t, "", "version")
	for _, want := range []string{"Version:", "Commit:", "Go:", "MCP API:", "Writer API:", "Executable:"} {
		if !strings.Contains(text, want) {
			t.Fatalf("version output missing %q: %s", want, text)
		}
	}
	var info struct {
		ApplicationVersion string `json:"application_version"`
		GitCommit          string `json:"git_commit"`
		MCPAPIVersion      int    `json:"mcp_api_version"`
		WriterAPIVersion   int    `json:"writer_api_version"`
	}
	if err := json.Unmarshal([]byte(runCLI(t, "", "version", "--output", "json")), &info); err != nil {
		t.Fatal(err)
	}
	if info.ApplicationVersion == "" || info.GitCommit == "" || info.MCPAPIVersion != buildinfo.MCPAPIVersion || info.WriterAPIVersion != buildinfo.WriterAPIVersion {
		t.Fatalf("incomplete JSON version output: %#v", info)
	}
	if err := json.Unmarshal([]byte(runCLI(t, "", "version", "--output", "json")), &info); err != nil {
		t.Fatalf("version --output json failed: %v", err)
	}
}

func TestReadinessReportsMissingRuntimeWithoutClaimingOperationalVerification(t *testing.T) {
	testProject(t)
	runCLI(t, "", "init")
	var report struct {
		Stage   string `json:"stage"`
		Runtime struct {
			Observed bool   `json:"observed"`
			Reason   string `json:"reason"`
		} `json:"runtime"`
		UnmetGates []string `json:"unmet_gates"`
	}
	if err := json.Unmarshal([]byte(runCLI(t, "", "doctor", "--readiness")), &report); err != nil {
		t.Fatal(err)
	}
	if report.Stage != "unknown" || report.Runtime.Observed || !strings.Contains(report.Runtime.Reason, "no long-running MCP process") || !slices.Contains(report.UnmetGates, "running MCP identity unavailable") {
		t.Fatalf("unexpected CLI readiness report: %#v", report)
	}
}

func TestMemoryIdentityRequiresExplicitInitialization(t *testing.T) {
	testProject(t)
	runCLI(t, "", "init")
	if got := runCLI(t, "", "sync", "status"); !strings.Contains(got, "Identity: none") {
		t.Fatalf("unexpected uninitialized identity: %s", got)
	}
	created := runCLI(t, "", "sync", "init")
	if !strings.Contains(created, `"lineage_id": "lineage:`) || !strings.Contains(created, `"association_source": "generated"`) {
		t.Fatalf("unexpected generated identity: %s", created)
	}
	if got := runCLI(t, "", "sync", "status", "--output", "json"); !strings.Contains(got, `"lineage_id": "lineage:`) {
		t.Fatalf("identity was not retained: %s", got)
	}

	second := testProject(t)
	t.Chdir(second)
	runCLI(t, "", "init")
	lineage := "lineage:0123456789abcdef0123456789abcdef"
	if got := runCLI(t, "", "sync", "init", "--lineage", lineage); !strings.Contains(got, `"lineage_id": "`+lineage+`"`) || !strings.Contains(got, `"association_source": "explicit_adoption"`) {
		t.Fatalf("unexpected adopted identity: %s", got)
	}
}

func TestExplicitProjectRoot(t *testing.T) {
	root := testProject(t)
	nested := filepath.Join(root, "components", "example")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	runCLI(t, "", "init")
	runCLI(t, "", "checkpoint", "create", "--task-id", "cli-task-5", "--task", "Explicit project", "--next", "Continue")
	t.Chdir(t.TempDir())
	if got := runCLI(t, "", "--project-root", nested, "work"); !strings.Contains(got, "Explicit project") {
		t.Fatalf("explicit project selection failed: %s", got)
	}
	cmd := newRootCommand("llewm")
	cmd.SetArgs([]string{"--project-root", filepath.Join(root, "missing"), "work"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "project root") {
		t.Fatalf("invalid explicit project root returned %v", err)
	}
}

func TestRoadmapCLIRecordsAndReportsDeterministicStatus(t *testing.T) {
	testProject(t)
	runCLI(t, "", "init")
	node := runCLI(t, "", "roadmap", "node", "--id", "continuity.roadmap", "--title", "Roadmap control", "--status", "active", "--priority", "10", "--source-path", "AGENTS.md", "--source-ref", "HEAD")
	if !strings.Contains(node, `"authority": "caller_asserted"`) || !strings.Contains(node, `"revision": 1`) {
		t.Fatalf("unexpected roadmap node: %s", node)
	}
	runCLI(t, "", "checkpoint", "create", "--task-id", "legacy-cli", "--task", "Legacy CLI task", "--status", "completed", "--next", "Done")
	runCLI(t, "", "roadmap", "link", "--kind", "file", "--node", "continuity.roadmap", "--path", "AGENTS.md", "--relationship", "contract")
	runCLI(t, "", "roadmap", "node", "--id", "continuity.future", "--title", "Future roadmap", "--status", "deferred", "--priority", "30", "--source-path", "AGENTS.md")
	link := runCLI(t, "", "roadmap", "link", "--node", "continuity.roadmap", "--target", "continuity.future", "--relationship", "parallel_with")
	if !strings.Contains(link, `"relationship": "parallel_with"`) {
		t.Fatalf("unexpected roadmap link: %s", link)
	}
	routine := runCLI(t, "", "roadmap", "sync", "--routine", "standard", "--title", "Standard review", "--status", "accepted", "--commit-threshold", "5", "--source", "docs/roadmap.md")
	if !strings.Contains(routine, `"commit_threshold": 5`) {
		t.Fatalf("unexpected roadmap routine: %s", routine)
	}
	sync := runCLI(t, "", "roadmap", "sync", "--client", "codex", "--model", "model-a", "--reviewed-node", "continuity.roadmap", "--reviewed-file", "AGENTS.md")
	if !strings.Contains(sync, `"git_commit":`) || !strings.Contains(sync, `"authority": "caller_asserted"`) {
		t.Fatalf("unexpected roadmap sync: %s", sync)
	}
	status := runCLI(t, "", "roadmap", "view", "status", "--output", "json")
	for _, want := range []string{`"freshness": "current"`, `"commits_since_sync": 0`, `"changed_files": []`, "roadmap meaning was not reevaluated now"} {
		if !strings.Contains(status, want) {
			t.Fatalf("roadmap status missing %q: %s", want, status)
		}
	}
	if statusOutput := runCLI(t, "", "roadmap", "view", "status", "--output", "json"); !strings.Contains(statusOutput, `"freshness": "current"`) {
		t.Fatalf("roadmap status --output json failed: %s", statusOutput)
	}
	overview := runCLI(t, "", "roadmap", "view", "overview", "--output", "json")
	for _, want := range []string{`"current_focus"`, `"future"`, `"parallel_with"`, `"changed_nodes_since_sync"`, `"tracks"`, `"review_triggers"`, `"state": "pending"`} {
		if !strings.Contains(overview, want) {
			t.Fatalf("roadmap overview missing %q: %s", want, overview)
		}
	}
	if overviewOutput := runCLI(t, "", "roadmap", "view", "overview", "--output", "json"); !strings.Contains(overviewOutput, `"current_focus"`) {
		t.Fatalf("roadmap overview --output json failed: %s", overviewOutput)
	}
	humanOverview := runCLI(t, "", "roadmap", "view", "overview")
	for _, want := range []string{"Track: [continuity.roadmap]", "Parallel with: continuity.future(deferred)", "Trigger: commits state=pending observed=0 threshold=5"} {
		if !strings.Contains(humanOverview, want) {
			t.Fatalf("human roadmap overview missing %q: %s", want, humanOverview)
		}
	}
	inventory := runCLI(t, "", "work", "inventory", "--limit", "10")
	if alias := runCLI(t, "", "work", "inventory", "--limit", "10", "--output", "json"); alias != inventory {
		t.Fatalf("roadmap inventory default and --json differ:\n%s\n%s", inventory, alias)
	}
	if explicit := runCLI(t, "", "work", "inventory", "--limit", "10", "--output", "json"); explicit != inventory {
		t.Fatalf("roadmap inventory default and --output json differ:\n%s\n%s", inventory, explicit)
	}
	inventoryText := runCLI(t, "", "work", "inventory", "--filter", "everything", "--limit", "1", "--ref-limit", "1", "--output", "text")
	for _, want := range []string{"Tracks: 1 of 3", "offset 0, limit 1, omitted 2, truncated true", "Filter: everything", "node:continuity.roadmap", "binding=missing", "checkpoint=-", "actions=0/0", "work_items=0/0", "refs_truncated=false", "Authority: mixed", "not selection, scheduling, or authority to execute work"} {
		if !strings.Contains(inventoryText, want) {
			t.Fatalf("roadmap inventory text missing %q: %s", want, inventoryText)
		}
	}
	rendered := runCLI(t, "", "roadmap", "view", "render")
	for _, want := range []string{"Generated by continuity-roadmap-markdown v2", "Graph hash:", "## Active tracks", "## Deferred work"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("roadmap projection missing %q: %s", want, rendered)
		}
	}
	if explicit := runCLI(t, "", "roadmap", "view", "render", "--output", "text"); explicit != rendered {
		t.Fatalf("roadmap render --output text changed the default:\n%s\n%s", rendered, explicit)
	}
	if alias, explicit := runCLI(t, "", "roadmap", "view", "render", "--output", "json"), runCLI(t, "", "roadmap", "view", "render", "--output", "json"); alias != explicit {
		t.Fatalf("roadmap render JSON selectors differ:\n%s\n%s", alias, explicit)
	}
	check := runCLI(t, "", "roadmap", "view", "check", "--path", "docs/missing-generated-roadmap.md", "--output", "json")
	if !strings.Contains(check, `"state": "missing"`) || !strings.Contains(check, `"expected_hash":`) {
		t.Fatalf("unexpected projection check: %s", check)
	}
	if explicit := runCLI(t, "", "roadmap", "view", "check", "--path", "docs/missing-generated-roadmap.md", "--output", "json"); explicit != check {
		t.Fatalf("roadmap check JSON selectors differ:\n%s\n%s", check, explicit)
	}
	packet := runCLI(t, "", "roadmap", "review-packet", "--manual", "--max-bytes", "1024")
	if !strings.Contains(packet, `"trigger": "manual"`) || !strings.Contains(packet, `"sources"`) {
		t.Fatalf("unexpected manual review packet: %s", packet)
	}
	if explicit := runCLI(t, "", "roadmap", "review-packet", "--manual", "--max-bytes", "1024", "--output", "json"); explicit != packet {
		t.Fatal("review-packet JSON default changed")
	}
	packetText := runCLI(t, "", "roadmap", "review-packet", "--manual", "--max-bytes", "1024", "--output", "text")
	for _, want := range []string{"trigger=manual", "content_bytes=", "Source:", "sha256=", "does not invoke a model"} {
		if !strings.Contains(packetText, want) {
			t.Fatalf("review packet text missing %q: %s", want, packetText)
		}
	}
}

const coverageTestRequirements = "# Requirements\n\n<!-- continuity:requirement session-start -->\n## Session start\nscope: claude-code, codex\nA fresh session receives the bootstrap.\n\n<!-- continuity:requirement checkpoint-hook -->\n## Checkpoint hook\nHooks record events.\n"

func writeCoverageRequirements(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "requirements.md"), []byte(coverageTestRequirements), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "docs/requirements.md"}, {"-c", "user.name=Continuity Test", "-c", "user.email=test@example.invalid", "commit", "-q", "-m", "Add requirements"}} {
		command := exec.Command("git", args...)
		command.Dir = root
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
}

func TestCoverageCLIRecordsAndReadsMatrix(t *testing.T) {
	root := testProject(t)
	runCLI(t, "", "init")
	writeCoverageRequirements(t, root)
	created := runCLI(t, "", "coverage", "record", "--requirement", "session-start", "--client", "codex", "--platform", "linux", "--client-version", "1.2", "--status", "supported", "--verification", "observed", "--implementation", "SessionStart hook token=coverage-secret", "--ref", "commit:abc1234")
	if !strings.Contains(created, `"requirement_id": "session-start"`) || strings.Contains(created, "coverage-secret") {
		t.Fatalf("unexpected coverage record output: %s", created)
	}
	listed := runCLI(t, "", "coverage", "--output", "json")
	for _, want := range []string{`"id": "session-start"`, `"status": "supported"`, `"id": "checkpoint-hook"`, `"status": "unknown"`, `"verification": "observed"`} {
		if !strings.Contains(listed, want) {
			t.Fatalf("coverage matrix missing %s: %s", want, listed)
		}
	}
	text := runCLI(t, "", "coverage", "--client", "codex")
	if !strings.Contains(text, "session-start [supported]") || !strings.Contains(text, "codex/linux supported (observed") {
		t.Fatalf("unexpected coverage text: %s", text)
	}
	for _, args := range [][]string{
		{"coverage", "record", "--requirement", "missing", "--client", "codex", "--platform", "linux", "--status", "supported", "--verification", "observed"},
		{"coverage", "record", "--requirement", "session-start", "--client", "codex", "--platform", "linux", "--status", "documented", "--verification", "observed"},
		{"coverage", "--history"},
	} {
		invalid := newRootCommand("llewm")
		invalid.SetArgs(args)
		if err := invalid.Execute(); err == nil {
			t.Fatalf("coverage accepted invalid arguments: %v", args)
		}
	}
}

func TestHookRejectsInvalidInput(t *testing.T) {
	testProject(t)
	for _, input := range []string{"null", "[]", "{", `{}` + strings.Repeat(" ", 2<<20)} {
		cmd := newRootCommand("llewm")
		cmd.SetArgs([]string{"hook", "codex", "SessionStart"})
		cmd.SetIn(strings.NewReader(input))
		if err := cmd.Execute(); err == nil {
			t.Fatalf("accepted invalid input of length %d", len(input))
		}
	}
	if _, err := os.Stat(".llewm/continuity"); !os.IsNotExist(err) {
		t.Fatalf("invalid input created state: %v", err)
	}
}

func TestHookRefusesPendingMigrationWithoutWriting(t *testing.T) {
	fixture, err := filepath.Abs("../../testdata/sessions/codex-post-tool.json")
	if err != nil {
		t.Fatal(err)
	}
	root := testProject(t)
	runCLI(t, "", "init")
	databasePath := filepath.Join(root, ".llewm/continuity", "continuity.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath)+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	var eventsBefore int
	if err := db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&eventsBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM schema_migrations WHERE version=?`, latestSchema); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	status := runCLI(t, "", "database", "status")
	if !strings.Contains(status, `"state": "pending"`) || !strings.Contains(status, `"pending_migrations": [`) || !strings.Contains(status, latestSchema) {
		t.Fatalf("unexpected pending database status: %s", status)
	}
	for _, args := range [][]string{{"work"}, {"mcp"}} {
		command := newRootCommand("llewm")
		command.SetArgs(args)
		if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "migration_required") {
			t.Fatalf("%v did not report migration_required: %v", args, err)
		}
	}
	body, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	command := newRootCommand("llewm")
	command.SetIn(bytes.NewReader(body))
	command.SetArgs([]string{"hook", "codex"})
	err = command.Execute()
	if err == nil || !strings.Contains(err.Error(), "migration_required") {
		t.Fatalf("hook did not report migration_required: %v", err)
	}
	db, err = sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var eventsAfter, latestApplied int
	if err := db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&eventsAfter); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=?`, latestSchema).Scan(&latestApplied); err != nil {
		t.Fatal(err)
	}
	if eventsAfter != eventsBefore || latestApplied != 0 {
		t.Fatalf("hook changed pending database: events %d -> %d, latest migration rows %d", eventsBefore, eventsAfter, latestApplied)
	}
}

func TestDatabaseBackupUsesGeneratedOrExplicitDestination(t *testing.T) {
	root := testProject(t)
	runCLI(t, "", "init")

	var generated database.BackupResult
	if err := json.Unmarshal([]byte(runCLI(t, "", "database", "backup")), &generated); err != nil {
		t.Fatal(err)
	}
	if generated.BackupIntegrity != "ok" || generated.BackupMigration != latestSchema || filepath.Dir(generated.BackupPath) != filepath.Join(root, ".llewm/continuity", "backups") {
		t.Fatalf("generated backup receipt: %#v", generated)
	}
	var second database.BackupResult
	if err := json.Unmarshal([]byte(runCLI(t, "", "database", "backup")), &second); err != nil {
		t.Fatal(err)
	}
	if second.BackupPath == generated.BackupPath {
		t.Fatalf("generated backup path was reused: %s", second.BackupPath)
	}

	explicit := filepath.Join(root, "manual", "named.db")
	var named database.BackupResult
	if err := json.Unmarshal([]byte(runCLI(t, "", "database", "backup", "--backup", explicit)), &named); err != nil {
		t.Fatal(err)
	}
	if named.BackupPath != explicit || named.BackupIntegrity != "ok" {
		t.Fatalf("explicit backup receipt: %#v", named)
	}
}

func TestDatabaseBackupPruneIsDryRun(t *testing.T) {
	root := testProject(t)
	runCLI(t, "", "init")
	var backup database.BackupResult
	if err := json.Unmarshal([]byte(runCLI(t, "", "database", "backup")), &backup); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, ".llewm/continuity", "config.toml")
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	content = bytes.ReplaceAll(content, []byte("keep_last = 3"), []byte("keep_last = 0"))
	content = bytes.ReplaceAll(content, []byte("keep_days = 30"), []byte("keep_days = 0"))
	if err := os.WriteFile(configPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	var plan database.BackupRetentionPlan
	if err := json.Unmarshal([]byte(runCLI(t, "", "database", "backup", "prune")), &plan); err != nil {
		t.Fatal(err)
	}
	if !plan.DryRun || len(plan.PruneCandidates) != 1 || plan.PruneCandidates[0] != backup.BackupPath {
		t.Fatalf("prune plan: %#v", plan)
	}
	if _, err := os.Stat(backup.BackupPath); err != nil {
		t.Fatalf("dry-run removed backup: %v", err)
	}
	var receipt database.BackupPruneReceipt
	if err := json.Unmarshal([]byte(runCLI(t, "", "database", "backup", "prune", "--apply")), &receipt); err != nil {
		t.Fatal(err)
	}
	if !receipt.Applied || len(receipt.Removed) != 1 || receipt.Removed[0] != backup.BackupPath {
		t.Fatalf("prune receipt: %#v", receipt)
	}
	if _, err := os.Stat(backup.BackupPath); !os.IsNotExist(err) {
		t.Fatalf("applied prune retained backup: %v", err)
	}
}

func TestComponentUsesGitRootStateAndGuidance(t *testing.T) {
	root := testProject(t)
	t.Setenv("CONTINUITY_HOME", "")
	nested := filepath.Join(root, "components", "continuity")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("# Component guidance\n\nRead components/continuity/AGENTS.md before editing.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "AGENTS.md"), []byte("# Nested\n\nComponent-only rule.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runCLI(t, "", "init")
	runCLI(t, "", "checkpoint", "create", "--task-id", "cli-task-6", "--task", "Shared project checkpoint", "--next", "Check nested directory")
	want := runCLI(t, "", "bootstrap", "--output", "json")
	t.Chdir(nested)
	runCLI(t, "", "init")
	if got := runCLI(t, "", "bootstrap", "--output", "json"); got != want {
		t.Fatalf("component resolved different project state or instructions:\n%s", got)
	}
	if !strings.Contains(want, "components/continuity/AGENTS.md") || strings.Contains(want, "Component-only rule.") {
		t.Fatalf("unexpected instruction discovery scope:\n%s", want)
	}
	if _, err := os.Stat(filepath.Join(root, ".llewm/continuity", "continuity.db")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(nested, ".llewm/continuity")); !os.IsNotExist(err) {
		t.Fatalf("component should not have separate state: %v", err)
	}
}

// Run the real command in a child process, keeping the MCP stdout stream clean.
func TestMCPProcess(t *testing.T) {
	if os.Getenv("LLEWM_TEST_MCP_PROCESS") != "1" {
		return
	}
	cmd := newRootCommand("llewm")
	args := []string{"mcp"}
	if root := os.Getenv("LLEWM_TEST_PROJECT_ROOT"); root != "" {
		args = []string{"--project-root", root, "mcp"}
	}
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

// setTestAuthor makes the test project's commits the engineer's own, whatever
// the machine's global Git identity, so narrative coverage lists them.
func setTestAuthor(t *testing.T, root string) {
	t.Helper()
	if output, err := exec.Command("git", "-C", root, "config", "user.email", "test@example.invalid").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, output)
	}
}

func TestMCPStdio(t *testing.T) {
	root := testProject(t)
	setTestAuthor(t, root)
	runCLI(t, "", "init")
	writeCoverageRequirements(t, root)
	if err := os.WriteFile(filepath.Join(os.Getenv("CONTINUITY_GLOBAL_HOME"), "AGENTS.md"), []byte("# Commits\n\nEngineer-wide commit guidance."), 0o600); err != nil {
		t.Fatal(err)
	}
	runCLI(t, `{"session_id":"mcp-session","model":"model-a"}`, "hook", "codex", "PostToolUse")
	for i := 0; i < 2; i++ {
		sessionID := fmt.Sprintf("mcp-friction-%d", i)
		runCLI(t, fmt.Sprintf(`{"session_id":%q,"hook_event_name":"PostToolUseFailure","tool_name":"Bash","tool_input":{"command":"go test ./..."},"error":"go: failed to initialize build cache: permission denied"}`, sessionID), "hook", "claude")
		runCLI(t, fmt.Sprintf(`{"session_id":%q,"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"GOCACHE=/tmp/llewm-cache go test ./..."}}`, sessionID), "hook", "claude")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMCPProcess$")
	child.Dir = t.TempDir()
	child.Env = append(os.Environ(), "LLEWM_TEST_MCP_PROCESS=1", "LLEWM_TEST_PROJECT_ROOT="+root)
	client := mcp.NewClient(&mcp.Implementation{Name: "scaffold-smoke", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: child}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	list, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Tools) != expectedMCPToolCount {
		t.Fatalf("expected %d tools: %#v", expectedMCPToolCount, list.Tools)
	}
	for _, call := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"continuity_checkpoint", map[string]any{"task_id": "mcp-smoke", "session_id": "mcp-session", "task": "MCP smoke task", "summary": "Testing stdio token=fixture-mcp-secret", "next_action": "Verify bootstrap", "attempts": []map[string]any{{"description": "Earlier approach", "outcome": "failure", "reason": "Missing dependency"}}, "verified": []string{"Tests passed"}, "evidence_refs": []string{"event-reference"}}, `"unnarrated_commits":[{"commit":"`},
		{"continuity_checkpoint", map[string]any{"dry_run": true, "task_id": "prospective", "task": "Prospective MCP task", "summary": "Compile only", "next_action": "Save when ready"}, `"authority":"prospective_caller_asserted"`},
		{"cli", map[string]any{"argv": []string{"checkpoint", "task-transition", "--operation", "start", "--task-id", "completion-mcp", "--request-key", "start-completion-mcp"}}, ""},
		{"continuity_checkpoint", map[string]any{"task_id": "completion-mcp", "task": "Completion MCP task", "summary": "Needs finalization", "next_action": "Finalize"}, `"id"`},
		{"cli", map[string]any{"argv": []string{"procedure", "record", "--file", writeJSONFile(t, map[string]any{"id": "go-checks", "title": "Go checks", "purpose": "Verify source", "status": "accepted", "platforms": []string{"linux"}, "working_directory": "components/continuity", "steps": []map[string]any{{"id": "test", "description": "Run Go tests", "stage_kind": "source_check", "stage_id": "go-test", "argv_template": []string{"go", "test", "./..."}}}, "source": "docs/procedure-monitoring.md"})}}, ""},
		{"continuity_procedure", map[string]any{"id": "go-checks"}, `"stage_id":"go-test"`},
		{"continuity_procedure", map[string]any{"kind": "check"}, `"id":"go-checks"`},
		{"cli", map[string]any{"argv": []string{"procedure", "record", "--file", writeJSONFile(t, map[string]any{"id": "box-install", "kind": "runbook", "title": "Install on the box", "purpose": "Copy a build", "status": "proposed", "platforms": []string{"linux/amd64"}, "declared_effects": []string{"replaces the remote binary"}, "steps": []map[string]any{{"id": "copy", "description": "Copy the binary", "argv_template": []string{"scp", "{{out}}", "{{host}}:bin"}}}, "source": "docs/resources-and-runbooks.md"})}}, ""},
		{"continuity_procedure", map[string]any{"kind": "runbook"}, `"procedures":[{`},
		{"continuity_procedure", map[string]any{"id": "box-install"}, `"status":"proposed"`},
		{"cli", map[string]any{"argv": []string{"resource", "record", "box", "--kind", "host", "--title", "Test box", "--attr", "ssh=user@host", "--creator", "owner", "--note", "PATH lacks ~/.local/bin", "--note-authority", "machine_observed"}}, ""},
		{"continuity_resource", map[string]any{"id": "box"}, `"ssh":"user@host"`},
		{"continuity_resource", map[string]any{"kind": "host"}, `"resources":[{`},
		{"continuity_pin", map[string]any{"target": "resource:box", "scope": "project", "creator": "owner"}, `"action":"pin"`},
		{"continuity_pin", map[string]any{}, `"ref":"resource:box"`},
		{"continuity_checkpoint_actions", map[string]any{"task_id": "mcp-smoke"}, `"description":"Verify bootstrap"`},
		{"continuity_work", map[string]any{"view": "freshness", "task_id": "mcp-smoke", "limit": 1, "remind_after_events": 1}, `"reminder_evaluated":true`},
		{"continuity_checkpoint_get", map[string]any{"query": "smoke", "limit": 1}, `"authority":"caller_asserted"`},
		{"continuity_usage", map[string]any{"by": "family"}, `"by":"family"`},
		{"continuity_work", map[string]any{}, `"recommendation_source":"project_latest_checkpoint.next_action"`},
		{"continuity_work", map[string]any{"view": "summary", "limit": 10}, `"routing_decision":"project_latest"`},
		{"continuity_work", map[string]any{"view": "check", "scope": "current", "limit": 10}, `"authority":"machine_observed"`},
		{"continuity_work", map[string]any{"view": "list", "list": "active", "limit": 10}, `"view":"active"`},
		{"continuity_work", map[string]any{"view": "item", "identifier": "completion-mcp"}, `"identifier":"completion-mcp"`},
		{"continuity_presence", map[string]any{"live_only": true, "limit": 10}, `"authority":"machine_observed"`},
		// mcp-smoke's checkpoint leased it to mcp-session, so a session-less
		// bootstrap takes the free task instead of another session's.
		{"continuity_bootstrap", map[string]any{}, "Completion MCP task"},
		{"continuity_bootstrap", map[string]any{"task_id": "mcp-smoke"}, "Earlier approach"},
		{"continuity_bootstrap", map[string]any{"task_id": "mcp-smoke"}, "Verification claims (not independently verified)"},
		{"continuity_bootstrap", map[string]any{"task_id": "mcp-smoke"}, "event-reference"},
		{"continuity_bootstrap", map[string]any{"task_id": "mcp-smoke"}, "MCP smoke task"},
		{"continuity_bootstrap", map[string]any{"task_id": "missing-task"}, "Factual recovery fallback"},
		{"continuity_bootstrap", map[string]any{"task_id": "missing-task"}, "project activity cannot establish task intent or completion"},
		{"continuity_work", map[string]any{"view": "freshness", "task_id": "missing-task", "limit": 1}, `"events_observed":5`},
		{"continuity_work", map[string]any{"session_id": "mcp-session"}, `"recommendation_source":"explicit_checkpoint.next_action"`},
		{"continuity_bootstrap", map[string]any{}, "Engineer-wide commit guidance."},
		{"continuity_roadmap_node", map[string]any{"id": "continuity.roadmap", "title": "Roadmap control", "status": "active", "priority": 10, "source_path": "AGENTS.md", "source_ref": "HEAD"}, `"ref":"roadmap_node:continuity.roadmap@1"`},
		{"continuity_roadmap_node", map[string]any{"id": "continuity.future", "title": "Future roadmap", "status": "deferred", "priority": 30, "source_path": "AGENTS.md"}, `"status":"deferred"`},
		{"continuity_roadmap_node", map[string]any{"id": "release.node", "title": "Release capability", "status": "completed", "priority": 20, "source_path": "AGENTS.md"}, `"status":"completed"`},
		{"cli", map[string]any{"argv": []string{"roadmap", "sync", "--routine", "standard", "--title", "Standard review", "--status", "accepted", "--commit-threshold", "5", "--source", "docs/roadmap.md"}}, ""},
		{"cli", map[string]any{"argv": []string{"roadmap", "link", "--kind", "file", "--node", "continuity.roadmap", "--path", "AGENTS.md", "--path-kind", "file", "--relationship", "contract"}}, ""},
		{"cli", map[string]any{"argv": []string{"roadmap", "link", "--kind", "task", "--node", "continuity.roadmap", "--task", "mcp-smoke", "--reason", "Explicitly map differing stable IDs", "--request-key", "bind-mcp-roadmap-smoke"}}, ""},
		{"cli", map[string]any{"argv": []string{"roadmap", "link", "--node", "continuity.roadmap", "--target", "continuity.future", "--relationship", "parallel_with"}}, ""},
		{"cli", map[string]any{"argv": []string{"roadmap", "sync", "--client", "codex", "--model", "model-a", "--reviewed-node", "continuity.roadmap", "--reviewed-file", "AGENTS.md"}}, ""},
		{"continuity_roadmap_view", map[string]any{}, `"freshness":"current"`},
		{"continuity_roadmap_view", map[string]any{"view": "overview", "limit": 10}, `"parallel_with"`},
		{"continuity_roadmap_view", map[string]any{"view": "coverage", "limit": 10}, `"nodes_without_current_bindings"`},
		{"continuity_roadmap_view", map[string]any{"view": "history", "node_id": "continuity.roadmap", "limit": 5}, `"initial_revision"`},
		{"continuity_roadmap_view", map[string]any{"view": "tree", "limit": 10}, `"hierarchy_roots"`},
		{"continuity_work", map[string]any{"view": "inventory", "filter": "current_focus", "limit": 10}, `"selected_task_id":"mcp-smoke"`},
		{"continuity_roadmap_view", map[string]any{"view": "render"}, `"renderer_version":2`},
		{"continuity_roadmap_view", map[string]any{"view": "check", "path": "docs/missing-generated-roadmap.md"}, `"state":"missing"`},
		{"continuity_narrative_record", map[string]any{"id": "mcp-work", "title": "MCP work", "trigger": "accepted_decision", "claims": []map[string]any{{"kind": "decision", "text": "Use bounded narratives token=fixture-narrative-secret", "authority": "caller_asserted"}}, "anchors": []map[string]any{{"kind": "checkpoint", "target": "mcp-checkpoint", "authority": "caller_asserted"}}, "creator": "codex"}, `"ref":"narrative:mcp-work"`},
		{"continuity_release_record", map[string]any{"id": "unreleased", "version": "Unreleased", "title": "Draft", "status": "draft", "entries": []map[string]any{{"position": 1, "category": "feature", "wording": "Add release projections.", "node_id": "release.node", "node_revision": 1, "narrative_ids": []string{"mcp-work"}}}}, `"content_sha256"`},
		{"continuity_roadmap_view", map[string]any{"view": "changelog"}, `"draft":true`},
		{"continuity_narrative_record", map[string]any{"id": "mcp-commit", "title": "MCP commit", "commit": "HEAD", "claims": []map[string]any{{"kind": "outcome", "label": "What changed", "text": "Initialized", "authority": "caller_asserted"}, {"kind": "intent", "text": "Start", "authority": "caller_asserted"}, {"kind": "verification", "text": "None needed", "authority": "caller_asserted"}, {"kind": "limitation", "text": "None", "authority": "caller_asserted"}}, "creator": "codex", "skip_note": true}, `"action":"skipped"`},
		{"continuity_history_search", map[string]any{"linked": "checkpoint:mcp-checkpoint"}, `"ref":"narrative:mcp-work#0"`},
		{"continuity_history_show", map[string]any{"refs": []string{"narrative:mcp-work"}}, `Use bounded narratives`},
		{"continuity_item_capture", map[string]any{"id": "mcp-suggestion", "kind": "task", "title": "Persist the next steps", "body": "Resume without rederiving", "creator": "codex", "task_id": "mcp-smoke", "roadmap_node_id": "continuity.roadmap"}, `"status":"pending"`},
		{"continuity_item", map[string]any{"status": "open", "task_id": "mcp-smoke"}, `"id":"mcp-suggestion"`},
		{"continuity_track_plan_record", map[string]any{"id": "mcp-plan", "roadmap_node_id": "continuity.roadmap", "task_id": "mcp-smoke", "title": "MCP plan", "summary": "Persisted next steps", "status": "accepted", "creator": "human-review", "work_item_ids": []string{"mcp-suggestion"}, "items": []map[string]any{{"id": "schema", "position": 0, "title": "Persist schema", "detail": "", "status": "completed", "depends_on": []string{}, "evidence_refs": []string{}, "work_item_ids": []string{"mcp-suggestion"}}, {"id": "reads", "position": 1, "title": "Expose reads", "detail": "", "status": "pending", "depends_on": []string{"schema"}, "evidence_refs": []string{}, "work_item_ids": []string{"mcp-suggestion"}}}}, `"ref":"plan:mcp-plan@1"`},
		{"continuity_track_plan_record", map[string]any{"id": "mcp-proposal", "roadmap_node_id": "continuity.roadmap", "title": "MCP proposal", "summary": "Proposed steps", "status": "proposed", "creator": "agent", "model": "model-x", "items": []map[string]any{{"id": "one", "position": 1, "title": "One", "detail": "First step", "status": "pending", "depends_on": []string{}, "evidence_refs": []string{}, "work_item_ids": []string{}}}}, `"status":"proposed"`},
		{"continuity_track_plan_record", map[string]any{"id": "mcp-proposal", "status": "accepted", "creator": "human-review"}, `"ref":"plan:mcp-proposal@2","status":"accepted"`},
		{"continuity_track_plan", map[string]any{"task_id": "mcp-smoke"}, `"ready_count":1`},
		{"continuity_track_plan", map[string]any{"id": "plan:mcp-plan"}, `"outline":{`},
		{"continuity_track_plan", map[string]any{"id": "mcp-plan", "history": true}, `"revisions":[{`},
		{"continuity_track_plan", map[string]any{"id": "mcp-plan", "history": true, "detail": true}, `"history":[{`},
		{"continuity_track_plan", map[string]any{"id": "mcp-plan", "detail": true}, `"evidence_refs":[]`},
		{"continuity_track_plan", map[string]any{"id": "mcp-plan", "item": "reads"}, `"item":{"depends_on":["schema"],"detail"`},
		{"continuity_track_plan", map[string]any{"task_id": "mcp-smoke", "ready": true}, `"plan_id":"mcp-plan"`},
		{"continuity_item_capture", map[string]any{"id": "mcp-idea", "kind": "idea", "title": "MCP idea", "body": "Keep this tangent token=fixture-idea-secret", "tags": []string{"memory"}, "creator": "human"}, `"status":"pending"`},
		{"continuity_item", map[string]any{"id": "work_item:mcp-idea", "max_bytes": 2048}, `"title":"MCP idea"`},
		{"continuity_item_capture", map[string]any{"id": "mcp-report-bug", "kind": "bug", "title": "Missing bootstrap", "body": "Investigate", "observed": "Missing packet", "creator": "codex"}, `"status":"pending"`},
		{"continuity_item", map[string]any{"id": "mcp-report-bug", "report": true, "expected": "Receive packet", "hypothesis": "Hook trust"}, `"report_id":"sha256:`},
		{"continuity_item", map[string]any{"audit": true, "kind": "idea", "max_bytes": 2048}, `"pending_count":1`},
		{"continuity_item_disposition", map[string]any{"id": "work_item:mcp-idea", "status": "integrated", "reason": "Accepted after review", "target_refs": []string{"roadmap:mcp"}, "creator": "human"}, `"status":"integrated"`},
		{"continuity_item", map[string]any{"friction": true, "min_count": 2, "max_bytes": 2048}, `"candidate_count":1`},
		{"continuity_decision_record", map[string]any{"id": "mcp-decision", "title": "Keep local state", "rationale": "Reviewed boundary", "disposition": "accepted", "actor": "owner", "actor_type": "human", "authority": "user_reported", "review_source": "checkpoint:mcp-smoke", "links": []map[string]any{{"kind": "subject", "target_kind": "roadmap_node", "target_id": "mcp-node"}}}, `"ref":"decision:mcp-decision"`},
		{"continuity_history_search", map[string]any{"linked": "roadmap_node:mcp-node"}, `"ref":"decision:mcp-decision"`},
		{"continuity_history_search", map[string]any{"query": "boundary", "families": []string{"decision"}}, `"ref":"decision:mcp-decision"`},
		{"continuity_history_show", map[string]any{"refs": []string{"decision:mcp-decision"}}, `Reviewed boundary`},
		{"cli", map[string]any{"argv": []string{"topic", "record", "mcp-topic", "--title", "MCP topic", "--creator", "owner", "--alias", "mcp-alias"}}, ""},
		{"cli", map[string]any{"argv": []string{"topic", "record", "mcp-topic", "--link", "decision:mcp-decision", "--creator", "agent", "--model", "model-x"}}, ""},
		{"continuity_history_search", map[string]any{"topic": "mcp-alias"}, `"kind":"curated"`},
		{"continuity_lease", map[string]any{"action": "acquire", "scope": "task:mcp-lease", "holder": "mcp-agent", "ttl_seconds": 600}, `"token":1`},
		{"continuity_lease", map[string]any{"action": "renew", "scope": "task:mcp-lease", "holder": "mcp-agent", "token": 1}, `"renewals":1`},
		// The checkpoint saved with session mcp-session already leased its task to
		// that session, so the holder's acquire renews it.
		{"continuity_lease", map[string]any{"action": "acquire", "scope": "task:mcp-smoke", "holder": "codex", "session": "mcp-session"}, `"renewed":true`},
		{"continuity_bootstrap", map[string]any{"current_session_id": "mcp-session"}, "Chosen by presence: task:mcp-smoke has live presence from this session."},
		{"continuity_bootstrap", map[string]any{"current_session_id": "another-session"}, "Completion MCP task"},
		{"continuity_brief", map[string]any{"ref": "mcp-plan/reads"}, `"depends_on":[{"item_id":"schema","status":"completed"}]`},
		{"continuity_brief", map[string]any{}, `"lines":[]`},
		{"continuity_lease", map[string]any{"action": "release", "scope": "task:mcp-lease", "holder": "mcp-agent", "token": 1}, `"state":"released"`},
		{"continuity_info", map[string]any{}, `"database_schema":"` + latestSchema + `"`},
		{"continuity_info", map[string]any{}, `"mcp_api_version":` + strconv.Itoa(buildinfo.MCPAPIVersion)},
		{"continuity_info", map[string]any{}, `"process_started_at"`},
		{"continuity_info", map[string]any{"probes": []string{"narrative:mcp-work"}, "check_integrity": true}, `"status":"passed"`},
	} {
		if call.name == "cli" {
			// Maintenance commands left MCP for the CLI; they still set up state for later calls.
			runCLI(t, "", call.args["argv"].([]string)...)
			continue
		}
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: call.name, Arguments: call.args})
		if err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError || !strings.Contains(string(body), call.want) {
			t.Fatalf("%s: %s content=%v", call.name, body, result.Content[0].(*mcp.TextContent).Text)
		}
		// Write tools return receipts, never the record the caller just sent.
		if receiptTools[call.name] && (len(body) > 512 || strings.Contains(string(body), `"claims"`) || strings.Contains(string(body), `"items"`) || strings.Contains(string(body), `"rationale"`)) {
			t.Fatalf("%s returned more than a receipt (%d bytes): %s", call.name, len(body), body)
		}
		if strings.Contains(string(body), "fixture-mcp-secret") || strings.Contains(string(body), "fixture-evidence-secret") || strings.Contains(string(body), "fixture-idea-secret") || strings.Contains(string(body), "fixture-narrative-secret") {
			t.Fatal("MCP returned unredacted content")
		}
	}
	unscoped, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "continuity_checkpoint_actions", Arguments: map[string]any{}})
	if err != nil || !unscoped.IsError {
		t.Fatalf("unscoped action listing across tasks was not refused: %v %#v", err, unscoped)
	}
	actionsResult, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "continuity_checkpoint_actions", Arguments: map[string]any{"all": true}})
	if err != nil || actionsResult.IsError {
		t.Fatalf("list actions for terminal checkpoint: %v %#v", err, actionsResult)
	}
	actionsBody, _ := json.Marshal(actionsResult.StructuredContent)
	var actionList struct {
		Actions []checkpoint.Action `json:"actions"`
	}
	if err := json.Unmarshal(actionsBody, &actionList); err != nil {
		t.Fatal(err)
	}
	var smokeActionID string
	var completionActionID string
	for _, action := range actionList.Actions {
		if action.Description == "Verify bootstrap" {
			smokeActionID = action.ID
		}
		if action.Description == "Finalize" {
			completionActionID = action.ID
		}
	}
	if smokeActionID == "" || completionActionID == "" {
		t.Fatalf("missing MCP smoke action: %s", actionsBody)
	}
	var completionLineage struct {
		Lineage checkpoint.TaskLineageView
	}
	if err := json.Unmarshal([]byte(runCLI(t, "", "checkpoint", "task-lineage", "--task-id", "completion-mcp")), &completionLineage.Lineage); err != nil {
		t.Fatal(err)
	}
	// A completed checkpoint is the terminal step for a task.
	completionDone, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "continuity_checkpoint", Arguments: map[string]any{
		"task_id": "completion-mcp", "predecessor_checkpoint_id": completionLineage.Lineage.CurrentTip, "task": "Completion MCP task", "status": "completed", "summary": "Completion MCP task complete", "next_action": "Task complete; await the next objective.",
		"action_updates": []map[string]any{{"id": completionActionID, "status": "completed", "reason": "finalized by a completed checkpoint"}},
	}})
	if err != nil || completionDone.IsError {
		t.Fatalf("complete the completion task: %v %#v", err, completionDone)
	}
	terminalArgs := map[string]any{
		"task_id": "mcp-smoke", "predecessor_checkpoint_id": taskTip(t, "mcp-smoke"), "session_id": "mcp-session", "task": "MCP smoke task", "status": "completed",
		"summary": "MCP smoke complete", "next_action": "Task complete; await the next objective.",
		"action_updates": []map[string]any{{"id": smokeActionID, "status": "completed", "reason": "verified by stdio test"}},
	}
	dryRunArgs := map[string]any{"dry_run": true}
	for key, value := range terminalArgs {
		dryRunArgs[key] = value
	}
	preflightResult, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "continuity_checkpoint", Arguments: dryRunArgs})
	if err != nil || preflightResult.IsError {
		t.Fatalf("terminal checkpoint preflight: %v %#v", err, preflightResult)
	}
	preflightBody, _ := json.Marshal(preflightResult.StructuredContent)
	if !strings.Contains(string(preflightBody), `"projected_unresolved_action_count":0`) || !strings.Contains(string(preflightBody), `"creates_next_action":false`) {
		t.Fatalf("terminal preflight did not project a clean lifecycle: %s", preflightBody)
	}
	terminalResult, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "continuity_checkpoint", Arguments: terminalArgs})
	if err != nil || terminalResult.IsError {
		t.Fatalf("terminal checkpoint save: %v %#v", err, terminalResult)
	}
	terminalBody, _ := json.Marshal(terminalResult.StructuredContent)
	if !strings.Contains(string(terminalBody), `"action_created":false`) || !strings.Contains(string(terminalBody), `"status":"completed"`) {
		t.Fatalf("terminal checkpoint did not reconcile atomically: %s", terminalBody)
	}
	actionsResult, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "continuity_checkpoint_actions", Arguments: map[string]any{"task_id": "mcp-smoke"}})
	if err != nil || actionsResult.IsError {
		t.Fatalf("list actions after terminal checkpoint: %v %#v", err, actionsResult)
	}
	actionsBody, _ = json.Marshal(actionsResult.StructuredContent)
	if strings.Contains(string(actionsBody), smokeActionID) {
		t.Fatalf("terminal checkpoint left predecessor unresolved: %s", actionsBody)
	}
	frictionResult, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "continuity_item", Arguments: map[string]any{"friction": true, "min_count": 2, "max_bytes": 2048}})
	if err != nil || frictionResult.IsError {
		t.Fatalf("friction report for tracking: %v %#v", err, frictionResult)
	}
	frictionBody, _ := json.Marshal(frictionResult.StructuredContent)
	var frictionReport struct {
		Friction struct {
			Candidates []struct {
				ID string `json:"id"`
			} `json:"candidates"`
		} `json:"friction"`
	}
	if err = json.Unmarshal(frictionBody, &frictionReport); err != nil || len(frictionReport.Friction.Candidates) != 1 {
		t.Fatalf("decode friction report: %v %s", err, frictionBody)
	}
	frictionBug, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "continuity_item_capture", Arguments: map[string]any{"kind": "bug", "title": "Cache permissions", "body": "Review cache permissions", "severity": "low", "from": "friction:" + frictionReport.Friction.Candidates[0].ID, "creator": "human"}})
	var frictionReceipt struct {
		Ref string `json:"ref"`
	}
	frictionBugBody, _ := json.Marshal(frictionBug.StructuredContent)
	if err != nil || frictionBug.IsError || json.Unmarshal(frictionBugBody, &frictionReceipt) != nil || !strings.HasPrefix(frictionReceipt.Ref, "work_item:") {
		t.Fatalf("friction tracking bug: %v %s", err, frictionBugBody)
	}
	frictionItem, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "continuity_item", Arguments: map[string]any{"id": frictionReceipt.Ref}})
	frictionItemBody, _ := json.Marshal(frictionItem.StructuredContent)
	if err != nil || frictionItem.IsError || !strings.Contains(string(frictionItemBody), `"relationship":"derived_from"`) {
		t.Fatalf("friction tracking bug links: %v %s", err, frictionItemBody)
	}
	findResult, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "continuity_checkpoint_get", Arguments: map[string]any{"query": "MCP smoke"}})
	if err != nil || findResult.IsError {
		t.Fatalf("find MCP checkpoint for show: %v %#v", err, findResult)
	}
	findBody, _ := json.Marshal(findResult.StructuredContent)
	var found struct {
		Matches struct {
			Hits []struct {
				ID string `json:"id"`
			} `json:"hits"`
		} `json:"matches"`
	}
	if err := json.Unmarshal(findBody, &found); err != nil || len(found.Matches.Hits) == 0 {
		t.Fatalf("decode MCP history result: %v %s", err, findBody)
	}
	showResult, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "continuity_checkpoint_get", Arguments: map[string]any{"id": found.Matches.Hits[0].ID, "max_bytes": 2048}})
	showBody, _ := json.Marshal(showResult.StructuredContent)
	if err != nil || showResult.IsError || !strings.Contains(string(showBody), "# Historical handoff") || !strings.Contains(string(showBody), `"authority":"caller_asserted"`) {
		t.Fatalf("unexpected MCP history show: %v %s", err, showBody)
	}

	for _, args := range []map[string]any{
		{"task": " ", "summary": "", "next_action": "Next"},
		{"task": "Task", "summary": strings.Repeat("x", 11<<10), "next_action": "Next"},
		{"task": "Task", "summary": strings.Repeat("x", 33<<10), "next_action": "Next"},
		{"task": "Task", "summary": "", "next_action": "Next", "attempts": []map[string]any{{"description": "Incomplete", "outcome": ""}}},
	} {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "continuity_checkpoint", Arguments: args})
		if err == nil && !result.IsError {
			t.Fatal("MCP accepted invalid checkpoint")
		}
	}
	if got := runCLI(t, "", "work"); !strings.Contains(got, "MCP smoke task") {
		t.Fatal("invalid checkpoint replaced valid handoff")
	}
}

func TestCheckpointCLIValidationAndRedaction(t *testing.T) {
	testProject(t)
	runCLI(t, "", "init")
	for _, args := range [][]string{
		{"--task-id", "v", "--task", " ", "--next", "Next"},
		{"--task-id", "v", "--task", "Task", "--next", "Next", "--status", "invalid"},
		{"--task-id", "v", "--task", "Task", "--next", "Next", "--summary", strings.Repeat("x", 17<<10)},
		{"--task-id", "v", "--task", "Task", "--next", "Next", "--summary", strings.Repeat("x", 33<<10)},
		{"--task", "Task", "--next", "Next"},
	} {
		cmd := newRootCommand("llewm")
		cmd.SetArgs(append([]string{"checkpoint", "create"}, args...))
		if err := cmd.Execute(); err == nil {
			t.Fatal("CLI accepted invalid checkpoint")
		}
	}
	if got := runCLI(t, "", "work"); !strings.Contains(got, "No checkpoint") {
		t.Fatal("invalid CLI write persisted")
	}
	runCLI(t, "", "checkpoint", "create", "--task-id", "cli-task-7", "--task", "Valid", "--next", "Next", "--summary", "password=fixture-cli-secret")
	if got := runCLI(t, "", "bootstrap", "--output", "json"); strings.Contains(got, "fixture-cli-secret") || !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("CLI redaction failed: %s", got)
	}
}

func TestCheckpointHistoryCLI(t *testing.T) {
	testProject(t)
	runCLI(t, "", "init")
	olderID := strings.TrimSpace(runCLI(t, "", "checkpoint", "create", "--task-id", "cli-task-8", "--task", "Historical parser repair", "--summary", "Found a lexical needle", "--next", "Reuse the repair"))
	runCLI(t, "", "checkpoint", "create", "--task-id", "new-active", "--task", "New active task", "--next", "Keep working")
	found := runCLI(t, "", "checkpoint", "show", "--query", "needle", "--output", "json")
	for _, want := range []string{olderID, `"authority": "caller_asserted"`, "no model ranked or interpreted"} {
		if !strings.Contains(found, want) {
			t.Fatalf("history find missing %q: %s", want, found)
		}
	}
	if explicit := runCLI(t, "", "checkpoint", "show", "--query", "needle", "--output", "json"); explicit != found {
		t.Fatalf("checkpoint find JSON selectors differ:\n%s\n%s", found, explicit)
	}
	shown := runCLI(t, "", "checkpoint", "show", olderID, "--max-bytes", "2048")
	if !strings.Contains(shown, "# Historical handoff") || !strings.Contains(shown, "Reuse the repair") || !strings.Contains(shown, "Authority: caller_asserted") {
		t.Fatalf("unexpected historical export: %s", shown)
	}
	if explicit := runCLI(t, "", "checkpoint", "show", olderID, "--max-bytes", "2048", "--output", "text"); explicit != shown {
		t.Fatalf("checkpoint show --output text changed the default:\n%s\n%s", shown, explicit)
	}
	if alias, explicit := runCLI(t, "", "checkpoint", "show", olderID, "--output", "json"), runCLI(t, "", "checkpoint", "show", olderID, "--output", "json"); alias != explicit {
		t.Fatalf("checkpoint show JSON selectors differ:\n%s\n%s", alias, explicit)
	}
	if got := runCLI(t, "", "work"); !strings.Contains(got, "New active task") {
		t.Fatalf("history inspection changed latest checkpoint: %s", got)
	}
}

func TestCheckpointPreflightCLIIsReadOnly(t *testing.T) {
	testProject(t)
	runCLI(t, "", "init")
	got := runCLI(t, "", "checkpoint", "create", "--task-id", "cli-task-9", "--dry-run", "--task", "Prospective task", "--summary", "Check the packet", "--next", "Save only when ready", "--output", "json")
	for _, want := range []string{`"ready": true`, `"json_bytes":`, `"markdown_bytes":`, `"checkpoint_kernel_bytes":`, `"checkpoint_detail_bytes":`, `"authority": "prospective_caller_asserted"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("preflight missing %q: %s", want, got)
		}
	}
	if explicit := runCLI(t, "", "checkpoint", "create", "--dry-run", "--task-id", "cli-task-9", "--task", "Prospective task", "--summary", "Check the packet", "--next", "Save only when ready", "--output", "json"); explicit != got {
		t.Fatalf("checkpoint preflight JSON selectors differ:\n%s\n%s", got, explicit)
	}
	if got := runCLI(t, "", "work"); !strings.Contains(got, "No checkpoint") {
		t.Fatalf("preflight persisted state: %s", got)
	}
}

func TestNarrativeCLIRecordListAndShow(t *testing.T) {
	root := testProject(t)
	runCLI(t, "", "init")
	body, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	anchors := fmt.Sprintf(`[{"kind":"checkpoint","target":"checkpoint-one","authority":"caller_asserted"},{"kind":"file","target":"AGENTS.md","content_sha256":"%s","authority":"machine_observed"}]`, hex.EncodeToString(sum[:]))
	claims := `[{"kind":"intent","text":"Preserve token=fixture-work-cli-secret","authority":"caller_asserted"},{"kind":"decision","text":"Use explicit capture","authority":"user_reported"}]`
	recorded := runCLI(t, "", "narrative", "record", "--id", "cli-work", "--title", "CLI work", "--trigger", "accepted_decision", "--claims-json", claims, "--anchors-json", anchors, "--creator", "codex")
	if !strings.Contains(recorded, `"record_type": "work_narrative"`) || !strings.Contains(recorded, "[REDACTED]") || strings.Contains(recorded, "fixture-work-cli-secret") {
		t.Fatalf("unexpected narrative record: %s", recorded)
	}
	listed := runCLI(t, "", "narrative", "list", "--anchor-kind", "checkpoint", "--anchor", "checkpoint-one")
	if !strings.Contains(listed, `"id": "cli-work"`) || !strings.Contains(listed, `"records_loaded": 0`) || strings.Contains(listed, "Use explicit capture") {
		t.Fatalf("unexpected narrative list: %s", listed)
	}
	if alias := runCLI(t, "", "narrative", "list", "--anchor-kind", "checkpoint", "--anchor", "checkpoint-one", "--output", "json"); alias != listed {
		t.Fatalf("narrative list default and --json differ:\n%s\n%s", listed, alias)
	}
	listedText := runCLI(t, "", "narrative", "list", "--anchor-kind", "checkpoint", "--anchor", "checkpoint-one", "--output", "text")
	for _, want := range []string{"Records: 1 of 1 candidates", "narrative:cli-work", "state=current", "type=work_narrative", "trigger=accepted_decision", "anchors=2", "Measurement: loaded=0", "content_bytes=0", "expansion_depth=0", "Authority: mixed", "not instructions"} {
		if !strings.Contains(listedText, want) {
			t.Fatalf("narrative text list missing %q: %s", want, listedText)
		}
	}
	if strings.Contains(listedText, "Use explicit capture") {
		t.Fatalf("narrative text list loaded claims: %s", listedText)
	}
	shown := runCLI(t, "", "narrative", "show", "--id", "narrative:cli-work", "--max-bytes", "4096")
	if !strings.Contains(shown, "# CLI work") || !strings.Contains(shown, "Use explicit capture") || !strings.Contains(shown, "drift `current`") {
		t.Fatalf("unexpected narrative show: %s", shown)
	}
	if explicit := runCLI(t, "", "narrative", "show", "--id", "narrative:cli-work", "--max-bytes", "4096", "--output", "text"); explicit != shown {
		t.Fatalf("narrative show --output text changed the default:\n%s\n%s", shown, explicit)
	}
	if alias, explicit := runCLI(t, "", "narrative", "show", "--id", "narrative:cli-work", "--output", "json"), runCLI(t, "", "narrative", "show", "--id", "narrative:cli-work", "--output", "json"); alias != explicit {
		t.Fatalf("narrative show JSON selectors differ:\n%s\n%s", alias, explicit)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	shown = runCLI(t, "", "narrative", "show", "--id", "cli-work", "--max-bytes", "4096")
	if !strings.Contains(shown, "drift `drifted`") {
		t.Fatalf("missing drift: %s", shown)
	}
}

func TestItemCLIWorkflow(t *testing.T) {
	testProject(t)
	runCLI(t, "", "init")
	added := runCLI(t, "", "item", "add", "--id", "cli-idea", "--title", "Capture tangent", "--body", "Discuss later password=fixture-idea-cli-secret", "--tag", "memory")
	if !strings.Contains(added, `"status": "pending"`) || !strings.Contains(added, `"kind": "idea"`) || !strings.Contains(added, "[REDACTED]") || strings.Contains(added, "fixture-idea-cli-secret") {
		t.Fatalf("unexpected item add: %s", added)
	}
	bootstrap := runCLI(t, "", "bootstrap")
	if strings.Contains(bootstrap, "Capture tangent") || strings.Contains(bootstrap, "Discuss later") {
		t.Fatalf("item body leaked into bootstrap: %s", bootstrap)
	}
	if !strings.Contains(bootstrap, "**Work items (machine-observed):** 1 open (1 idea)") {
		t.Fatalf("bootstrap does not count open items: %s", bootstrap)
	}
	if status := runCLI(t, "", "work"); !strings.Contains(status, "No checkpoint") {
		t.Fatalf("item capture changed active checkpoint: %s", status)
	}
	bug := runCLI(t, "", "item", "add", "--kind", "bug", "--id", "cli-bug", "--title", "Sync crash", "--body", "Sync exits", "--observed", "panic", "--reproduction", "run sync twice", "--environment", "linux", "--severity", "high", "--tag", "memory")
	if !strings.Contains(bug, `"severity": "high"`) || !strings.Contains(bug, `"reproduction": "run sync twice"`) {
		t.Fatalf("unexpected bug add: %s", bug)
	}
	listed := runCLI(t, "", "item", "list", "--status", "pending", "--tag", "memory", "--output", "json")
	if !strings.Contains(listed, `"id": "cli-idea"`) || strings.Contains(listed, "Discuss later") {
		t.Fatalf("item list loaded body: %s", listed)
	}
	listedText := runCLI(t, "", "item", "list", "--status", "open", "--tag", "memory", "--limit", "1")
	for _, want := range []string{"Work items: 1 of 2 (limit 1, truncated true)", "work_item:cli-idea", "idea", "status=pending", "tags=memory", "title=Capture tangent", "not an instruction"} {
		if !strings.Contains(listedText, want) {
			t.Fatalf("item list text missing %q: %s", want, listedText)
		}
	}
	if bugs := runCLI(t, "", "item", "list", "--kind", "bug"); !strings.Contains(bugs, "bug/high") || strings.Contains(bugs, "cli-idea") {
		t.Fatalf("kind filter failed: %s", bugs)
	}
	shown := runCLI(t, "", "item", "show", "work_item:cli-idea")
	if !strings.Contains(shown, "Capture tangent") || !strings.Contains(shown, "[REDACTED]") {
		t.Fatalf("unexpected item show: %s", shown)
	}
	audit := runCLI(t, "", "item", "list", "--audit", "--max-bytes", "2048")
	for _, want := range []string{"Pending work items: 2 included of 2 (truncated false)", "# Pending work items", "work_item:cli-idea", "[REDACTED]", "Read-only pending work-item packet"} {
		if !strings.Contains(audit, want) {
			t.Fatalf("item audit missing %q: %s", want, audit)
		}
	}
	if strings.Contains(audit, "fixture-idea-cli-secret") {
		t.Fatalf("item audit leaked secret: %s", audit)
	}
	disposed := runCLI(t, "", "item", "dispose", "work_item:cli-idea", "--status", "accepted", "--reason", "Plan this", "--target-ref", "roadmap:memory")
	if !strings.Contains(disposed, `"status": "accepted"`) {
		t.Fatalf("unexpected acceptance: %s", disposed)
	}
	task := runCLI(t, "", "item", "add", "--kind", "task", "--id", "cli-task", "--title", "Do the tangent", "--body", "Concrete work", "--from", "work_item:cli-idea")
	if !strings.Contains(task, `"target_ref": "work_item:cli-idea"`) {
		t.Fatalf("promotion link missing: %s", task)
	}
	shown = runCLI(t, "", "item", "show", "cli-idea")
	if !strings.Contains(shown, "Disposition history") || !strings.Contains(shown, "Plan this") || !strings.Contains(shown, "Promoted to task work_item:cli-task") {
		t.Fatalf("item transition history missing: %s", shown)
	}
	if pending := runCLI(t, "", "item", "list", "--status", "open"); strings.Contains(pending, "cli-idea") || !strings.Contains(pending, "cli-task") {
		t.Fatalf("promoted item remained open: %s", pending)
	}
}

func TestItemFrictionCLIWorkflow(t *testing.T) {
	testProject(t)
	runCLI(t, "", "init")
	for i := 0; i < 2; i++ {
		sessionID := fmt.Sprintf("cli-friction-%d", i)
		runCLI(t, fmt.Sprintf(`{"session_id":%q,"hook_event_name":"PostToolUseFailure","tool_name":"Bash","tool_input":{"command":"go test ./..."},"error":"go: failed to initialize build cache: permission denied token=fixture-cli-friction-secret"}`, sessionID), "hook", "claude")
		runCLI(t, fmt.Sprintf(`{"session_id":%q,"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"GOCACHE=/tmp/llewm-go-cache go test ./..."}}`, sessionID), "hook", "claude")
	}
	textReport := runCLI(t, "", "item", "list", "--friction", "--min-count", "2", "--max-bytes", "2048")
	for _, want := range []string{"Friction candidates: 1 included of 1", "Events scanned:", "# Repeated friction candidates", "GOCACHE=* go test", "[redacted]", "Tracked by: none", "do not establish cause"} {
		if !strings.Contains(textReport, want) {
			t.Fatalf("friction text missing %q: %s", want, textReport)
		}
	}
	if strings.Contains(textReport, "fixture-cli-friction-secret") {
		t.Fatalf("friction text leaked secret: %s", textReport)
	}
	body := runCLI(t, "", "item", "list", "--friction", "--min-count", "2", "--max-bytes", "2048", "--output", "json")
	var report struct {
		CandidateCount int `json:"candidate_count"`
		Candidates     []struct {
			ID            string `json:"id"`
			RecoveryShape string `json:"recovery_shape"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(body), &report); err != nil || report.CandidateCount != 1 || len(report.Candidates) != 1 || report.Candidates[0].RecoveryShape != "GOCACHE=* go test" {
		t.Fatalf("unexpected friction report: %v %s", err, body)
	}
	runCLI(t, "", "item", "add", "--kind", "bug", "--id", "cache-bug", "--title", "Go cache permission", "--body", "Persist a cache override", "--from", "friction:"+report.Candidates[0].ID)
	if updated := runCLI(t, "", "item", "list", "--friction", "--min-count", "2", "--max-bytes", "2048"); !strings.Contains(updated, "Tracked by: `work_item:cache-bug` (bug, pending)") {
		t.Fatalf("tracking item missing from report: %s", updated)
	}
}

func TestTrackPlanListSupportsBoundedTextAndCompatibleJSON(t *testing.T) {
	root := testProject(t)
	runCLI(t, "", "init")
	for _, id := range []string{"a", "b"} {
		runCLI(t, "", "roadmap", "node", "--id", "node-"+id, "--title", "Node "+id, "--status", "active", "--source-path", "AGENTS.md")
		path := filepath.Join(root, "plan-"+id+".json")
		body := fmt.Sprintf(`{"id":"plan-%s","roadmap_node_id":"node-%s","title":"Plan %s","summary":"Bounded plan","status":"proposed","creator":"human","items":[{"id":"item","position":0,"title":"Item","detail":"Inspect this plan","status":"pending","evidence_refs":["docs/roadmap.md"],"work_item_ids":["candidate"]}]}`, id, id, id)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		runCLI(t, "", "track-plan", "record", "--file", path)
	}
	listed := runCLI(t, "", "track-plan", "show", "--status", "proposed")
	if alias := runCLI(t, "", "track-plan", "show", "--status", "proposed", "--output", "json"); alias != listed {
		t.Fatalf("track-plan list default and --json differ:\n%s\n%s", listed, alias)
	}
	if explicit := runCLI(t, "", "track-plan", "show", "--status", "proposed", "--output", "json"); explicit != listed {
		t.Fatalf("track-plan list default and --output json differ:\n%s\n%s", listed, explicit)
	}
	text := runCLI(t, "", "track-plan", "show", "--status", "proposed", "--limit", "1", "--output", "text")
	for _, want := range []string{"Track plans: 1 of 2 (limit 1, truncated true, next_cursor=unavailable)", "plan:plan-a@1", "status=proposed", "roadmap=roadmap:node-a", "task=none", "items=1", "ready=0", "title=Plan a", "Authority: caller-asserted plan revisions", "does not select, activate, resume, or execute a plan or item"} {
		if !strings.Contains(text, want) {
			t.Fatalf("track-plan list text missing %q: %s", want, text)
		}
	}
	planArgs := []string{"track-plan", "show", "plan-a"}
	planJSON := runCLI(t, "", planArgs...)
	if alias := runCLI(t, "", append(planArgs, "--output", "json")...); alias != planJSON {
		t.Fatalf("track-plan show --json changed default output:\ndefault: %s\nalias: %s", planJSON, alias)
	}
	if selected := runCLI(t, "", append(planArgs, "--output", "json")...); selected != planJSON {
		t.Fatalf("track-plan show --output json changed default output:\ndefault: %s\nselected: %s", planJSON, selected)
	}
	if strings.Contains(planJSON, "Inspect this plan") || !strings.Contains(planJSON, `"title": "Item"`) {
		t.Fatalf("track-plan show default is not an outline: %s", planJSON)
	}
	if full := runCLI(t, "", append(planArgs, "--detail")...); !strings.Contains(full, "Inspect this plan") {
		t.Fatalf("track-plan show --detail lacks item detail: %s", full)
	}
	if item := runCLI(t, "", append(planArgs, "--item", "item")...); !strings.Contains(item, "Inspect this plan") {
		t.Fatalf("track-plan show --item lacks item detail: %s", item)
	}
	planText := runCLI(t, "", append(planArgs, "--output", "text")...)
	for _, want := range []string{"Track plan: plan:plan-a@1", "Roadmap: roadmap:node-a", "Status: proposed", "Title: Plan a", "Summary: Bounded plan", "Items: 1 (maximum 100; plan limit 256 KiB)", "item:item", "position=0", "status=pending", "title=Item", "Authority: caller_asserted"} {
		if !strings.Contains(planText, want) {
			t.Fatalf("track-plan show text missing %q: %s", want, planText)
		}
	}
	if strings.Contains(planText, "Inspect this plan") {
		t.Fatalf("track-plan show text includes item detail without --detail: %s", planText)
	}
	planText = runCLI(t, "", append(planArgs, "--output", "text", "--detail")...)
	for _, want := range []string{"detail=Inspect this plan", "evidence=docs/roadmap.md", "work_items=work_item:candidate"} {
		if !strings.Contains(planText, want) {
			t.Fatalf("track-plan show --detail text missing %q: %s", want, planText)
		}
	}
}

func TestTrackPlanHistoryAddsBoundedMetadataTextWithoutChangingJSON(t *testing.T) {
	root := testProject(t)
	runCLI(t, "", "init")
	path := filepath.Join(root, "plan.json")
	for revision := 1; revision <= 2; revision++ {
		body := fmt.Sprintf(`{"id":"history","roadmap_node_id":"node-a","title":"Revision %d","summary":"Private summary %d","status":"proposed","creator":"human","items":[{"id":"item","position":0,"title":"Private item","detail":"Private detail","status":"pending"}]}`, revision, revision)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		runCLI(t, "", "track-plan", "record", "--file", path)
	}
	listed := runCLI(t, "", "track-plan", "show", "plan:history", "--history")
	if alias := runCLI(t, "", "track-plan", "show", "history", "--history", "--output", "json"); alias != listed {
		t.Fatalf("track-plan history default and --json differ:\n%s\n%s", listed, alias)
	}
	if explicit := runCLI(t, "", "track-plan", "show", "history", "--history", "--output", "json"); explicit != listed {
		t.Fatalf("track-plan history default and --output json differ:\n%s\n%s", listed, explicit)
	}
	text := runCLI(t, "", "track-plan", "show", "history", "--history", "--limit", "1", "--output", "text")
	for _, want := range []string{"Track plan revisions: 1 at offset 0 of 2 (limit 1, has_more true, next_cursor=unavailable)", "plan:history@1", "roadmap=roadmap:node-a", "items=1", "title=Revision 1", "Authority: caller-asserted plan revisions", "omits plan summaries and item details"} {
		if !strings.Contains(text, want) {
			t.Fatalf("track-plan history text missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "Private summary") || strings.Contains(text, "Private detail") || strings.Contains(text, "Private item") {
		t.Fatalf("track-plan history text included summary or item body: %s", text)
	}
	next := runCLI(t, "", "track-plan", "show", "history", "--history", "--limit", "1", "--offset", "1", "--output", "text")
	if !strings.Contains(next, "at offset 1 of 2") || !strings.Contains(next, "plan:history@2") || strings.Contains(next, "plan:history@1") {
		t.Fatalf("unexpected second history page: %s", next)
	}
}

func TestTrackPlanReadyTextPreservesJSONAndLabelsBounds(t *testing.T) {
	root := testProject(t)
	runCLI(t, "", "init")
	path := filepath.Join(root, "ready-plan.json")
	body := `{"id":"ready-plan","roadmap_node_id":"node-a","task_id":"task-a","title":"Ready plan","summary":"Work","status":"accepted","creator":"human","items":[{"id":"first","position":0,"title":"First","detail":"Go","status":"pending"},{"id":"blocked","position":1,"title":"Blocked","detail":"Wait","status":"pending","depends_on":["first"]}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	runCLI(t, "", "track-plan", "record", "--file", path)
	base := []string{"track-plan", "show", "--ready", "--roadmap-node", "node-a"}
	defaultJSON := runCLI(t, "", base...)
	if selected := runCLI(t, "", append(base, "--output", "json")...); selected != defaultJSON {
		t.Fatalf("ready JSON selector changed default:\n%s\n%s", defaultJSON, selected)
	}
	if alias := runCLI(t, "", append(base, "--output", "json")...); alias != defaultJSON {
		t.Fatalf("ready JSON alias changed default:\n%s\n%s", defaultJSON, alias)
	}
	text := runCLI(t, "", append(base, "--output", "text")...)
	for _, want := range []string{"Ready items: 1 of 1 within the inspected plans", "plan:ready-plan@1/item:first", "roadmap=roadmap:node-a", "task=task:task-a", "title=First", "plan limit can omit other plans", "does not select, activate, resume, or execute work"} {
		if !strings.Contains(text, want) {
			t.Fatalf("ready text missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "item:blocked") || strings.Contains(text, "detail=Go") {
		t.Fatalf("ready text exposed blocked item or item body: %s", text)
	}
}

func TestCheckpointSelectionCLI(t *testing.T) {
	testProject(t)
	runCLI(t, "", "init")
	firstID := strings.TrimSpace(runCLI(t, "", "checkpoint", "create", "--task-id", "task-a", "--session", "session-a", "--task", "First task", "--next", "Continue first"))
	runCLI(t, "", "checkpoint", "create", "--task-id", "task-b", "--session", "session-b", "--task", "Second task", "--next", "Continue second")
	for _, args := range [][]string{
		{"work", "--checkpoint-id", firstID},
		{"work", "--task-id", "task-a"},
		{"work", "--session", "session-a"},
		{"bootstrap", "--task-id", "task-a"},
		{"work", "--task-id", "task-a"},
	} {
		if got := runCLI(t, "", args...); !strings.Contains(got, "First task") {
			t.Fatalf("%v selected wrong checkpoint: %s", args, got)
		}
	}
	if got := runCLI(t, "", "work", "--task-id", "missing"); !strings.Contains(got, "No checkpoint recorded") {
		t.Fatalf("stale selection was not explicit: %s", got)
	}
	runCLI(t, `{"session_id":"session-a","hook_event_name":"PostToolUse","tool_name":"Read"}`, "hook", "codex")
	freshness := runCLI(t, "", "work", "freshness", "--task-id", "task-a", "--limit", "1", "--remind-after-events", "1", "--output", "json")
	for _, want := range []string{`"freshness": "activity_since_checkpoint"`, `"events_since_checkpoint": 1`, `"activity_scope": "project"`, `"repository_authority": "machine_observed"`, `"checkpoint_authority": "caller_asserted"`, `"reminder_evaluated": true`, `"checkpoint_recommended": true`, `"event_threshold"`} {
		if !strings.Contains(freshness, want) {
			t.Fatalf("freshness output missing %q: %s", want, freshness)
		}
	}
	if explicit := runCLI(t, "", "work", "freshness", "--task-id", "task-a", "--limit", "1", "--remind-after-events", "1", "--output", "json"); !strings.Contains(explicit, `"reminder_evaluated": true`) || !strings.Contains(explicit, `"checkpoint_recommended": true`) {
		t.Fatalf("unexpected freshness --output json: %s", explicit)
	}
	invalid := newRootCommand("llewm")
	invalid.SetArgs([]string{"work", "--task-id", "task-a", "--session", "session-a"})
	if err := invalid.Execute(); err == nil {
		t.Fatal("ambiguous selection succeeded")
	}
}

func TestGlobalGuidanceAcrossProjects(t *testing.T) {
	global := t.TempDir()
	t.Setenv("CONTINUITY_GLOBAL_HOME", global)
	t.Setenv("CONTINUITY_HOME", "")
	t.Chdir(t.TempDir()) // Global commands do not require a Git checkout.
	if got := strings.TrimSpace(runCLI(t, "", "global", "path")); got != global {
		t.Fatalf("global path: %s", got)
	}
	runCLI(t, "", "global", "init")
	for name, text := range map[string]string{"AGENTS.md": "# Commits\n\nGlobal commit convention.", "CONTEXT.md": "# Background\n\nEngineer background.", "PROMPTS.md": "# Review\n\nWhen reviewing, explain verification gaps."} {
		if err := os.WriteFile(filepath.Join(global, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runCLI(t, "", "global", "init") // Must preserve customized files.
	for _, name := range []string{"alpha", "beta"} {
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, ".git"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("# Project\n\nProject "+name), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Chdir(root)
		runCLI(t, "", "init")
		for _, args := range [][]string{{"bootstrap"}, {"bootstrap", "--output", "json"}, {"hook", "codex", "SessionStart"}, {"hook", "claude", "SessionStart"}} {
			got := runCLI(t, "{}", args...)
			for _, want := range []string{"Global commit convention.", "Engineer background.", "When reviewing", "Project " + name} {
				if !strings.Contains(got, want) {
					t.Fatalf("%v missing %s: %s", args, want, got)
				}
			}
		}
	}
	if err := os.WriteFile(filepath.Join(global, "AGENTS.md"), []byte("# Commits\n\nUpdated convention."), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := runCLI(t, "", "bootstrap"); !strings.Contains(got, "Updated convention.") || strings.Contains(got, "Global commit convention.") {
		t.Fatalf("global edits not reloaded: %s", got)
	}
}

var receiptTools = map[string]bool{
	"continuity_checkpoint_action_update": true, "continuity_decision_record": true,
	"continuity_item_capture": true, "continuity_item_disposition": true, "continuity_narrative_record": true,
	"continuity_roadmap_node":      true,
	"continuity_track_plan_record": true,
}
