//go:build !windows

package memsync

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestAskpassFallbackPrecedence asks real Git for a credential and checks that
// the fallback answers only when nothing the user configured does.
func TestAskpassFallbackPrecedence(t *testing.T) {
	dir := t.TempDir()
	prompt := func(name string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\necho "+name+"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		return path
	}
	fallback, user, ambient := prompt("fallback"), prompt("user"), prompt("ambient")
	helper := filepath.Join(dir, "helper")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\ntest \"$1\" = get && echo username=u && echo password=helper\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, name := range []string{"GIT_ASKPASS", "SSH_ASKPASS"} {
		t.Setenv(name, "")
		os.Unsetenv(name)
	}

	password := func(g git, config ...string) string {
		t.Helper()
		args := append([]string{"-C", dir}, g.config()...)
		args = append(args, config...)
		command := exec.Command("git", append(args, "credential", "fill")...)
		command.Env = g.env()
		command.Stdin = strings.NewReader("protocol=https\nhost=example.invalid\nusername=u\n\n")
		out, err := command.Output()
		if err != nil {
			return "error"
		}
		for _, line := range strings.Split(string(out), "\n") {
			if value, ok := strings.CutPrefix(line, "password="); ok {
				return value
			}
		}
		return "none"
	}

	if got := password(git{}); got != "error" {
		t.Fatalf("without any prompt program: %q, want error", got)
	}
	t.Setenv("SSH_ASKPASS", ambient)
	if got := password(git{Askpass: fallback}); got != "fallback" {
		t.Fatalf("fallback over ambient SSH_ASKPASS: %q", got)
	}
	if got := password(git{Askpass: fallback}, "-c", "core.askPass="+user); got != "user" {
		t.Fatalf("core.askPass over fallback: %q", got)
	}
	if got := password(git{Askpass: fallback}, "-c", "credential.helper=!"+helper); got != "helper" {
		t.Fatalf("credential helper over fallback: %q", got)
	}
	t.Setenv("GIT_ASKPASS", user)
	if got := password(git{Askpass: fallback}); got != "user" {
		t.Fatalf("GIT_ASKPASS over fallback: %q", got)
	}
	// Automatic sync must not invoke any prompt source, even if all are
	// configured or a desktop display happens to be available.
	for _, interactive := range []bool{false, true} {
		g := git{Dir: dir, Askpass: fallback, NoPrompt: true, Interactive: interactive}
		if got := password(g, "-c", "core.askPass="+user); got != "error" {
			t.Fatalf("automatic sync prompted (interactive=%v): %q", interactive, got)
		}
		if got := password(g, "-c", "credential.helper=!"+helper); got != "helper" {
			t.Fatalf("automatic sync lost cached credentials: %q", got)
		}
	}
	err := (git{Dir: dir, Askpass: fallback, NoPrompt: true}).runInput(context.Background(), "protocol=https\nhost=example.invalid\nusername=u\n\n", "credential", "fill")
	if err == nil || !strings.Contains(err.Error(), "authentication unavailable: automatic sync cannot prompt") {
		t.Fatalf("missing actionable authentication failure: %v", err)
	}

}

func TestSSHKeyCommand(t *testing.T) {
	t.Setenv("GIT_SSH_COMMAND", "ssh -o Ambient=yes")
	key := "/keys/it's a key"
	var command string
	for _, entry := range (git{SSHKey: key}).env() {
		if value, ok := strings.CutPrefix(entry, "GIT_SSH_COMMAND="); ok {
			command = value
		}
	}
	if !strings.HasPrefix(command, "ssh -i ") || !strings.HasSuffix(command, " -o IdentitiesOnly=yes -o BatchMode=yes -o ConnectTimeout=5 -o ServerAliveInterval=5 -o ServerAliveCountMax=3") {
		t.Fatalf("GIT_SSH_COMMAND = %q", command)
	}
	quoted := strings.TrimSuffix(strings.TrimPrefix(command, "ssh -i "), " -o IdentitiesOnly=yes -o BatchMode=yes -o ConnectTimeout=5 -o ServerAliveInterval=5 -o ServerAliveCountMax=3")
	out, err := exec.Command("sh", "-c", "printf %s "+quoted).Output()
	if err != nil || string(out) != key {
		t.Fatalf("shell reads the key path as %q (%v), want %q", out, err, key)
	}
}

