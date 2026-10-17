package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestTwoMachinesForkAndResolveATask(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	previous := startBackgroundSync
	startBackgroundSync = func(string, string) error { return nil }
	t.Cleanup(func() { startBackgroundSync = previous })
	remote := filepath.Join(t.TempDir(), "memory.git")
	if output, err := exec.Command("git", "init", "--quiet", "--bare", remote).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v %s", err, output)
	}
	checkpoint := func(next string) string {
		// Each machine continues its own local tip; the fork appears after sync.
		return strings.TrimSpace(runCLI(t, "", "checkpoint", "create", "--task", "Ship the feature", "--task-id", "ship", "--predecessor-checkpoint", taskTip(t, "ship"), "--next", next))
	}

	rootA := testProject(t)
	xdgA := t.TempDir()
	t.Setenv("XDG_DATA_HOME", xdgA)
	rootB := filepath.Join(t.TempDir(), "machine-b")
	gitIn(t, filepath.Dir(rootB), "clone", "--quiet", rootA, rootB)
	runCLI(t, "", "init")
	var identity struct {
		LineageID string `json:"lineage_id"`
	}
	if err := json.Unmarshal([]byte(runCLI(t, "", "sync", "init")), &identity); err != nil {
		t.Fatal(err)
	}
	runCLI(t, "", "sync", "init", "--remote", remote)
	base := checkpoint("Start")
	runCLI(t, "", "sync")

	useProject(t, rootB)
	xdgB := os.Getenv("XDG_DATA_HOME")
	runCLI(t, "", "init")
	runCLI(t, "", "sync", "init", "--lineage", identity.LineageID)
	runCLI(t, "", "sync", "init", "--remote", remote)
	runCLI(t, "", "sync")
	// Both machines continue the base before either syncs again.
	onB := checkpoint("Finish on B")
	useProject(t, rootA)
	t.Setenv("XDG_DATA_HOME", xdgA)
	onA := checkpoint("Finish on A")
	runCLI(t, "", "sync")
	useProject(t, rootB)
	t.Setenv("XDG_DATA_HOME", xdgB)
	runCLI(t, "", "sync")

	var forks []struct {
		Subject string `json:"subject"`
		Base    string `json:"base"`
		Heads   []struct {
			CheckpointID string `json:"checkpoint_id"`
		} `json:"heads"`
	}
	var listed struct {
		Forks         json.RawMessage   `json:"forks"`
		RevisionForks []json.RawMessage `json:"revision_forks"`
	}
	if err := json.Unmarshal([]byte(runCLI(t, "", "fork", "list", "--output", "json")), &listed); err != nil || listed.RevisionForks == nil || len(listed.RevisionForks) != 0 {
		t.Fatalf("fork list: %#v %v", listed, err)
	}
	if err := json.Unmarshal(listed.Forks, &forks); err != nil {
		t.Fatal(err)
	}
	if len(forks) != 1 || forks[0].Subject != "ship" || forks[0].Base != base || len(forks[0].Heads) != 2 || forks[0].Heads[0].CheckpointID != onA || forks[0].Heads[1].CheckpointID != onB {
		t.Fatalf("machine B forks: %#v (A %s, B %s, base %s)", forks, onA, onB, base)
	}
	hook := runCLI(t, `{"session_id":"session-b","hook_event_name":"SessionStart","source":"startup"}`, "hook", "claude-code")
	if !strings.Contains(hook, "Fork (machine-observed)") || !strings.Contains(hook, "task ship has 2 checkpoint heads") || !strings.Contains(hook, "Finish on B") {
		t.Fatalf("bootstrap without a fork notice:\n%s", hook)
	}
	if shown := runCLI(t, "", "fork", "show", "task:ship"); !strings.Contains(shown, "proposed head "+onA) || !strings.Contains(shown, "Common base: "+base) {
		t.Fatalf("fork show:\n%s", shown)
	}

	// B keeps its own head; the resolution reaches A through sync.
	resolved := runCLI(t, "", "fork", "resolve", "task:ship", "--accept", onB, "--resolver", "owner")
	if !strings.Contains(resolved, "continued "+onB) || !strings.Contains(resolved, "kept "+onA) {
		t.Fatalf("resolve: %s", resolved)
	}
	if listed := runCLI(t, "", "fork", "list"); !strings.Contains(listed, "Forks: 0.") {
		t.Fatalf("machine B still forked:\n%s", listed)
	}
	runCLI(t, "", "sync")
	useProject(t, rootA)
	t.Setenv("XDG_DATA_HOME", xdgA)
	runCLI(t, "", "sync")
	if listed := runCLI(t, "", "fork", "list"); !strings.Contains(listed, "Forks: 0.") {
		t.Fatalf("machine A still forked after sync:\n%s", listed)
	}
	if status := runCLI(t, "", "work"); !strings.Contains(status, "Next: Finish on B") {
		t.Fatalf("machine A does not continue from the accepted head:\n%s", status)
	}

	// Both machines write revision 2 of one topic before syncing.
	topic := func(title string) { runCLI(t, "", "topic", "record", "sync", "--title", title, "--creator", "owner") }
	topic("Base")
	runCLI(t, "", "sync")
	useProject(t, rootB)
	t.Setenv("XDG_DATA_HOME", xdgB)
	runCLI(t, "", "sync")
	topic("Topic on B")
	useProject(t, rootA)
	t.Setenv("XDG_DATA_HOME", xdgA)
	topic("Topic on A")
	runCLI(t, "", "sync")
	useProject(t, rootB)
	t.Setenv("XDG_DATA_HOME", xdgB)
	if synced := runCLI(t, "", "sync"); !strings.Contains(synced, "1 record revisions have several versions") {
		t.Fatalf("sync did not report the fork:\n%s", synced)
	}
	if listed := runCLI(t, "", "fork", "list"); !strings.Contains(listed, "topic_revision:sync@2\t2 heads") || !strings.Contains(listed, "Topic on A") || !strings.Contains(listed, "Forks: 1.") {
		t.Fatalf("fork list:\n%s", listed)
	}
	if shown := runCLI(t, "", "fork", "show", "topic_revision:sync@2"); !strings.Contains(shown, "Title: Topic on A") || !strings.Contains(shown, "(held here)") {
		t.Fatalf("fork show:\n%s", shown)
	}
	hook = runCLI(t, `{"session_id":"session-b2","hook_event_name":"SessionStart","source":"startup"}`, "hook", "claude-code")
	if !strings.Contains(hook, "1 record revisions have several versions") || !strings.Contains(hook, "topic_revision:sync@2") {
		t.Fatalf("bootstrap without a revision fork notice:\n%s", hook)
	}
	// B accepts the proposed (newest) head, A's, which becomes revision 3 everywhere.
	resolved = runCLI(t, "", "fork", "resolve", "topic_revision:sync@2", "--resolver", "owner")
	if !strings.Contains(resolved, "appended its content as revision 3") {
		t.Fatalf("resolve: %s", resolved)
	}
	if shown := runCLI(t, "", "topic", "show", "sync"); !strings.Contains(shown, "Topic on A") {
		t.Fatalf("machine B topic after accepting A's head:\n%s", shown)
	}
	runCLI(t, "", "sync")
	useProject(t, rootA)
	t.Setenv("XDG_DATA_HOME", xdgA)
	runCLI(t, "", "sync")
	if listed := runCLI(t, "", "fork", "list"); !strings.Contains(listed, "Forks: 0.") {
		t.Fatalf("machine A still forked after the resolution synced:\n%s", listed)
	}
}

