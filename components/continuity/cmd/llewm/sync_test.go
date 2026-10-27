package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/llewm/llewm/components/continuity/internal/config"
	"github.com/llewm/llewm/components/continuity/internal/memsync"
)

func TestCPUProfileBoundsAndFileSafety(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cpu.pprof")
	called := false
	if err := withCPUProfile(path, 0, func() error { called = true; return nil }); err == nil || called {
		t.Fatalf("invalid duration: called=%v err=%v", called, err)
	}
	if err := withCPUProfile(path, 20*time.Millisecond, func() error {
		time.Sleep(300 * time.Millisecond)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm()&0o077 != 0 || info.Size() == 0 {
		t.Fatalf("profile file: info=%v err=%v", info, err)
	}
	if out, err := exec.Command("go", "tool", "pprof", "-top", path).CombinedOutput(); err != nil {
		t.Fatalf("read profile: %v %s", err, out)
	}
	if err := withCPUProfile(path, time.Second, func() error { return nil }); err == nil {
		t.Fatal("existing profile was overwritten")
	}
}

func TestMemoryCredentialHelperSupportsAskpassDisabledSync(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	execPath := t.TempDir()
	t.Setenv("GIT_EXEC_PATH", execPath)
	clone := t.TempDir()
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	var output bytes.Buffer
	cmd.SetErr(&output)
	access := memsync.Access{Askpass: "/fixture/askpass"}
	const remote = "https://user@example.invalid/memory.git"

	// A prompt may make setup succeed once, but cannot authenticate a later
	// background sync. Refuse it as the only authentication mechanism.
	if helper, err := memoryCredentialHelper(cmd, remote, clone, access, "", false); err == nil || helper != "" || !strings.Contains(err.Error(), "automatic sync disables it") {
		t.Fatalf("askpass-only setup: helper=%q err=%v", helper, err)
	}

	// Discover a fixture helper using real Git's exec-path lookup. It is never
	// executed, and this test cannot access the user's keyring or credentials.
	if err := os.WriteFile(filepath.Join(execPath, "git-credential-libsecret"), nil, 0o700); err != nil {
		t.Fatal(err)
	}
	if helper, err := memoryCredentialHelper(cmd, remote, clone, access, "", false); err != nil || helper != "libsecret" {
		t.Fatalf("keyring with askpass: helper=%q err=%v", helper, err)
	}
	if helper, err := memoryCredentialHelper(cmd, remote, clone, access, "chosen-helper", false); err != nil || helper != "chosen-helper" {
		t.Fatalf("explicit helper: helper=%q err=%v", helper, err)
	}
	if out, err := exec.Command("git", "init", "--quiet", clone).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	if out, err := exec.Command("git", "-C", clone, "config", "credential.helper", "existing-helper").CombinedOutput(); err != nil {
		t.Fatalf("git config: %v %s", err, out)
	}
	if helper, err := memoryCredentialHelper(cmd, remote, clone, access, "", false); err != nil || helper != "" {
		t.Fatalf("preserve existing helper: helper=%q err=%v", helper, err)
	}
}

func TestSyncCommandsAcrossTwoCheckouts(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	remote := filepath.Join(t.TempDir(), "memory.git")
	if output, err := exec.Command("git", "init", "--quiet", "--bare", remote).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v %s", err, output)
	}

	// Machine A: new history.
	testProject(t)
	runCLI(t, "", "init")
	if status := runCLI(t, "", "sync", "status"); !strings.Contains(status, "not configured") {
		t.Fatalf("unconfigured status: %s", status)
	}
	if err, _ := runCLIError("sync"); err == nil || !strings.Contains(err.Error(), "sync init --remote") {
		t.Fatalf("sync without a remote: %v", err)
	}
	// An empty memory repository: sync init starts a new history.
	if out := runCLI(t, "", "sync", "init", "--remote", remote); !strings.Contains(out, "Lineage: lineage:") {
		t.Fatalf("sync init did not create an identity: %s", out)
	}
	var status struct {
		Identity struct {
			LineageID string `json:"lineage_id"`
		} `json:"identity"`
	}
	if err := json.Unmarshal([]byte(runCLI(t, "", "sync", "status", "--output", "json")), &status); err != nil || status.Identity.LineageID == "" {
		t.Fatalf("sync status identity: %+v %v", status, err)
	}
	identity := status.Identity
	decisionFile := filepath.Join(t.TempDir(), "decision.json")
	if err := os.WriteFile(decisionFile, []byte(`{"id":"sync-history","title":"Sync history","rationale":"Machines share memory","disposition":"accepted","actor":"owner","actor_type":"human","authority":"caller_asserted"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	runCLI(t, "", "decision", "record", "--file", decisionFile)
	profile := filepath.Join(t.TempDir(), "sync.pprof")
	if out := runCLI(t, "", "sync", "--cpu-profile", profile); !strings.Contains(out, "Sync synced: exported") {
		t.Fatalf("machine A sync: %s", out)
	}
	if info, err := os.Stat(profile); err != nil || info.Size() == 0 {
		t.Fatalf("sync profile: info=%v err=%v", info, err)
	}
	if status := runCLI(t, "", "sync", "status"); !strings.Contains(status, "Sync: last synced just now") {
		t.Fatalf("machine A status: %s", status)
	}

	// Machine B: a separate checkout and store must adopt the lineage explicitly.
	testProject(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	runCLI(t, "", "init")
	err, _ := runCLIError("sync", "init", "--remote", remote)
	if err == nil || !strings.Contains(err.Error(), "--lineage LINEAGE") || !strings.Contains(err.Error(), identity.LineageID) {
		t.Fatalf("sync init without identity: %v", err)
	}
	runCLI(t, "", "sync", "init", "--remote", remote, "--lineage", identity.LineageID)
	var result struct {
		Outcome         string `json:"outcome"`
		ImportedRecords int    `json:"imported_records"`
	}
	if err := json.Unmarshal([]byte(runCLI(t, "", "sync", "--output", "json")), &result); err != nil || result.Outcome != "synced" || result.ImportedRecords == 0 {
		t.Fatalf("machine B sync: %#v %v", result, err)
	}
	if shown := runCLI(t, "", "history", "show", "decision:sync-history"); !strings.Contains(shown, "Sync history") {
		t.Fatalf("machine B does not see machine A's decision:\n%s", shown)
	}
}

func TestAutomaticSyncTriggers(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	var started []string
	previous := startBackgroundSync
	startBackgroundSync = func(executable, root string) error {
		started = append(started, root)
		return nil
	}
	t.Cleanup(func() { startBackgroundSync = previous })
	remote := filepath.Join(t.TempDir(), "memory.git")
	if output, err := exec.Command("git", "init", "--quiet", "--bare", remote).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v %s", err, output)
	}

	// Machine A: a checkpoint starts one background sync.
	rootA := testProject(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	runCLI(t, "", "init")
	var identity struct {
		LineageID string `json:"lineage_id"`
	}
	if err := json.Unmarshal([]byte(runCLI(t, "", "sync", "init")), &identity); err != nil {
		t.Fatal(err)
	}
	runCLI(t, "", "checkpoint", "create", "--task-id", "before-sync", "--status", "paused", "--task", "Before sync is configured", "--next", "Configure sync")
	if len(started) != 0 {
		t.Fatalf("background sync without a remote: %v", started)
	}
	runCLI(t, "", "sync", "init", "--remote", remote)
	runCLI(t, "", "checkpoint", "create", "--task", "Wire the daemon", "--task-id", "wire-daemon", "--next", "Connect the API")
	if len(started) != 1 || started[0] != rootA {
		t.Fatalf("background syncs after a checkpoint = %v", started)
	}
	runCLI(t, "", "sync")
	if status := runCLI(t, "", "work"); !strings.Contains(status, "Sync: last synced just now") {
		t.Fatalf("status without a sync line:\n%s", status)
	}

	// Machine B: session start syncs before building the bootstrap.
	testProject(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	runCLI(t, "", "init")
	runCLI(t, "", "sync", "init", "--lineage", identity.LineageID)
	runCLI(t, "", "sync", "init", "--remote", remote)
	hook := runCLI(t, `{"session_id":"session-b","hook_event_name":"SessionStart","source":"startup"}`, "hook", "claude-code")
	if !strings.Contains(hook, "Wire the daemon") || !strings.Contains(hook, "Sync: last synced just now") {
		t.Fatalf("session start did not sync first:\n%s", hook)
	}
	// A session start with records to push leaves the push to a background sync.
	runCLI(t, "", "checkpoint", "create", "--task-id", "machine-b", "--task", "Machine B work", "--next", "Push it")
	started = nil
	hook = runCLI(t, `{"session_id":"session-b2","hook_event_name":"SessionStart","source":"startup"}`, "hook", "claude-code")
	if len(started) != 1 || !strings.Contains(hook, "fetched and imported; a background sync pushes") {
		t.Fatalf("session start with a pending push: started %v\n%s", started, hook)
	}

	// Manual projects never sync on their own.
	configPath := filepath.Join(os.Getenv("CONTINUITY_HOME"), "config.toml")
	body, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(strings.Replace(string(body), "[sync]\n", "[sync]\n  manual = true\n", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	started = nil
	runCLI(t, "", "checkpoint", "create", "--task-id", "manual-now", "--task", "Manual now", "--next", "Sync by hand")
	if len(started) != 0 {
		t.Fatalf("manual project started a background sync: %v", started)
	}
}

func TestSyncSettingsNameEveryDeviation(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	clone := filepath.Join(t.TempDir(), "clone")
	if output, err := exec.Command("git", "init", "--quiet", clone).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, output)
	}
	ctx := context.Background()
	if got := syncSettings(ctx, config.Sync{Remote: "r"}, clone); len(got) != 0 {
		t.Fatalf("defaults reported settings: %v", got)
	}
	for _, value := range []string{"", memsync.PlaintextHelper(clone)} {
		if output, err := exec.Command("git", "-C", clone, "config", "--add", "credential.helper", value).CombinedOutput(); err != nil {
			t.Fatalf("git config: %v %s", err, output)
		}
	}
	got := strings.Join(syncSettings(ctx, config.Sync{Remote: "r", Manual: true, SessionTimeoutSeconds: 30, SSHKey: "/k", Askpass: "/a"}, clone), "; ")
	want := "automatic sync off (manual = true); session sync limit 10s; SSH key /k; askpass /a; PLAINTEXT credentials in " + memsync.PlaintextCredentialFile(clone)
	if got != want {
		t.Fatalf("settings:\n got %s\nwant %s", got, want)
	}
}
