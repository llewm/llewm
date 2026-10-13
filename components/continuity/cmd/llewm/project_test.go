package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/project"
	"github.com/llewm/llewm/components/continuity/internal/workspacechange"
)

func projectShow(t *testing.T) projectView {
	t.Helper()
	var view projectView
	if err := json.Unmarshal([]byte(runCLI(t, "", "project", "show", "--output", "json")), &view); err != nil {
		t.Fatal(err)
	}
	return view
}

func TestMovedCheckoutKeepsItsProjectAndHistory(t *testing.T) {
	root := testProject(t)
	t.Setenv("CONTINUITY_HOME", "")
	runCLI(t, "", "init")
	checkpointID := strings.TrimSpace(runCLI(t, "", "checkpoint", "create", "--task", "Before the move", "--task-id", "before-move", "--summary", "Recorded at the old path", "--next", "Move the checkout"))
	before := projectShow(t)

	moved := filepath.Join(filepath.Dir(root), "moved")
	t.Chdir(filepath.Dir(root))
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	t.Chdir(moved)

	pending := projectShow(t)
	if pending.ProjectID != before.ProjectID || pending.Identity.Outcome != "relocation_pending" || pending.Identity.PreviousRoot != root {
		t.Fatalf("read-only view after move: %#v", pending)
	}
	if shown := runCLI(t, "", "checkpoint", "show", checkpointID); !strings.Contains(shown, "Before the move") {
		t.Fatalf("history before the move is not visible:\n%s", shown)
	}
	runCLI(t, "", "checkpoint", "create", "--task", "After the move", "--task-id", "after-move", "--summary", "Recorded at the new path", "--next", "Keep working")
	after := projectShow(t)
	if after.ProjectID != before.ProjectID || after.Root != moved || after.Identity.Outcome != "existing" {
		t.Fatalf("view after a write: %#v", after)
	}
	if len(after.Locations) < 2 || after.Locations[0].Source != "relocated" || after.Locations[0].Path != moved || after.Locations[1].Path != root {
		t.Fatalf("locations after move: %#v", after.Locations)
	}
	if text := runCLI(t, "", "project", "show"); !strings.Contains(text, "relocated") || !strings.Contains(text, "Project: "+before.ProjectID) {
		t.Fatalf("text view:\n%s", text)
	}
}

func TestWorktreesAndSubmodulesShareTheirProjectsStore(t *testing.T) {
	root := testProject(t)
	t.Setenv("CONTINUITY_HOME", "")
	runCLI(t, "", "init")
	main := projectShow(t)

	lib := filepath.Join(filepath.Dir(root), "lib")
	worktree := filepath.Join(filepath.Dir(root), "wt")
	for _, step := range []struct {
		dir  string
		args []string
	}{
		{filepath.Dir(root), []string{"init", "-q", lib}},
		{lib, []string{"commit", "-q", "--allow-empty", "-m", "lib"}},
		{root, []string{"-c", "protocol.file.allow=always", "submodule", "add", "-q", lib, "lib"}},
		{root, []string{"commit", "-q", "-m", "add lib"}},
		{root, []string{"worktree", "add", "-q", worktree}},
	} {
		command := exec.Command("git", append([]string{"-c", "user.name=Continuity Test", "-c", "user.email=test@example.invalid"}, step.args...)...)
		command.Dir = step.dir
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", step.args, err, output)
		}
	}
	for _, dir := range []struct{ path, kind string }{{worktree, "worktree"}, {filepath.Join(root, "lib"), "submodule"}} {
		t.Chdir(dir.path)
		view := projectShow(t)
		if view.ProjectID != main.ProjectID || view.Root != root || view.Checkout.Kind != dir.kind || view.Checkout.Path != dir.path {
			t.Fatalf("%s view: %#v", dir.kind, view)
		}
		if _, err := os.Stat(filepath.Join(dir.path, ".continuity")); !os.IsNotExist(err) {
			t.Fatalf("%s gained its own store: %v", dir.kind, err)
		}
	}
}

