package memsync

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/app"
	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/decision"
	"github.com/llewm/llewm/components/continuity/internal/fork"
	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/topic"
)

const testLineage = "lineage:5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a"

type machine struct {
	db      *sql.DB
	project string
	syncer  Syncer
}

func newMachine(t *testing.T, name, remote string) machine {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	db, err := database.Initialize(ctx, filepath.Join(dir, "continuity.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	projectID, err := (activity.Store{DB: db}).EnsureProject(ctx, filepath.Join(dir, name), name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (portable.Service{DB: db}).Initialize(ctx, projectID, testLineage); err != nil {
		t.Fatal(err)
	}
	clone := filepath.Join(dir, "clone")
	if err := Init(ctx, remote, clone, Access{}, Setup{}); err != nil {
		t.Fatal(err)
	}
	return machine{db: db, project: projectID, syncer: Syncer{DB: db, ProjectID: projectID, Remote: remote, Clone: clone}}
}

func (m machine) decide(t *testing.T, id, title string) {
	t.Helper()
	if _, err := (decision.Service{DB: m.db}).Append(context.Background(), decision.Record{ProjectID: m.project, ID: id, Title: title, Rationale: "Because " + title, Disposition: "accepted", Actor: "owner", ActorType: "human", Authority: "caller_asserted"}); err != nil {
		t.Fatal(err)
	}
}

func (m machine) sync(t *testing.T, want string) Result {
	t.Helper()
	result, err := m.syncer.Sync(context.Background())
	if err != nil || result.Outcome != want {
		t.Fatalf("sync: outcome %s want %s, %#v, %v", result.Outcome, want, result, err)
	}
	return result
}

func (m machine) decisions(t *testing.T) string {
	t.Helper()
	rows, err := m.db.Query(`SELECT id FROM decision_records WHERE project_id=? ORDER BY id`, m.project)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return strings.Join(ids, ",")
}

func bareRemote(t *testing.T) string {
	t.Helper()
	// No global identity: memory commits must still work.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	remote := filepath.Join(t.TempDir(), "memory.git")
	if output, err := exec.Command("git", "init", "--quiet", "--bare", remote).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v %s", err, output)
	}
	return remote
}

func TestTwoMachinesSyncThroughOneRemote(t *testing.T) {
	remote := bareRemote(t)
	a := newMachine(t, "a", remote)
	b := newMachine(t, "b", remote)

	a.decide(t, "use-sqlite", "Use SQLite locally")
	first := a.sync(t, OutcomeSynced)
	if first.ExportedRecords != 1 || first.UnpushedCommits != 0 || first.ImportedHead == "" {
		t.Fatalf("first push: %#v", first)
	}
	pulled := b.sync(t, OutcomeSynced)
	if pulled.ImportedRecords != 1 || b.decisions(t) != "use-sqlite" {
		t.Fatalf("pull: %#v, decisions %q", pulled, b.decisions(t))
	}
	if again := b.sync(t, OutcomeSynced); again.ImportedRecords != 0 || again.ExportedRecords != 0 {
		t.Fatalf("idle sync did work: %#v", again)
	}

	// Both machines record while apart; neither sync conflicts.
	a.decide(t, "memory-repo", "Use a memory repository")
	b.decide(t, "leases-later", "Add leases later")
	a.sync(t, OutcomeSynced)
	b.sync(t, OutcomeSynced)
	a.sync(t, OutcomeSynced)
	for _, m := range []machine{a, b} {
		if got := m.decisions(t); got != "leases-later,memory-repo,use-sqlite" {
			t.Fatalf("converged decisions = %q", got)
		}
	}
	files, err := exec.Command("git", "-C", a.syncer.Clone, "ls-files").Output()
	if err != nil || strings.Count(string(files), "\n") != 3 || !strings.Contains(string(files), "v3/5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a/records/decision/") {
		t.Fatalf("memory repository files:\n%s %v", files, err)
	}
}

