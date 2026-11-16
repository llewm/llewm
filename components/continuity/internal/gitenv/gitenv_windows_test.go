package gitenv

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandEnablesLongPathsOnWindows(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	out, err := Command(context.Background(), "config", "--get", "core.longpaths").Output()
	if err != nil || strings.TrimSpace(string(out)) != "true" {
		t.Fatalf("core.longpaths = %q, %v", out, err)
	}
	// An object path beyond MAX_PATH must still be writable. core.longpaths
	// covers files inside the repository, not Git's working directory: from
	// about 240 characters Git warns, from about 252 discovery fails, and from
	// 260 it cannot chdir at all. So the repository directory has an exact
	// length below those limits whatever the TempDir length, while the objects
	// written inside it exceed MAX_PATH.
	const repoLen, maxPath = 230, 260
	deep := t.TempDir()
	if len(deep) > repoLen-2 {
		t.Skipf("TempDir %q leaves no room for a %d-character repository", deep, repoLen)
	}
	// Each segment costs a separator plus its name; never leave exactly one
	// character, which no segment could fill.
	for r := repoLen - len(deep); r > 0; r = repoLen - len(deep) {
		n := min(40, r-1)
		if r-1-n == 1 {
			n--
		}
		deep = filepath.Join(deep, strings.Repeat("d", n))
	}
	if len(deep) != repoLen {
		t.Fatalf("repository path %d characters, want %d", len(deep), repoLen)
	}
	if output, err := Command(context.Background(), "init", "--quiet", "--bare", deep).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, output)
	}
	write := Command(context.Background(), "-C", deep, "hash-object", "-w", "--stdin")
	write.Stdin = strings.NewReader("long path object")
	output, err := write.CombinedOutput()
	if err != nil {
		t.Fatalf("hash-object beyond MAX_PATH: %v %s", err, output)
	}
	id := strings.TrimSpace(string(output))
	object := filepath.Join(deep, "objects", id[:2], id[2:])
	if len(object) <= maxPath {
		t.Fatalf("object path %d characters does not exceed MAX_PATH", len(object))
	}
	if _, err := os.Stat(object); err != nil {
		t.Fatalf("object beyond MAX_PATH not written: %v", err)
	}
}
