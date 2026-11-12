package authorship

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/database"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func TestReadHeadFollowsLoosePackedAndWorktreeRefs(t *testing.T) {
	root := t.TempDir()
	git(t, root, "init", "-q", "-b", "main")
	git(t, root, "commit", "-q", "--allow-empty", "-m", "one")
	want := git(t, root, "rev-parse", "HEAD")
	if got, ok := ReadHead(root); !ok || got != want {
		t.Fatalf("loose ref: %q %v, want %s", got, ok, want)
	}
	git(t, root, "pack-refs", "--all")
	if got, ok := ReadHead(root); !ok || got != want {
		t.Fatalf("packed ref: %q %v, want %s", got, ok, want)
	}
	worktree := filepath.Join(t.TempDir(), "wt")
	git(t, root, "worktree", "add", "-q", "-b", "side", worktree)
	git(t, worktree, "commit", "-q", "--allow-empty", "-m", "two")
	if got, ok := ReadHead(worktree); !ok || got != git(t, worktree, "rev-parse", "HEAD") {
		t.Fatalf("worktree ref: %q %v", got, ok)
	}
	git(t, root, "checkout", "-q", "--detach")
	if got, ok := ReadHead(root); !ok || got != want {
		t.Fatalf("detached: %q %v", got, ok)
	}
}

func TestRanGitWrite(t *testing.T) {
	for input, want := range map[string]bool{
		`git commit -m "x"`:                      true,
		`cd repo && git add -A && git commit -q`: true,
		`git -C repo merge --ff-only topic`:      true,
		`git status && git log -3`:               false,
		`go test ./...`:                          false,
		`echo "commit" && ls`:                    false,
	} {
		if got := RanGitWrite(map[string]any{"command": input}); got != want {
			t.Errorf("%q: %v, want %v", input, got, want)
		}
	}
	if !RanGitWrite(map[string]any{"cmd": []any{"bash", "-lc", "git commit -m x"}}) {
		t.Error("argv form not recognized")
	}
}