func TestIdleSyncSkipsThePush(t *testing.T) {
	remote := bareRemote(t)
	a := newMachine(t, "a", remote)
	a.decide(t, "first", "First")
	a.sync(t, OutcomeSynced)
	trace := filepath.Join(t.TempDir(), "trace")
	t.Setenv("GIT_TRACE", trace)
	pushes := func() int {
		t.Helper()
		body, err := os.ReadFile(trace)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		return strings.Count(string(body), "trace: built-in: git push")
	}
	a.sync(t, OutcomeSynced)
	if got := pushes(); got != 0 {
		t.Fatalf("idle sync pushed %d times", got)
	}
	a.decide(t, "second", "Second")
	a.sync(t, OutcomeSynced)
	if got := pushes(); got != 1 {
		t.Fatalf("sync with a new record pushed %d times", got)
	}
}

func TestSyncKeepsLocalCommitsWhileOffline(t *testing.T) {
	remote := bareRemote(t)
	a := newMachine(t, "a", remote)
	a.decide(t, "first", "First")
	a.sync(t, OutcomeSynced)

	hidden := remote + ".away"
	if err := os.Rename(remote, hidden); err != nil {
		t.Fatal(err)
	}
	a.decide(t, "second", "Second")
	offline := a.sync(t, OutcomeOffline)
	if offline.ExportedRecords != 1 || offline.UnpushedCommits != 1 || !strings.Contains(offline.Detail, "remote unreachable") {
		t.Fatalf("offline: %#v", offline)
	}
	a.sync(t, OutcomeOffline)
	status, err := ReadStatus(context.Background(), a.db, a.project)
	if err != nil {
		t.Fatal(err)
	}
	status.Configured = true
	if line := status.Line(time.Now().UTC()); !strings.Contains(line, "1 unpushed commits") || !strings.Contains(line, "remote unreachable (2 attempts)") || !strings.Contains(line, "last synced") {
		t.Fatalf("status line = %q", line)
	}

	if err := os.Rename(hidden, remote); err != nil {
		t.Fatal(err)
	}
	if back := a.sync(t, OutcomeSynced); back.UnpushedCommits != 0 {
		t.Fatalf("reconnected: %#v", back)
	}
	b := newMachine(t, "b", remote)
	b.sync(t, OutcomeSynced)
	if got := b.decisions(t); got != "first,second" {
		t.Fatalf("records written offline did not arrive: %q", got)
	}
}

func TestSyncRefusesCollidingRecords(t *testing.T) {
	remote := bareRemote(t)
	a := newMachine(t, "a", remote)
	b := newMachine(t, "b", remote)
	a.decide(t, "shared", "Machine A's version")
	a.sync(t, OutcomeSynced)
	b.decide(t, "shared", "Machine B's version")
	refused := b.sync(t, OutcomeCollision)
	if refused.Collisions != 1 || refused.UnpushedCommits != 0 || !strings.Contains(refused.Detail, "1 records have conflicting versions") {
		t.Fatalf("collision: %#v", refused)
	}
	var title string
	if err := b.db.QueryRow(`SELECT title FROM decision_records WHERE project_id=? AND id='shared'`, b.project).Scan(&title); err != nil || title != "Machine B's version" {
		t.Fatalf("local record changed: %q %v", title, err)
	}
	status, err := ReadStatus(context.Background(), b.db, b.project)
	if err != nil {
		t.Fatal(err)
	}
	status.Configured = true
	if line := status.Line(time.Now().UTC()); !strings.Contains(line, "1 records with conflicting versions") {
		t.Fatalf("status line = %q", line)
	}
	// Machine A sees both versions too, other records still flow, and the
	// collision is reported again on the next sync.
	b.decide(t, "unrelated", "Unrelated")
	b.sync(t, OutcomeCollision)
	again := a.sync(t, OutcomeCollision)
	if again.Collisions != 1 || !strings.Contains(a.decisions(t), "unrelated") {
		t.Fatalf("machine A after collision: %#v, decisions %q", again, a.decisions(t))
	}
	if repeat := a.sync(t, OutcomeCollision); repeat.Collisions != 1 {
		t.Fatalf("collision was forgotten: %#v", repeat)
	}
}

func (m machine) topic(t *testing.T, id, title string) {
	t.Helper()
	if _, err := (topic.Service{DB: m.db}).Record(context.Background(), topic.Topic{ProjectID: m.project, ID: id, Title: title, Creator: "owner"}); err != nil {
		t.Fatal(err)
	}
}

