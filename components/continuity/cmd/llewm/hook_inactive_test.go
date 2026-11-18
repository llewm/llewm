package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type unreadHookInput struct{ t *testing.T }

func (i unreadHookInput) Read([]byte) (int, error) {
	i.t.Fatal("inactive hook read stdin")
	return 0, nil
}

func TestInactiveHooksSkipPayloadAndState(t *testing.T) {
	t.Setenv("CONTINUITY_HOME", "")
	dir := t.TempDir()
	repo := filepath.Join(dir, "first")
	for _, path := range []string{filepath.Join(repo, ".git"), filepath.Join(repo, "subdir"), filepath.Join(repo, ".llewm", "orchestration"), filepath.Join(dir, "second", ".git")} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{dir, repo, filepath.Join(repo, "subdir")} {
		for _, client := range []string{"codex", "claude-code"} {
			var out bytes.Buffer
			cmd := newRootCommand("llewm")
			cmd.SetArgs([]string{"--project-root", path, "hook", client})
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetIn(unreadHookInput{t})
			if err := cmd.Execute(); err != nil || out.Len() != 0 {
				t.Fatalf("%s %s: %v %q", path, client, err, out.String())
			}
		}
		if _, err := os.Lstat(filepath.Join(path, ".llewm", "continuity")); !os.IsNotExist(err) {
			t.Fatalf("unexpected state under %s", path)
		}
	}
	// Configured projects with a missing database retain their recovery diagnostic.
	state := filepath.Join(repo, ".llewm", "continuity")
	if err := os.MkdirAll(state, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "config.toml"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := newRootCommand("llewm")
	var out bytes.Buffer
	cmd.SetArgs([]string{"--project-root", repo, "hook", "codex"})
	cmd.SetIn(strings.NewReader(`{"hook_event_name":"SessionStart","session_id":"qual"}`))
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil || !strings.Contains(out.String(), "uninitialized") {
		t.Fatalf("missing configured recovery: %v %s", err, out.String())
	}
}