func TestObserveAttributesOnlyCommitsMadeByAGitToolCall(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := database.Initialize(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO projects(id,root,name,created_at) VALUES ('p',?,'test','2026-01-01')`, root); err != nil {
		t.Fatal(err)
	}
	git(t, root, "init", "-q", "-b", "main")
	git(t, root, "commit", "-q", "--allow-empty", "-m", "base")
	observe := func(session string, attribute bool) int {
		t.Helper()
		recorded, err := Observe(ctx, db, Observation{ProjectID: "p", Root: root, SessionID: session, Client: "codex", TaskID: "task-" + session, ToolName: "Bash", Attribute: attribute, At: time.Now()})
		if err != nil {
			t.Fatal(err)
		}
		return recorded
	}
	if got := observe("a", true); got != 0 {
		t.Fatalf("baseline attributed %d commits", got)
	}
	// The engineer commits by hand; the next hook ran no Git command.
	git(t, root, "commit", "-q", "--allow-empty", "-m", "manual")
	manual := git(t, root, "rev-parse", "HEAD")
	if got := observe("a", false); got != 0 {
		t.Fatalf("manual commit attributed: %d", got)
	}
	// Agent b's tool call makes two commits.
	git(t, root, "commit", "-q", "--allow-empty", "-m", "b1")
	first := git(t, root, "rev-parse", "HEAD")
	git(t, root, "commit", "-q", "--allow-empty", "-m", "b2")
	second := git(t, root, "rev-parse", "HEAD")
	if got := observe("b", true); got != 2 {
		t.Fatalf("attributed %d commits, want 2", got)
	}
	authors, err := Authors(ctx, db, "p", "", []string{manual, first, second})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := authors[manual]; ok || authors[first].TaskID != "task-b" || authors[second].SessionID != "b" {
		t.Fatalf("authors = %#v", authors)
	}
	if again := observe("a", true); again != 0 {
		t.Fatalf("unchanged HEAD attributed %d commits", again)
	}
	commits, err := TaskCommits(ctx, db, "p", "task-b")
	if err != nil || len(commits) != 2 {
		t.Fatalf("task commits = %#v %v", commits, err)
	}
}

func TestObserveSeparatesCreatedCommitsFromIncomingHistory(t *testing.T) {
	for _, operation := range []string{"fetch-merge", "pull", "local-fast-forward", "merge", "pull-merge", "rebase", "cherry-pick", "amend", "missing-reflog"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			upstream, root := t.TempDir(), t.TempDir()
			git(t, upstream, "init", "-q", "-b", "main")
			git(t, upstream, "commit", "-q", "--allow-empty", "-m", "base")
			git(t, root, "clone", "-q", upstream, ".")
			db, err := database.Initialize(ctx, filepath.Join(t.TempDir(), "test.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec(`INSERT INTO projects(id,root,name,created_at) VALUES ('p',?,'test','2026-01-01')`, root); err != nil {
				t.Fatal(err)
			}
			observe := func(attribute bool) int {
				t.Helper()
				n, err := Observe(ctx, db, Observation{ProjectID: "p", Root: root, SessionID: "agent", Client: "codex", Attribute: attribute, At: time.Now()})
				if err != nil {
					t.Fatal(err)
				}
				return n
			}
			if operation == "merge" || operation == "pull-merge" || operation == "rebase" || operation == "cherry-pick" {
				git(t, root, "commit", "-q", "--allow-empty", "-m", "local")
			}
			if operation == "local-fast-forward" {
				git(t, root, "checkout", "-qb", "incoming")
				git(t, root, "commit", "-q", "--allow-empty", "-m", "incoming-local")
				git(t, root, "checkout", "-q", "main")
			}
			observe(false)
			git(t, upstream, "commit", "-q", "--allow-empty", "-m", "incoming")
			incoming := git(t, upstream, "rev-parse", "HEAD")
			want := 0
			switch operation {
			case "fetch-merge":
				git(t, root, "fetch", "-q", "origin")
				git(t, root, "merge", "--ff-only", "origin/main")
			case "pull":
				git(t, root, "pull", "-q", "--ff-only", "origin", "main")
			case "local-fast-forward":
				git(t, root, "merge", "--ff-only", "incoming")
			case "merge":
				git(t, root, "fetch", "-q", "origin")
				git(t, root, "merge", "--no-ff", "-m", "merge", "origin/main")
				want = 1
			case "pull-merge":
				git(t, root, "pull", "--no-rebase", "--no-ff", "origin", "main")
				want = 1
			case "rebase":
				git(t, root, "fetch", "-q", "origin")
				git(t, root, "rebase", "--keep-empty", "origin/main")
				want = 1
			case "cherry-pick":
				git(t, root, "fetch", "-q", "origin")
				git(t, root, "cherry-pick", "--allow-empty", "origin/main")
				want = 1
			case "amend":
				git(t, root, "commit", "--amend", "--allow-empty", "-m", "amended")
				want = 1
			case "missing-reflog":
				git(t, root, "reflog", "expire", "--expire=all", "--all")
				git(t, root, "commit", "-q", "--allow-empty", "-m", "new-without-baseline")
			}
			if got := observe(true); got != want {
				t.Fatalf("attributed %d, want %d; reflog:\n%s", got, want, git(t, root, "reflog"))
			}
			head := git(t, root, "rev-parse", "HEAD")
			authors, err := Authors(ctx, db, "p", "", []string{incoming, head})
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := authors[incoming]; ok {
				t.Fatal("incoming upstream commit attributed to this session")
			}
			if want == 1 && authors[head].SessionID != "agent" {
				t.Fatal("newly created merge/rebase commit missing attribution")
			}
			if got := observe(true); got != 0 {
				t.Fatalf("repeat attributed %d commits", got)
			}
		})
	}
}