func TestSupersessionForkThroughTheCLI(t *testing.T) {
	root := testProject(t)
	runCLI(t, "", "init")
	record := func(id, supersedes string) {
		t.Helper()
		path := filepath.Join(t.TempDir(), id+".json")
		body := `{"id":"` + id + `","title":"Decision ` + id + `","rationale":"Because","disposition":"accepted","actor":"owner","actor_type":"human","authority":"user_reported","review_source":"test","supersedes_id":"` + supersedes + `"}`
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		runCLI(t, "", "decision", "record", "--file", path)
	}
	record("base", "")
	record("from-a", "base")
	// A second successor arrives only through sync; seed it as import would.
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(root, ".llewm/continuity", "continuity.db"))+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO decision_records(id,project_id,record_sha256,title,rationale,disposition,actor,actor_type,authority,review_source,supersedes_id,recorded_at,schema_version) SELECT 'from-b',project_id,'hash','Decision from-b','Because','accepted','owner','human','user_reported','test','base','2999-01-01T00:00:00Z',1 FROM decision_records WHERE id='base'`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	list := runCLI(t, "", "fork", "list")
	if !strings.Contains(list, "decision:base\t2 heads\tproposed from-b\tDecision base") || !strings.Contains(list, "Forks: 1.") {
		t.Fatalf("fork list:\n%s", list)
	}
	var listed struct {
		SupersessionForks []struct {
			RecordFamily string `json:"record_family"`
			RecordID     string `json:"record_id"`
		} `json:"supersession_forks"`
	}
	if err := json.Unmarshal([]byte(runCLI(t, "", "fork", "list", "--output", "json")), &listed); err != nil || len(listed.SupersessionForks) != 1 || listed.SupersessionForks[0].RecordID != "base" {
		t.Fatalf("fork list --json: %#v %v", listed, err)
	}
	show := runCLI(t, "", "fork", "show", "decision:base")
	for _, want := range []string{"Fork: decision:base (2 heads)", "proposed head decision:from-b", "head decision:from-a", "Title: Decision from-a"} {
		if !strings.Contains(show, want) {
			t.Fatalf("fork show missing %q:\n%s", want, show)
		}
	}
	runCLI(t, "", "checkpoint", "create", "--task", "Decide", "--task-id", "decide", "--next", "Resolve")
	hook := runCLI(t, `{"session_id":"s","hook_event_name":"SessionStart","source":"startup"}`, "hook", "claude-code")
	if !strings.Contains(hook, "1 records were superseded more than once") || !strings.Contains(hook, "decision:base") {
		t.Fatalf("bootstrap without a supersession fork notice:\n%s", hook)
	}
	if err, output := runCLIError("fork", "resolve", "topic:x@2", "--resolver", "owner"); err == nil || !strings.Contains(err.Error()+output, "has no unresolved fork") {
		t.Fatalf("misnamed revision fork: %v %s", err, output)
	}
	resolved := runCLI(t, "", "fork", "resolve", "decision:base", "--accept", "from-a", "--resolver", "owner")
	if !strings.Contains(resolved, "Resolved decision:base: accepted from-a; from-b now read as superseded by it") {
		t.Fatalf("fork resolve: %s", resolved)
	}
	if list := runCLI(t, "", "fork", "list"); !strings.Contains(list, "Forks: 0.") {
		t.Fatalf("fork list after resolving:\n%s", list)
	}
	if show := runCLI(t, "", "history", "show", "decision:from-b"); !strings.Contains(show, "Superseded by: from-a") {
		t.Fatalf("rejected head:\n%s", show)
	}
}
