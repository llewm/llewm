package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Continuity Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func commitFile(t *testing.T, dir, name, content, message string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", name)
	gitRun(t, dir, "commit", "--quiet", "-m", message)
	return gitRun(t, dir, "rev-parse", "HEAD")
}

// landedRepo is a repository on main with one base commit.
func landedRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, "init", "--quiet", "--initial-branch=main")
	commitFile(t, dir, "base.txt", "base\n", "Base")
	return dir
}

func TestLandedInDetectsSquashAndRebaseMerges(t *testing.T) {
	ctx := context.Background()

	t.Run("squash of the checkpoint commit", func(t *testing.T) {
		dir := landedRepo(t)
		gitRun(t, dir, "checkout", "--quiet", "-b", "feature")
		commitFile(t, dir, "a.txt", "a\n", "A")
		checkpoint := commitFile(t, dir, "b.txt", "b\n", "B")
		gitRun(t, dir, "checkout", "--quiet", "main")
		commitFile(t, dir, "other.txt", "other\n", "Unrelated work on main")
		gitRun(t, dir, "merge", "--quiet", "--squash", "feature")
		gitRun(t, dir, "commit", "--quiet", "-m", "Squash feature")
		gitRun(t, dir, "branch", "--quiet", "-D", "feature")
		if got := landedIn(ctx, dir, checkpoint); got != "HEAD" {
			t.Fatalf("landedIn = %q, want HEAD", got)
		}
	})

	t.Run("squash that also took later commits", func(t *testing.T) {
		dir := landedRepo(t)
		gitRun(t, dir, "checkout", "--quiet", "-b", "feature")
		checkpoint := commitFile(t, dir, "a.txt", "a\n", "A")
		commitFile(t, dir, "b.txt", "b\n", "Later B")
		gitRun(t, dir, "checkout", "--quiet", "main")
		gitRun(t, dir, "merge", "--quiet", "--squash", "feature")
		gitRun(t, dir, "commit", "--quiet", "-m", "Squash feature")
		if got := landedIn(ctx, dir, checkpoint); got != "HEAD" {
			t.Fatalf("landedIn = %q, want HEAD", got)
		}
	})

	t.Run("rebase merge", func(t *testing.T) {
		dir := landedRepo(t)
		gitRun(t, dir, "checkout", "--quiet", "-b", "feature")
		commitFile(t, dir, "a.txt", "a\n", "A")
		checkpoint := commitFile(t, dir, "b.txt", "b\n", "B")
		gitRun(t, dir, "checkout", "--quiet", "main")
		commitFile(t, dir, "other.txt", "other\n", "Unrelated work on main")
		gitRun(t, dir, "cherry-pick", "--quiet", "main.."+checkpoint)
		if got := landedIn(ctx, dir, checkpoint); got != "HEAD" {
			t.Fatalf("landedIn = %q, want HEAD", got)
		}
	})

	t.Run("landed on the default branch, not the checked-out one", func(t *testing.T) {
		dir := landedRepo(t)
		gitRun(t, dir, "checkout", "--quiet", "-b", "feature")
		checkpoint := commitFile(t, dir, "a.txt", "a\n", "A")
		gitRun(t, dir, "checkout", "--quiet", "main")
		gitRun(t, dir, "merge", "--quiet", "--squash", "feature")
		gitRun(t, dir, "commit", "--quiet", "-m", "Squash feature")
		gitRun(t, dir, "checkout", "--quiet", "-b", "side", "main~1")
		commitFile(t, dir, "side.txt", "side\n", "Side")
		if got := landedIn(ctx, dir, checkpoint); got != "main" {
			t.Fatalf("landedIn = %q, want main", got)
		}
	})

	t.Run("not merged", func(t *testing.T) {
		dir := landedRepo(t)
		gitRun(t, dir, "checkout", "--quiet", "-b", "feature")
		checkpoint := commitFile(t, dir, "a.txt", "a\n", "A")
		gitRun(t, dir, "checkout", "--quiet", "main")
		commitFile(t, dir, "other.txt", "other\n", "Unrelated work on main")
		if got := landedIn(ctx, dir, checkpoint); got != "" {
			t.Fatalf("landedIn = %q, want nothing", got)
		}
	})

	t.Run("later commits rewrote the same lines", func(t *testing.T) {
		// Conservative: the squash conflicts with the checkpoint's version, so
		// it is not claimed as landed.
		dir := landedRepo(t)
		gitRun(t, dir, "checkout", "--quiet", "-b", "feature")
		checkpoint := commitFile(t, dir, "a.txt", "first\n", "A")
		commitFile(t, dir, "a.txt", "second\n", "Rewrite A")
		gitRun(t, dir, "checkout", "--quiet", "main")
		gitRun(t, dir, "merge", "--quiet", "--squash", "feature")
		gitRun(t, dir, "commit", "--quiet", "-m", "Squash feature")
		if got := landedIn(ctx, dir, checkpoint); got != "" {
			t.Fatalf("landedIn = %q, want nothing", got)
		}
	})
}

func TestCodeBehindLineReportsLandedChanges(t *testing.T) {
	commit := strings.Repeat("a", 40)
	here := CodeBehindLine("cp1", []CodeLag{{Commit: commit, Landed: "HEAD"}})
	if !strings.Contains(here, "landed here by squash or rebase merge") || strings.Contains(here, "Code is behind") {
		t.Fatalf("landed in HEAD: %q", here)
	}
	elsewhere := CodeBehindLine("cp1", []CodeLag{{Member: "app", Commit: commit, Landed: "origin/main"}, {Commit: strings.Repeat("b", 40)}})
	if !strings.Contains(elsewhere, "app@aaaaaaaaaaaa, whose changes landed on origin/main") || !strings.Contains(elsewhere, "Switch to origin/main") || !strings.Contains(elsewhere, "Code is behind or off the checkpoint: cp1 was written on bbbbbbbbbbbb") {
		t.Fatalf("landed elsewhere: %q", elsewhere)
	}
}