// TestCloneHelperReplacesUserHelpers configures a helper in the memory clone and
// checks real Git uses it there, over a helper in the user's global config, and
// that nothing outside the clone changes.
func TestCloneHelperReplacesUserHelpers(t *testing.T) {
	remote := bareRemote(t)
	dir := t.TempDir()
	helper := filepath.Join(dir, "helper")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\ntest \"$1\" = get && echo username=u && echo password=global\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(dir, "gitconfig")
	globalConfig := []byte("[credential]\n\thelper = !" + helper + "\n")
	if err := os.WriteFile(global, globalConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	clone := filepath.Join(dir, "memory it's")
	store := PlaintextCredentialFile(clone)
	if err := Init(context.Background(), remote, clone, Access{}, Setup{Helper: PlaintextHelper(clone)}); err != nil {
		t.Fatal(err)
	}
	if got := Helper(context.Background(), clone); got != PlaintextHelper(clone) {
		t.Fatalf("clone helper = %q", got)
	}
	// What Git's store helper writes after a successful prompt.
	if err := os.WriteFile(store, []byte("https://u:stored@example.invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	password := func(repo string) string {
		t.Helper()
		command := exec.Command("git", "-C", repo, "credential", "fill")
		command.Env = append((git{}).env(), "GIT_ASKPASS=false")
		command.Stdin = strings.NewReader("protocol=https\nhost=example.invalid\nusername=u\n\n")
		out, err := command.Output()
		if err != nil {
			return "error"
		}
		for _, line := range strings.Split(string(out), "\n") {
			if value, ok := strings.CutPrefix(line, "password="); ok {
				return value
			}
		}
		return "none"
	}
	if got := password(clone); got != "stored" {
		t.Fatalf("in the clone: %q, want the clone helper's", got)
	}
	if got := password(dir); got != "global" {
		t.Fatalf("outside the clone: %q, want the user's helper", got)
	}
	if after, _ := os.ReadFile(global); string(after) != string(globalConfig) {
		t.Fatalf("global config changed: %s", after)
	}

	// Re-init replaces the clone's helper rather than adding another.
	if err := Init(context.Background(), remote, clone, Access{}, Setup{Helper: "cache"}); err != nil {
		t.Fatal(err)
	}
	values, _ := (git{Dir: clone}).run(context.Background(), "config", "--local", "--null", "--get-all", "credential.helper")
	if values != "\x00cache\x00" {
		t.Fatalf("clone helpers after re-init: %q", values)
	}
	if err := CheckAccess(context.Background(), clone, Access{}); err != nil {
		t.Fatalf("local remote: %v", err)
	}
}

func TestInteractiveOnlyDuringSetup(t *testing.T) {
	has := func(env []string, entry string) bool {
		for _, value := range env {
			if strings.HasPrefix(value, entry) {
				return true
			}
		}
		return false
	}
	quiet := (git{SSHKey: "/k"}).env()
	if !has(quiet, "GIT_TERMINAL_PROMPT=0") || !has(quiet, "GIT_SSH_COMMAND=ssh -i '/k' -o IdentitiesOnly=yes -o BatchMode=yes -o ConnectTimeout=5 -o ServerAliveInterval=5 -o ServerAliveCountMax=3") {
		t.Fatalf("sync env allows prompts: %v", quiet)
	}
	t.Setenv("GIT_TERMINAL_PROMPT", "")
	os.Unsetenv("GIT_TERMINAL_PROMPT")
	loud := (git{SSHKey: "/k", Interactive: true}).env()
	if has(loud, "GIT_TERMINAL_PROMPT=0") || !has(loud, "GIT_SSH_COMMAND=ssh -i '/k' -o IdentitiesOnly=yes") || has(loud, "GIT_SSH_COMMAND=ssh -i '/k' -o IdentitiesOnly=yes -o BatchMode") {
		t.Fatalf("setup env forbids prompts: %v", loud)
	}
	for remote, want := range map[string]bool{"https://h/r": true, "HTTP://h/r": true, "ssh://git@h/r": false, "git@h:o/r": false, "/srv/r.git": false} {
		if HTTPRemote(remote) != want {
			t.Errorf("HTTPRemote(%q) != %v", remote, want)
		}
	}
}

// TestTokenGoesToTheHelper hands a token to the configured helper and checks the
// helper, not Continuity, stored it.
func TestTokenGoesToTheHelper(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	clone := filepath.Join(dir, "clone")
	runner := git{Dir: dir, Helper: PlaintextHelper(clone)}
	ctx := context.Background()
	for _, bad := range []struct{ remote, token string }{
		{"https://example.invalid/o/r", "t"},
		{"ssh://u@example.invalid/o/r", "t"},
		{"https://u:p@example.invalid/o/r", "t"},
		{"https://u@example.invalid/o/r", " "},
		{"https://u@example.invalid/o/r", "a\nb"},
	} {
		if err := approveToken(ctx, runner, bad.remote, bad.token); err == nil {
			t.Errorf("approveToken(%q, %q) succeeded", bad.remote, bad.token)
		}
	}
	if _, err := os.Stat(PlaintextCredentialFile(clone)); !os.IsNotExist(err) {
		t.Fatalf("refused tokens reached the helper: %v", err)
	}
	if err := approveToken(ctx, runner, "https://u@example.invalid:8443/o/r", "to:k/en\n"); err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(PlaintextCredentialFile(clone))
	if err != nil || string(stored) != "https://u:to%3ak%2fen@example.invalid%3a8443\n" {
		t.Fatalf("helper stored %q (%v)", stored, err)
	}
}