func TestProjectRebindRequiresExactlyOneTarget(t *testing.T) {
	testProject(t)
	runCLI(t, "", "init")
	for _, args := range [][]string{{"project", "rebind"}, {"project", "rebind", "project-x", "--new"}} {
		if err, _ := runCLIError(args...); err == nil || !strings.Contains(err.Error(), "exactly one") {
			t.Fatalf("%v: %v", args, err)
		}
	}
}

func TestUnifiedWorkspaceGroupsItsMembersIntoOneProject(t *testing.T) {
	t.Setenv("CONTINUITY_GLOBAL_HOME", t.TempDir())
	t.Setenv("CONTINUITY_HOME", "")
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(base, "trading")
	for _, name := range []string{"joncyd", "joncy-trader", "joncy-agent"} {
		dir := filepath.Join(ws, name)
		for _, args := range [][]string{{"init", "-q", dir}, {"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "--allow-empty", "-m", "init"}} {
			if output, err := exec.Command("git", args...).CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v: %s", args, err, output)
			}
		}
	}
	t.Chdir(ws)
	if err, _ := runCLIError("work"); err == nil || !strings.Contains(err.Error(), "llewm workspace init") {
		t.Fatalf("status without a manifest: %v", err)
	}
	proposal := runCLI(t, "", "workspace", "init")
	if !strings.Contains(proposal, "Nothing written") || !strings.Contains(proposal, `path = "joncy-agent"`) {
		t.Fatalf("proposal:\n%s", proposal)
	}
	if _, err := os.Stat(filepath.Join(ws, ".continuity", "workspace.toml")); !os.IsNotExist(err) {
		t.Fatalf("proposal wrote the manifest: %v", err)
	}
	runCLI(t, "", "workspace", "init", "--write")
	if err, _ := runCLIError("workspace", "init", "--write"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second write: %v", err)
	}
	runCLI(t, "", "init")
	runCLI(t, "", "checkpoint", "create", "--task", "Wire the services", "--task-id", "wire", "--summary", "Across three repos", "--next", "Connect the daemon")

	root := projectShow(t)
	if root.Checkout.Kind != "workspace" || root.Root != ws || len(root.Members) != 3 {
		t.Fatalf("workspace view: %#v", root)
	}
	for _, member := range root.Members {
		if !member.Present || member.RepositoryID == "" || len(member.RootCommit) != 40 {
			t.Fatalf("member not recorded: %#v", member)
		}
	}
	t.Chdir(filepath.Join(ws, "joncyd"))
	member := projectShow(t)
	if member.ProjectID != root.ProjectID || member.Checkout.Member != "joncyd" || member.Root != ws {
		t.Fatalf("member view: %#v", member)
	}
	if status := runCLI(t, "", "work"); !strings.Contains(status, "Wire the services") {
		t.Fatalf("member status does not see the workspace checkpoint:\n%s", status)
	}
	if text := runCLI(t, "", "project", "show"); !strings.Contains(text, "member joncyd") || !strings.Contains(text, "Workspace: trading (unified, 3 members)") {
		t.Fatalf("text view:\n%s", text)
	}
	if roadmap := runCLI(t, "", "roadmap", "view", "status"); !strings.Contains(roadmap, "Current commit: joncy-agent@") || !strings.Contains(roadmap, ",joncyd@") {
		t.Fatalf("roadmap status:\n%s", roadmap)
	}

	// Commits resolve in the member containing the working directory.
	if err := os.WriteFile(filepath.Join(ws, "joncyd", "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "main.go"}, {"-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "-m", "Add daemon entry point"}} {
		command := exec.Command("git", args...)
		command.Dir = filepath.Join(ws, "joncyd")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	var narrative struct {
		Anchors []struct {
			Kind          string `json:"kind"`
			Target        string `json:"target"`
			RegionLocator string `json:"region_locator"`
		} `json:"anchors"`
	}
	recorded := runCLI(t, "", "narrative", "record", "--title", "Daemon entry", "--claims-json", `[{"kind":"outcome","text":"Added main.go","authority":"caller_asserted"},{"kind":"intent","text":"Entry point","authority":"caller_asserted"},{"kind":"verification","text":"None needed","authority":"caller_asserted"},{"kind":"limitation","text":"None","authority":"caller_asserted"}]`, "--commit", "HEAD", "--no-note")
	if err := json.Unmarshal([]byte(recorded), &narrative); err != nil {
		t.Fatal(err)
	}
	if len(narrative.Anchors) != 2 || narrative.Anchors[0].Kind != "commit" || narrative.Anchors[0].RegionLocator != "member:joncyd" || narrative.Anchors[1].Target != "joncyd/main.go" {
		t.Fatalf("narrative anchors: %s", recorded)
	}
	commitClaims := `[{"kind":"outcome","text":"o","authority":"caller_asserted"},{"kind":"intent","text":"i","authority":"caller_asserted"},{"kind":"verification","text":"v","authority":"caller_asserted"},{"kind":"limitation","text":"l","authority":"caller_asserted"}]`
	var commitNarrative struct {
		Anchors []struct {
			Kind          string `json:"kind"`
			Target        string `json:"target"`
			RegionLocator string `json:"region_locator"`
		} `json:"anchors"`
	}
	if err := json.Unmarshal([]byte(runCLI(t, "", "narrative", "record", "--commit", "HEAD", "--no-note", "--title", "t", "--claims-json", commitClaims)), &commitNarrative); err != nil || len(commitNarrative.Anchors) == 0 || commitNarrative.Anchors[0].RegionLocator != "member:joncyd" || len(commitNarrative.Anchors[0].Target) != 40 {
		t.Fatalf("commit narrative: %#v %v", commitNarrative, err)
	}

	// At the workspace folder, a member must be named.
	t.Chdir(ws)
	if err, _ := runCLIError("narrative", "record", "--commit", "HEAD", "--no-note", "--title", "t", "--claims-json", commitClaims); err == nil || !strings.Contains(err.Error(), "name the member repository (--repo, or repo over MCP): joncy-agent, joncy-trader, joncyd") {
		t.Fatalf("commit narrative without --repo: %v", err)
	}
	if err, _ := runCLIError("narrative", "record", "--repo", "nope", "--commit", "HEAD", "--no-note", "--title", "t", "--claims-json", commitClaims); err == nil || !strings.Contains(err.Error(), `"nope" is not a member`) {
		t.Fatalf("unknown member: %v", err)
	}
	if err := json.Unmarshal([]byte(runCLI(t, "", "narrative", "record", "--repo", "joncy-agent", "--commit", "HEAD", "--no-note", "--title", "t", "--claims-json", commitClaims)), &commitNarrative); err != nil || commitNarrative.Anchors[0].RegionLocator != "member:joncy-agent" {
		t.Fatalf("commit narrative with --repo: %#v %v", commitNarrative, err)
	}
	t.Chdir(filepath.Join(ws, "joncyd"))
	if _, err := os.Stat(filepath.Join(ws, "joncyd", ".continuity")); !os.IsNotExist(err) {
		t.Fatalf("member gained its own store: %v", err)
	}
}

func TestFederatedWorkspaceRegistersIndependentMemberProjects(t *testing.T) {
	global := t.TempDir()
	t.Setenv("CONTINUITY_GLOBAL_HOME", global)
	t.Setenv("CONTINUITY_HOME", "")
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(base, "fleet")
	for _, name := range []string{"api", "worker"} {
		dir := filepath.Join(ws, name)
		for _, args := range [][]string{{"init", "-q", dir}, {"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "--allow-empty", "-m", "init"}} {
			if output, err := exec.Command("git", args...).CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v: %s", args, err, output)
			}
		}
	}
	t.Chdir(ws)
	manifest := runCLI(t, "", "workspace", "init", "--mode", "federated", "--write")
	if !strings.Contains(manifest, `mode = "federated"`) || !strings.Contains(manifest, "inside each member") {
		t.Fatalf("manifest output:\n%s", manifest)
	}
	projects := map[string]string{}
	for _, name := range []string{"api", "worker"} {
		t.Chdir(filepath.Join(ws, name))
		runCLI(t, "", "init")
		view := projectShow(t)
		projects[name] = view.ProjectID
		if view.Root != filepath.Join(ws, name) || view.Workspace == nil || view.Workspace.Mode != "federated" || view.Checkout.Member != name {
			t.Fatalf("%s view: %#v", name, view)
		}
	}
	if projects["api"] == projects["worker"] {
		t.Fatalf("federated members shared project %s", projects["api"])
	}
	t.Chdir(ws)
	shown := runCLI(t, "", "workspace", "show")
	for name, projectID := range projects {
		if !strings.Contains(shown, name) || !strings.Contains(shown, projectID) || !strings.Contains(shown, filepath.Join(ws, name, ".continuity", "continuity.db")) {
			t.Fatalf("workspace show omitted %s route:\n%s", name, shown)
		}
	}
	routed := runCLI(t, "", "workspace", "route", projects["worker"])
	if !strings.Contains(routed, "project="+projects["worker"]) || !strings.Contains(routed, filepath.Join(ws, "worker")) {
		t.Fatalf("workspace route:\n%s", routed)
	}
}

func TestWorkspaceChangeSplitsAndMergesPortableMemoryAfterReview(t *testing.T) {
	t.Setenv("CONTINUITY_GLOBAL_HOME", t.TempDir())
	t.Setenv("CONTINUITY_HOME", "")
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(base, "suite")
	for _, name := range []string{"api", "worker"} {
		dir := filepath.Join(ws, name)
		for _, args := range [][]string{{"init", "-q", dir}, {"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "--allow-empty", "-m", "init"}} {
			if output, err := exec.Command("git", args...).CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v: %s", args, err, output)
			}
		}
	}
	t.Chdir(ws)
	runCLI(t, "", "workspace", "init", "--write")
	runCLI(t, "", "init")
	var identity struct {
		LineageID string `json:"lineage_id"`
	}
	if err := json.Unmarshal([]byte(runCLI(t, "", "sync", "init")), &identity); err != nil || identity.LineageID == "" {
		t.Fatalf("portable identity: %#v %v", identity, err)
	}
	first := strings.TrimSpace(runCLI(t, "", "checkpoint", "create", "--task-id", "topology", "--task", "Change topology", "--summary", "Before split", "--next", "Split"))

	var split workspacechange.Plan
	if err := json.Unmarshal([]byte(runCLI(t, "", "workspace", "change", "--mode", "federated", "--output", "json")), &split); err != nil {
		t.Fatal(err)
	}
	if !split.ApplyAllowed || split.Operation != "split" || split.LineageID != identity.LineageID || split.Confirmation == "" {
		t.Fatalf("split plan: %#v", split)
	}
	if err, _ := runCLIError("workspace", "change", "--mode", "federated", "--apply", "--confirm", "wrong"); err == nil || !strings.Contains(err.Error(), "explicit review required") {
		t.Fatalf("wrong split confirmation: %v", err)
	}
	staleConfirmation := split.Confirmation
	runCLI(t, "", "checkpoint", "create", "--task-id", "after-preview", "--task", "Change after preview", "--summary", "Review is now stale", "--next", "Preview again")
	if err, _ := runCLIError("workspace", "change", "--mode", "federated", "--apply", "--confirm", staleConfirmation); err == nil || !strings.Contains(err.Error(), "explicit review required") {
		t.Fatalf("stale split confirmation: %v", err)
	}
	if err := json.Unmarshal([]byte(runCLI(t, "", "workspace", "change", "--mode", "federated", "--output", "json")), &split); err != nil {
		t.Fatal(err)
	}
	runCLI(t, "", "workspace", "change", "--mode", "federated", "--apply", "--confirm", split.Confirmation)
	workspace, err := project.LoadWorkspace(ws)
	if err != nil || workspace == nil || workspace.Mode != project.ModeFederated {
		t.Fatalf("federated manifest: %#v %v", workspace, err)
	}

	projects := map[string]string{}
	for _, name := range []string{"api", "worker"} {
		t.Chdir(filepath.Join(ws, name))
		view := projectShow(t)
		projects[name] = view.ProjectID
		if shown := runCLI(t, "", "checkpoint", "show", first); !strings.Contains(shown, "Before split") {
			t.Fatalf("%s did not receive portable memory:\n%s", name, shown)
		}
		var status struct {
			Identity struct {
				LineageID string `json:"lineage_id"`
			} `json:"identity"`
		}
		if err := json.Unmarshal([]byte(runCLI(t, "", "sync", "status", "--output", "json")), &status); err != nil || status.Identity.LineageID != identity.LineageID {
			t.Fatalf("%s lineage: %#v %v", name, status, err)
		}
	}
	if projects["api"] == projects["worker"] {
		t.Fatalf("split members share project %s", projects["api"])
	}

	t.Chdir(filepath.Join(ws, "worker"))
	second := strings.TrimSpace(runCLI(t, "", "checkpoint", "create", "--task-id", "worker-only", "--task", "Worker task", "--summary", "After split", "--next", "Merge"))
	t.Chdir(ws)
	var merge workspacechange.Plan
	if err := json.Unmarshal([]byte(runCLI(t, "", "workspace", "change", "--mode", "unified", "--output", "json")), &merge); err != nil {
		t.Fatal(err)
	}
	if !merge.ApplyAllowed || merge.Operation != "merge" || merge.LineageID != identity.LineageID {
		t.Fatalf("merge plan: %#v", merge)
	}
	runCLI(t, "", "workspace", "change", "--mode", "unified", "--apply", "--confirm", merge.Confirmation)
	workspace, err = project.LoadWorkspace(ws)
	if err != nil || workspace == nil || workspace.Mode != project.ModeUnified {
		t.Fatalf("unified manifest: %#v %v", workspace, err)
	}
	for _, checkpointID := range []string{first, second} {
		if shown := runCLI(t, "", "checkpoint", "show", checkpointID); !strings.Contains(shown, checkpointID) {
			t.Fatalf("merged checkpoint %s missing:\n%s", checkpointID, shown)
		}
	}
}

func TestWorkspaceEditorGeneratesNonAuthoritativeVSCodeFile(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(base, "suite")
	for _, name := range []string{"api", "worker"} {
		dir := filepath.Join(ws, name)
		if output, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
			t.Fatalf("git init: %v: %s", err, output)
		}
	}
	t.Chdir(ws)
	runCLI(t, "", "workspace", "init", "--write")
	preview := runCLI(t, "", "workspace", "editor")
	if !strings.Contains(preview, `"path": "api"`) || !strings.Contains(preview, `"path": "worker"`) {
		t.Fatalf("editor preview:\n%s", preview)
	}
	path := filepath.Join(ws, "dev.code-workspace")
	receipt := runCLI(t, "", "workspace", "editor", "--write", "--path", path, "--output", "json")
	if !strings.Contains(receipt, `"authoritative": false`) {
		t.Fatalf("editor receipt: %s", receipt)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != preview {
		t.Fatalf("editor file differs from preview: %v\n%s", err, content)
	}
	if err, _ := runCLIError("workspace", "editor", "--write", "--path", path); err == nil {
		t.Fatal("editor workspace was overwritten")
	}
}

func TestWorkspaceMergeRefusesDifferentLineages(t *testing.T) {
	t.Setenv("CONTINUITY_GLOBAL_HOME", t.TempDir())
	t.Setenv("CONTINUITY_HOME", "")
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(base, "fleet")
	for _, name := range []string{"api", "worker"} {
		dir := filepath.Join(ws, name)
		for _, args := range [][]string{{"init", "-q", dir}, {"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "--allow-empty", "-m", "init"}} {
			if output, err := exec.Command("git", args...).CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v: %s", args, err, output)
			}
		}
	}
	t.Chdir(ws)
	runCLI(t, "", "workspace", "init", "--mode", "federated", "--write")
	for _, name := range []string{"api", "worker"} {
		t.Chdir(filepath.Join(ws, name))
		runCLI(t, "", "init")
		runCLI(t, "", "sync", "init")
	}
	t.Chdir(ws)
	var plan workspacechange.Plan
	if err := json.Unmarshal([]byte(runCLI(t, "", "workspace", "change", "--mode", "unified", "--output", "json")), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.ApplyAllowed || len(plan.Blockers) == 0 || !strings.Contains(strings.Join(plan.Blockers, "\n"), "different lineages") {
		t.Fatalf("merge plan did not reject different lineages: %#v", plan)
	}
	if err, _ := runCLIError("workspace", "change", "--mode", "unified", "--apply", "--confirm", plan.Confirmation); err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("mismatched lineage merge applied: %v", err)
	}
}