func (m machine) topicTitle(t *testing.T, id string, revision int) string {
	t.Helper()
	var title string
	if err := m.db.QueryRow(`SELECT title FROM topic_revisions WHERE project_id=? AND id=? AND revision=?`, m.project, id, revision).Scan(&title); err != nil {
		t.Fatal(err)
	}
	return title
}

func (m machine) forks(t *testing.T) []fork.RevisionFork {
	t.Helper()
	forks, err := fork.RevisionForks(context.Background(), m.db, m.project, "")
	if err != nil {
		t.Fatal(err)
	}
	return forks
}

func TestConcurrentRevisionsBecomeAForkInsteadOfACollision(t *testing.T) {
	remote := bareRemote(t)
	a := newMachine(t, "a", remote)
	b := newMachine(t, "b", remote)
	a.topic(t, "sync", "Base")
	a.sync(t, OutcomeSynced)
	b.sync(t, OutcomeSynced)
	// Both machines write revision 2 before syncing.
	a.topic(t, "sync", "Machine A's revision")
	a.sync(t, OutcomeSynced)
	b.topic(t, "sync", "Machine B's revision")
	b.decide(t, "unrelated", "Unrelated")
	forked := b.sync(t, OutcomeSynced)
	if forked.Forks != 1 || forked.Collisions != 0 || forked.ImportedHead == "" || forked.UnpushedCommits != 0 || !strings.Contains(forked.Detail, "1 record revisions have several versions") {
		t.Fatalf("fork sync: %#v", forked)
	}
	if got := b.topicTitle(t, "sync", 2); got != "Machine B's revision" {
		t.Fatalf("local revision changed: %q", got)
	}
	forks := b.forks(t)
	if len(forks) != 1 || forks[0].Ref() != "topic_revision:sync@2" || len(forks[0].Heads) != 2 || !forks[0].Proposed().Local || forks[0].Heads[1].Local || forks[0].Proposed().Title != "Machine B's revision" {
		t.Fatalf("machine B forks: %#v", forks)
	}
	// A repeat sync finds nothing new and records nothing twice.
	if again := b.sync(t, OutcomeSynced); again.Forks != 0 {
		t.Fatalf("repeat sync: %#v", again)
	}
	if forks := b.forks(t); len(forks) != 1 || len(forks[0].Heads) != 2 {
		t.Fatalf("repeat sync changed forks: %#v", forks)
	}
	// Machine A keeps its own version, and other records still flow.
	if seen := a.sync(t, OutcomeSynced); seen.Forks != 1 || !strings.Contains(a.decisions(t), "unrelated") {
		t.Fatalf("machine A: %#v, decisions %q", seen, a.decisions(t))
	}
	if got := a.topicTitle(t, "sync", 2); got != "Machine A's revision" {
		t.Fatalf("machine A's revision changed: %q", got)
	}
	if forks := a.forks(t); len(forks) != 1 || forks[0].Proposed().Title != "Machine B's revision" || forks[0].Proposed().Local || !forks[0].Heads[1].Local {
		t.Fatalf("machine A forks: %#v", forks)
	}
	// A machine with neither version takes the proposed one and sees the fork.
	c := newMachine(t, "c", remote)
	if joined := c.sync(t, OutcomeSynced); joined.Forks != 1 {
		t.Fatalf("machine C: %#v", joined)
	}
	if got := c.topicTitle(t, "sync", 2); got != "Machine B's revision" {
		t.Fatalf("machine C adopted %q", got)
	}
	if forks := c.forks(t); len(forks) != 1 || !forks[0].Proposed().Local {
		t.Fatalf("machine C forks: %#v", forks)
	}

	// Machine B accepts machine A's head: it becomes revision 3 everywhere.
	resolver := &app.App{DB: b.db, ProjectID: b.project}
	if _, err := resolver.ResolveRevisionFork(context.Background(), "topic_revision:sync@2", "abc", "owner", "user_reported"); err == nil {
		t.Fatal("a head that does not exist was accepted")
	}
	other := b.forks(t)[0].Heads[1].ContentSHA256
	resolved, err := resolver.ResolveRevisionFork(context.Background(), "topic_revision:sync@2", other[:12], "owner", "user_reported")
	if err != nil || resolved.Revision != 3 || resolved.Resolution.AcceptedHead != other || resolved.Resolution.Continuation != "topic_revision:sync@3" || len(resolved.Resolution.RejectedHeads) != 1 {
		t.Fatalf("resolve: %#v %v", resolved, err)
	}
	if got := b.topicTitle(t, "sync", 3); got != "Machine A's revision" || len(b.forks(t)) != 0 {
		t.Fatalf("machine B after resolving: %q, forks %#v", got, b.forks(t))
	}
	if _, err := resolver.ResolveRevisionFork(context.Background(), "topic_revision:sync@2", "", "owner", "user_reported"); err == nil {
		t.Fatal("a resolved fork was resolved again")
	}
	b.sync(t, OutcomeSynced)
	for name, m := range map[string]machine{"A": a, "C": c} {
		m.sync(t, OutcomeSynced)
		if got := m.topicTitle(t, "sync", 3); got != "Machine A's revision" || len(m.forks(t)) != 0 {
			t.Fatalf("machine %s after the resolution synced: %q, forks %#v", name, got, m.forks(t))
		}
	}
}

