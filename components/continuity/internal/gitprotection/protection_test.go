package gitprotection

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/gitenv"
)

func TestInspectExclusionAndTrackedState(t *testing.T) {
	for _, tc := range []struct {
		name, rules             string
		local, tracked, ignored bool
	}{
		{name: "missing"},
		{name: "shared", rules: "/.llewm/\n", ignored: true},
		{name: "local", rules: "/.llewm/\n", local: true, ignored: true},
		{name: "only component", rules: ".llewm/continuity/\n"},
		{name: "only files", rules: "*.db\n"},
		{name: "negated", rules: "/.llewm/\n!/.llewm/\n"},
		{name: "tracked despite ignore", rules: "/.llewm/\n", ignored: true, tracked: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			// Isolate global ignore configuration without changing the user's file.
			t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(root, "absent-global"))
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			git := func(args ...string) {
				t.Helper()
				if out, err := gitenv.Command(ctx, append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v: %s", args, err, out)
				}
			}
			git("init", "-q")
			path := filepath.Join(root, ".gitignore")
			if tc.local {
				path = filepath.Join(root, ".git", "info", "exclude")
			}
			if err := os.WriteFile(path, []byte(tc.rules), 0600); err != nil {
				t.Fatal(err)
			}
			if tc.tracked {
				if err := os.MkdirAll(filepath.Join(root, ".llewm"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, ".llewm", "private.txt"), []byte("local state"), 0600); err != nil {
					t.Fatal(err)
				}
				git("add", "-f", ".llewm/private.txt")
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			r := Inspect(ctx, root)
			if r.Error != "" || r.Ignored != tc.ignored || (r.TrackedFiles > 0) != tc.tracked {
				t.Fatalf("unexpected report: %+v", r)
			}
			if (len(r.Warnings()) == 0) != (tc.ignored && !tc.tracked) {
				t.Fatalf("unexpected warnings: %v", r.Warnings())
			}
			after, err := os.ReadFile(path)
			if err != nil || string(before) != string(after) {
				t.Fatal("inspection changed ignore rules")
			}
			if !tc.tracked {
				if _, err := os.Stat(filepath.Join(root, ".llewm")); !os.IsNotExist(err) {
					t.Fatal("inspection created state")
				}
			}
		})
	}
}

func TestInspectUnavailableGitIsUnknown(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	r := Inspect(context.Background(), t.TempDir())
	if r.Error == "" || len(r.Warnings()) == 0 {
		t.Fatalf("missing Git must not pass: %+v", r)
	}
}
