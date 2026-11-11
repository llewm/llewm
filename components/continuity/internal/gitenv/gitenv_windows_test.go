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
	// An object path beyond MAX_PATH must still be writable.
	deep := t.TempDir()
	for len(deep) < 240 {
		deep = filepath.Join(deep, strings.Repeat("d", 40))
	}
	if output, err := Command(context.Background(), "init", "--quiet", "--bare", deep).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, output)
	}
	write := Command(context.Background(), "-C", deep, "hash-object", "-w", "--stdin")
	write.Stdin = strings.NewReader("long path object")
	if output, err := write.CombinedOutput(); err != nil {
		t.Fatalf("hash-object beyond MAX_PATH: %v %s", err, output)
	}
}