func TestInitRefusesACloneOfAnotherRemote(t *testing.T) {
	first, second := bareRemote(t), bareRemote(t)
	clone := filepath.Join(t.TempDir(), "clone")
	if err := Init(context.Background(), first, clone, Access{}, Setup{}); err != nil {
		t.Fatal(err)
	}
	if err := Init(context.Background(), first, clone, Access{}, Setup{}); err != nil {
		t.Fatalf("re-init: %v", err)
	}
	if err := Init(context.Background(), second, clone, Access{}, Setup{}); err == nil || !strings.Contains(err.Error(), "is a clone of") {
		t.Fatalf("wrong remote: %v", err)
	}
}

func TestDefaultCloneIsStablePerRemote(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/data")
	a, _ := DefaultClone("git@github.com:org/llewm-memory.git")
	b, _ := DefaultClone("https://github.com/org/llewm-memory.git")
	if !strings.HasPrefix(a, "/data/llewm/memory/llewm-memory-") || a == b {
		t.Fatalf("clones: %s %s", a, b)
	}
	if again, _ := DefaultClone("git@github.com:org/llewm-memory.git"); again != a {
		t.Fatalf("unstable clone path: %s %s", a, again)
	}
	if scp, _ := DefaultClone("flux@host:llewm-memory.git"); !strings.HasPrefix(scp, "/data/llewm/memory/llewm-memory-") {
		t.Fatalf("scp-like remote clone: %s", scp)
	}
}

func TestTimedOutSyncIsRecorded(t *testing.T) {
	remote := bareRemote(t)
	a := newMachine(t, "a", remote)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := a.syncer.Sync(ctx)
	if err == nil || result.Outcome != OutcomeError {
		t.Fatalf("canceled sync: %#v %v", result, err)
	}
	status, err := ReadStatus(context.Background(), a.db, a.project)
	if err != nil || status.Latest == nil || status.Latest.Outcome != OutcomeError {
		t.Fatalf("canceled attempt was not recorded: %#v %v", status, err)
	}
}

func TestNothingToSyncIntoAnEmptyRemote(t *testing.T) {
	m := newMachine(t, "a", bareRemote(t))
	if result := m.sync(t, OutcomeSynced); result.ExportedRecords != 0 || result.UnpushedCommits != 0 {
		t.Fatalf("empty sync: %#v", result)
	}
}

