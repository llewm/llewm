package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Completion must not open project state or require a Git checkout.
func TestCompletionOutsideProject(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	state := filepath.Join(root, "state")
	global := filepath.Join(root, "global")
	t.Setenv("CONTINUITY_HOME", state)
	t.Setenv("CONTINUITY_GLOBAL_HOME", global)
	for _, tc := range []struct {
		name   string
		args   []string
		want   []string
		absent string
	}{
		{"command", []string{"__completeNoDesc", "check"}, []string{"checkpoint"}, ""},
		{"nested", []string{"__completeNoDesc", "checkpoint", "cr"}, []string{"create"}, ""},
		{"flag", []string{"__completeNoDesc", "checkpoint", "create", "--sta"}, []string{"--status"}, ""},
		{"statuses", []string{"__completeNoDesc", "checkpoint", "create", "--status", ""}, []string{"in_progress", "completed", "blocked", "paused", ":4"}, ""},
		{"prefix", []string{"__completeNoDesc", "checkpoint", "create", "--status", "p"}, []string{"paused", ":4"}, "completed"},
		{"no match", []string{"__completeNoDesc", "checkpoint", "create", "--status", "unknown"}, []string{":4"}, "paused"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := runCLI(t, "", tc.args...)
			lines := "\n" + out
			for _, want := range tc.want {
				if !strings.Contains(lines, "\n"+want+"\n") {
					t.Fatalf("missing %q in %q", want, out)
				}
			}
			if tc.absent != "" && strings.Contains(out, tc.absent) {
				t.Fatalf("unexpected %q in %q", tc.absent, out)
			}
		})
	}
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		t.Run(shell, func(t *testing.T) {
			out := runCLI(t, "", "completion", shell)
			if !strings.Contains(out, "llewm") || !strings.Contains(out, "__complete") {
				t.Fatalf("missing completion wiring for %s", shell)
			}
		})
	}
	for _, path := range []string{state, global, filepath.Join(root, ".continuity")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("completion touched %s: %v", path, err)
		}
	}
}

// Completion registers the canonical executable in each supported shell.
func TestCompletionCanonicalRegistration(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	t.Setenv("CONTINUITY_HOME", filepath.Join(root, "state"))
	t.Setenv("CONTINUITY_GLOBAL_HOME", filepath.Join(root, "global"))
	for _, tc := range []struct {
		shell string
		want  []string
		wrong []string
	}{
		{"bash", []string{"__start_llewm", "complete -o default -F __start_llewm llewm"}, []string{"__start_continuity", "__start_cuity"}},
		{"zsh", []string{"#compdef llewm", "_llewm"}, []string{"#compdef continuity", "#compdef cuity"}},
		{"fish", []string{"complete -c llewm -e"}, []string{"complete -c continuity -e", "complete -c cuity -e"}},
		{"powershell", []string{"Register-ArgumentCompleter -CommandName 'llewm'"}, []string{"Register-ArgumentCompleter -CommandName 'continuity'", "Register-ArgumentCompleter -CommandName 'cuity'"}},
	} {
		t.Run(tc.shell, func(t *testing.T) {
			out := runCLIAs(t, "llewm", "", "completion", tc.shell)
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Fatalf("missing %q in llewm %s completion:\n%s", want, tc.shell, out)
				}
			}
			for _, wrong := range tc.wrong {
				if strings.Contains(out, wrong) {
					t.Fatalf("unexpected %q in llewm %s completion (registered under continuity instead)", wrong, tc.shell)
				}
			}
		})
	}
}
