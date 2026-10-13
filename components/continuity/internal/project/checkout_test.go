package project

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir, "-c", "protocol.file.allow=always"}, args...)...)
	command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid", "GIT_CONFIG_GLOBAL=/dev/null")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func initRepository(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "init", "-q")
	git(t, dir, "commit", "-q", "--allow-empty", "-m", "initial")
	return dir
}

// checkoutFixture builds super (with submodule lib, which has nested submodule
// inner) and a linked worktree wt of super.
func checkoutFixture(t *testing.T) (string, string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	inner := initRepository(t, filepath.Join(base, "inner"))
	lib := initRepository(t, filepath.Join(base, "lib"))
	git(t, lib, "submodule", "add", "-q", inner, "inner")
	git(t, lib, "commit", "-q", "-m", "add inner")
	super := initRepository(t, filepath.Join(base, "super"))
	git(t, super, "submodule", "add", "-q", lib, "lib")
	git(t, super, "submodule", "update", "-q", "--init", "--recursive")
	git(t, super, "commit", "-q", "-m", "add lib")
	git(t, super, "worktree", "add", "-q", filepath.Join(base, "wt"))
	return base, super
}

func TestResolveCheckoutClassifiesRepositoriesWorktreesAndSubmodules(t *testing.T) {
	base, super := checkoutFixture(t)
	for _, test := range []struct {
		start, root, path, kind string
	}{
		{filepath.Join(super), super, super, KindRepository},
		{filepath.Join(base, "wt"), super, filepath.Join(base, "wt"), KindWorktree},
		{filepath.Join(super, "lib"), super, filepath.Join(super, "lib"), KindSubmodule},
		{filepath.Join(super, "lib", "inner"), super, filepath.Join(super, "lib", "inner"), KindSubmodule},
		{filepath.Join(base, "lib"), filepath.Join(base, "lib"), filepath.Join(base, "lib"), KindRepository},
	} {
		got, err := ResolveCheckout(test.start)
		if err != nil {
			t.Fatalf("ResolveCheckout(%s): %v", test.start, err)
		}
		if got.Root != test.root || got.Path != test.path || got.Kind != test.kind {
			t.Fatalf("ResolveCheckout(%s) = %#v, want root=%s path=%s kind=%s", test.start, got, test.root, test.path, test.kind)
		}
	}
}

func TestResolveCheckoutKeepsWorktreesAndSubmodulesWithTheirOwnStore(t *testing.T) {
	base, super := checkoutFixture(t)
	for _, dir := range []string{filepath.Join(base, "wt"), filepath.Join(super, "lib")} {
		if err := os.Mkdir(filepath.Join(dir, StateDirName), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct{ start, root string }{
		{filepath.Join(base, "wt"), filepath.Join(base, "wt")},
		{filepath.Join(super, "lib"), filepath.Join(super, "lib")},
		// A nested submodule stops at the nearest standalone ancestor.
		{filepath.Join(super, "lib", "inner"), filepath.Join(super, "lib")},
	} {
		got, err := ResolveCheckout(test.start)
		if err != nil {
			t.Fatal(err)
		}
		if got.Root != test.root {
			t.Fatalf("ResolveCheckout(%s).Root = %s, want %s", test.start, got.Root, test.root)
		}
	}
}

func TestResolveCheckoutTreatsSeparateGitDirAsOneRepository(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(base, "work")
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	git(t, work, "init", "-q", "--separate-git-dir", filepath.Join(base, "store.git"))
	got, err := ResolveCheckout(filepath.Join(work))
	if err != nil {
		t.Fatal(err)
	}
	if got.Root != work || got.Kind != KindRepository {
		t.Fatalf("ResolveCheckout = %#v", got)
	}
}

func TestStoreInside(t *testing.T) {
	for _, test := range []struct {
		database, root string
		want           bool
	}{
		{"/work/a/.continuity/continuity.db", "/work/a", true},
		{"/work/ab/.continuity/continuity.db", "/work/a", false},
		{"/home/me/.continuity-home/continuity.db", "/work/a", false},
	} {
		if got := StoreInside(test.database, test.root); got != test.want {
			t.Fatalf("StoreInside(%s, %s) = %t", test.database, test.root, got)
		}
	}
}