// TestAmbientGitEnvironmentCannotRedirectSync runs Init and Sync as a Git hook
// would, with repository-selecting variables pointing at another repository, and
// checks that repository is untouched while the sync still reaches the remote.
func TestAmbientGitEnvironmentCannotRedirectSync(t *testing.T) {
	remote := bareRemote(t)
	decoy := filepath.Join(t.TempDir(), "code")
	if output, err := exec.Command("git", "init", "--quiet", decoy).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, output)
	}
	t.Setenv("GIT_DIR", filepath.Join(decoy, ".git"))
	t.Setenv("GIT_WORK_TREE", decoy)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(decoy, ".git", "index"))
	t.Setenv("GIT_CONFIG_PARAMETERS", "'core.hookspath'='"+decoy+"'")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "remote.origin.url")
	t.Setenv("GIT_CONFIG_VALUE_0", decoy)

	m := newMachine(t, "a", remote)
	m.decide(t, "decision-a", "From a hook")
	if result := m.sync(t, OutcomeSynced); result.ExportedRecords == 0 {
		t.Fatalf("nothing exported: %#v", result)
	}
	for _, name := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0"} {
		os.Unsetenv(name)
	}
	if output, err := exec.Command("git", "-C", decoy, "rev-parse", "--verify", "--quiet", "HEAD").CombinedOutput(); err == nil {
		t.Fatalf("the decoy repository gained a commit: %s", output)
	}
	if entries, _ := os.ReadDir(decoy); len(entries) != 1 {
		t.Fatalf("the decoy work tree changed: %v", entries)
	}
	if count, err := exec.Command("git", "-C", remote, "rev-list", "--count", "--all").Output(); err != nil || strings.TrimSpace(string(count)) == "0" {
		t.Fatalf("the remote received nothing: %s %v", count, err)
	}
}

// TestRewrittenRemoteIsRefused points the configured remote elsewhere through
// url.*.insteadOf and checks Init and Sync refuse instead of following it.
func TestRewrittenRemoteIsRefused(t *testing.T) {
	remote, other := bareRemote(t), bareRemote(t)
	m := newMachine(t, "a", remote)
	global := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(global, []byte("[url \""+other+"\"]\n\tinsteadOf = "+remote+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	m.decide(t, "decision-a", "Rewritten")
	if result, err := m.syncer.Sync(context.Background()); err == nil || !strings.Contains(err.Error(), "refusing to sync") {
		t.Fatalf("sync followed the rewrite: %#v %v", result, err)
	}
	if err := Init(context.Background(), remote, filepath.Join(t.TempDir(), "fresh"), Access{}, Setup{}); err == nil || !strings.Contains(err.Error(), "refusing to clone") {
		t.Fatalf("init followed the rewrite: %v", err)
	}
	if count, _ := exec.Command("git", "-C", other, "rev-list", "--count", "--all").Output(); strings.TrimSpace(string(count)) != "0" {
		t.Fatalf("the rewrite target received commits: %s", count)
	}
}

func TestStatusLineShowsSettings(t *testing.T) {
	status := Status{Configured: true, Settings: []string{"automatic sync off (manual = true)", "SSH key /k"}}
	if line := status.Line(time.Now().UTC()); line != "Sync: configured, never run (llewm sync) · settings: automatic sync off (manual = true), SSH key /k" {
		t.Fatalf("never-run line: %q", line)
	}
	status.Latest = &Attempt{Result: Result{Outcome: OutcomeSynced}, FinishedAt: time.Now().UTC()}
	status.LastSynced = status.Latest
	if line := status.Line(time.Now().UTC()); line != "Sync: last synced just now · settings: automatic sync off (manual = true), SSH key /k" {
		t.Fatalf("synced line: %q", line)
	}
}

// TestUnknownRecordTypesAreDeferred stands in for a newer machine that syncs a
// record type this build cannot read: the rest still imports, the unknown record
// is counted, and the watermark holds so an upgraded build imports it later.
func TestUnknownRecordTypesAreDeferred(t *testing.T) {
	remote := bareRemote(t)
	a, b := newMachine(t, "a", remote), newMachine(t, "b", remote)
	a.decide(t, "decision-a", "Known record")
	a.sync(t, OutcomeSynced)

	encoded, err := portable.EncodeEnvelope(portable.EnvelopeSpec{LineageID: testLineage, RecordType: "decision", RecordSchemaVersion: 1, RecordID: "future", RecordedAt: time.Now().UTC(), Authority: "caller_asserted", Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	e := encoded.Envelope
	e.RecordType = "future_type"
	e.EnvelopeSHA256 = ""
	body, _ := json.Marshal(e)
	canonical, err := portable.CanonicalizeJSON(body)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	e.EnvelopeSHA256 = hex.EncodeToString(sum[:])
	body, _ = json.Marshal(e)
	canonical, err = portable.CanonicalizeJSON(body)
	if err != nil {
		t.Fatal(err)
	}
	future := filepath.Join(a.syncer.Clone, portable.MemoryRecordPath(e))
	if err := os.MkdirAll(filepath.Dir(future), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(future, append(canonical, '\n'), 0600); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{{"add", "--all"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "--quiet", "-m", "future"}, {"push", "--quiet", "origin", "HEAD"}} {
		if output, err := exec.Command("git", append([]string{"-C", a.syncer.Clone}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, output)
		}
	}

	first := b.sync(t, OutcomeSynced)
	if first.ImportedRecords == 0 || first.DeferredRecords != 1 || first.ImportedHead != "" {
		t.Fatalf("first sync: %#v", first)
	}
	if got := b.decisions(t); got != "decision-a" {
		t.Fatalf("known record not imported: %q", got)
	}
	status, err := ReadStatus(context.Background(), b.db, b.project)
	if err != nil {
		t.Fatal(err)
	}
	status.Configured = true
	if line := status.Line(time.Now().UTC()); !strings.Contains(line, "1 records need a newer Continuity") {
		t.Fatalf("deferral not shown: %q", line)
	}
	if again := b.sync(t, OutcomeSynced); again.DeferredRecords != 1 || again.ImportedRecords != 0 {
		t.Fatalf("second sync: %#v", again)
	}
}

func TestSessionSyncImportsAndLeavesThePushToTheBackground(t *testing.T) {
	remote := bareRemote(t)
	a, b := newMachine(t, "a", remote), newMachine(t, "b", remote)
	a.decide(t, "first", "First")
	a.sync(t, OutcomeSynced)

	b.decide(t, "second", "Second")
	result, err := b.syncer.Session(context.Background(), 5*time.Second)
	if err != nil || result.Outcome != OutcomeDeferred || result.UnpushedCommits != 1 || !strings.Contains(result.Detail, "background sync pushes") {
		t.Fatalf("session sync: %#v %v", result, err)
	}
	if got := b.decisions(t); got != "first,second" {
		t.Fatalf("session sync did not import before the bootstrap: %q", got)
	}
	status, err := ReadStatus(context.Background(), b.db, b.project)
	if err != nil {
		t.Fatal(err)
	}
	status.Configured = true
	if line := status.Line(time.Now().UTC()); !strings.Contains(line, "never synced") || !strings.Contains(line, "background sync pushes") {
		t.Fatalf("status line = %q", line)
	}
	// The background sync finishes the push.
	if pushed := b.sync(t, OutcomeSynced); pushed.UnpushedCommits != 0 {
		t.Fatalf("background sync: %#v", pushed)
	}
	a.sync(t, OutcomeSynced)
	if got := a.decisions(t); got != "first,second" {
		t.Fatalf("pushed record did not arrive: %q", got)
	}

	// With nothing to push, a session sync is a complete sync.
	if idle, err := b.syncer.Session(context.Background(), 5*time.Second); err != nil || idle.Outcome != OutcomeSynced {
		t.Fatalf("idle session sync: %#v %v", idle, err)
	}
}

func TestSpentSessionBudgetStillFinishesLocalWork(t *testing.T) {
	remote := bareRemote(t)
	a := newMachine(t, "a", remote)
	a.decide(t, "first", "First")
	result, err := a.syncer.Session(context.Background(), 0)
	if err != nil || result.Outcome != OutcomeDeferred || result.ExportedRecords != 1 || result.UnpushedCommits != 1 || !strings.Contains(result.Detail, "no time left to fetch") {
		t.Fatalf("spent budget: %#v %v", result, err)
	}
	if output, err := exec.Command("git", "-C", a.syncer.Clone, "status", "--porcelain").CombinedOutput(); err != nil || len(output) != 0 {
		t.Fatalf("clone not clean after a spent budget: %q %v", output, err)
	}
	a.sync(t, OutcomeSynced)
}

func TestCanceledSyncStopsBetweenStepsWithACleanClone(t *testing.T) {
	remote := bareRemote(t)
	a := newMachine(t, "a", remote)
	a.decide(t, "first", "First")
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel as soon as the export's commit exists, while later steps remain.
	go func() {
		for ctx.Err() == nil {
			if exec.Command("git", "-C", a.syncer.Clone, "rev-parse", "--verify", "--quiet", "HEAD").Run() == nil {
				cancel()
			}
		}
	}()
	result, err := a.syncer.Sync(ctx)
	cancel()
	if err != nil && (result.Outcome != OutcomeError || !strings.Contains(result.Detail, "sync stopped before")) {
		t.Fatalf("canceled sync: %#v %v", result, err)
	}
	for _, leftover := range []string{"index.lock", "rebase-merge", "rebase-apply"} {
		if _, err := os.Stat(filepath.Join(a.syncer.Clone, ".git", leftover)); err == nil {
			t.Fatalf("canceled sync left %s behind", leftover)
		}
	}
	a.sync(t, OutcomeSynced)
	if b := newMachine(t, "b", remote); b.sync(t, OutcomeSynced).ImportedRecords != 1 {
		t.Fatal("record committed before the cancel was not pushed later")
	}
}

func TestSessionSyncStopsWaitingOnAHungRemoteAtItsBudget(t *testing.T) {
	for _, name := range []string{"https_proxy", "HTTPS_PROXY", "http_proxy", "HTTP_PROXY", "all_proxy", "ALL_PROXY"} {
		t.Setenv(name, "")
	}
	a := newMachine(t, "a", bareRemote(t))
	// A remote that accepts connections and never answers.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			defer connection.Close()
		}
	}()
	hung := "http://" + listener.Addr().String() + "/memory"
	if output, err := exec.Command("git", "-C", a.syncer.Clone, "remote", "set-url", "origin", hung).CombinedOutput(); err != nil {
		t.Fatalf("set-url: %v %s", err, output)
	}
	a.syncer.Remote = hung
	a.decide(t, "first", "First")

	started := time.Now()
	result, err := a.syncer.Session(context.Background(), 1500*time.Millisecond)
	// Well under budget plus WaitDelay: Git and its transport helper both stop.
	if elapsed := time.Since(started); elapsed > 3500*time.Millisecond {
		t.Fatalf("session sync took %s with a 1.5s budget", elapsed)
	}
	if err != nil || result.Outcome != OutcomeDeferred || result.ExportedRecords != 1 || result.UnpushedCommits != 1 || !strings.Contains(result.Detail, "fetch did not finish within 1.5s") {
		t.Fatalf("hung remote: %#v %v", result, err)
	}
	for _, leftover := range []string{"index.lock", "shallow.lock", "FETCH_HEAD.lock"} {
		if _, err := os.Stat(filepath.Join(a.syncer.Clone, ".git", leftover)); err == nil {
			t.Fatalf("interrupted fetch left %s behind", leftover)
		}
	}
}

// plantFormerFormat writes the previous wire encoding, including a record
// which exists only on the old remote, rather than the converting machine.
func plantFormerFormat(t *testing.T, m machine) {
	t.Helper()
	export, err := portable.EncodeMemoryExport(context.Background(), m.db, m.project)
	if err != nil {
		t.Fatal(err)
	}
	for _, current := range export.Envelopes {
		old := current.Envelope
		old.Format, old.FormatVersion, old.EnvelopeSHA256 = "llmem-portable-record", 2, ""
		body, _ := json.Marshal(old)
		canonical, err := portable.CanonicalizeJSON(body)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(canonical)
		old.EnvelopeSHA256 = hex.EncodeToString(sum[:])
		body, _ = json.Marshal(old)
		canonical, err = portable.CanonicalizeJSON(body)
		if err != nil {
			t.Fatal(err)
		}
		relative := strings.Replace(filepath.ToSlash(portable.MemoryRecordPath(old)), "v3/", "v2/", 1)
		path := filepath.Join(m.syncer.Clone, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(canonical, '\n'), 0644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"add", "--all"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "--quiet", "-m", "format-2 observations"}, {"push", "--quiet", "origin", "HEAD"}} {
		if output, err := exec.Command("git", append([]string{"-C", m.syncer.Clone}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, output)
		}
	}
}

func TestFormatTwoConversionPreservesRemoteOnlyHistory(t *testing.T) {
	remote := bareRemote(t)
	a := newMachine(t, "a", remote)
	b := newMachine(t, "b", remote)
	a.decide(t, "use-sqlite", "Use SQLite locally")
	a.sync(t, OutcomeSynced)
	a.decide(t, "legacy-only", "Retain remote-only history")
	plantFormerFormat(t, a)

	b.decide(t, "memory-repo", "Use a memory repository")
	result, err := b.syncer.Sync(context.Background())
	if err == nil || !strings.Contains(err.Error(), "llewm sync convert") || result.Outcome != OutcomeError {
		t.Fatalf("sync past format-2 observations: %#v %v", result, err)
	}
	if b.decisions(t) != "memory-repo,use-sqlite" {
		t.Fatalf("current records were not imported beside format-2 observations: %q", b.decisions(t))
	}

	converted, err := b.syncer.Convert(context.Background())
	if err != nil || !converted.Converted || converted.Outcome != OutcomeSynced || converted.UnpushedCommits != 0 {
		t.Fatalf("convert: %#v %v", converted, err)
	}
	log, err := exec.Command("git", "-C", b.syncer.Clone, "log", "-1", "--format=%s").Output()
	if err != nil || !strings.Contains(string(log), "convert to format 3") {
		t.Fatalf("conversion commit: %q %v", log, err)
	}
	a.sync(t, OutcomeSynced)
	for _, m := range []machine{a, b} {
		if _, err := os.Stat(filepath.Join(m.syncer.Clone, portable.FormerMemoryRepositoryRoot)); !os.IsNotExist(err) {
			t.Fatalf("format-2 observations remain in %s: %v", m.syncer.Clone, err)
		}
		if got := m.decisions(t); got != "legacy-only,memory-repo,use-sqlite" {
			t.Fatalf("decisions after conversion = %q", got)
		}
	}
	if again, err := a.syncer.Convert(context.Background()); err != nil || !again.Converted || again.ExportedRecords != 0 {
		t.Fatalf("second conversion was not idempotent: %#v %v", again, err)
	}
	c := newMachine(t, "fresh", remote)
	c.sync(t, OutcomeSynced)
	if got := c.decisions(t); got != "legacy-only,memory-repo,use-sqlite" {
		t.Fatalf("fresh machine lost history: %s", got)
	}
	history, err := exec.Command("git", "-C", c.syncer.Clone, "log", "--all", "--format=%s").Output()
	if err != nil || !strings.Contains(string(history), "format-2 observations") {
		t.Fatalf("original memory history lost: %s %v", history, err)
	}

}

// A real HTTP authentication challenge must leave automatic-sync records
// committed locally without launching any configured askpass process.
func TestAutomaticSyncAuthenticationKeepsLocalCommit(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_ASKPASS", "/must-not-launch-askpass")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Basic realm="fixture"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	for _, session := range []bool{false, true} {
		m := newMachine(t, "auth", bareRemote(t))
		m.decide(t, "keep-local", "Retain local work")
		repo := git{Dir: m.syncer.Clone}
		if _, err := repo.run(context.Background(), "remote", "set-url", "origin", server.URL); err != nil {
			t.Fatal(err)
		}
		m.syncer.Remote = server.URL
		m.syncer.Askpass = "/must-not-launch-fallback"
		var result Result
		var err error
		if session {
			result, err = m.syncer.Session(context.Background(), 10*time.Second)
		} else {
			m.syncer.NoPrompt = true
			result, err = m.syncer.Sync(context.Background())
		}
		if err != nil || result.Outcome != OutcomeOffline || result.UnpushedCommits != 1 || !strings.Contains(result.Detail, "authentication unavailable:") || strings.Contains(result.Detail, "must-not-launch") {
			t.Fatalf("session=%v: result=%+v err=%v", session, result, err)
		}
		status := Status{Configured: true, Latest: &Attempt{Result: result}, OfflineStreak: 1}
		if line := status.Line(time.Now()); !strings.Contains(line, "authentication unavailable") || strings.Contains(line, "remote unreachable") {
			t.Fatal(line)
		}
	}
}
