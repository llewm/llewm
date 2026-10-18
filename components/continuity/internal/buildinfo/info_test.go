package buildinfo

import (
	"github.com/llewm/llewm/components/continuity/internal/pluginidentity"
	"os"
	"path/filepath"
	"testing"
)

func TestCurrentHasOperationalIdentity(t *testing.T) {
	info := Current()
	if info.ApplicationVersion == "" || info.GitCommit == "" || info.GoVersion == "" || info.MCPAPIVersion == 0 || info.WriterAPIVersion == 0 || info.Executable == "" {
		t.Fatalf("incomplete build identity: %#v", info)
	}
}

func TestRuntimeHealthDetectsInstalledReplacement(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	runtimeState := RuntimeState{Build: Info{Executable: executable, GitCommit: "runtime"}, ExecutableSHA256: "old-hash"}
	health := runtimeState.CheckInstalled()
	if health.Status != "stale" || !health.RestartRequired || len(health.Reasons) == 0 {
		t.Fatalf("replacement was not reported stale: %#v", health)
	}
}

func TestRuntimeHealthReportsUnreadableInstallUnknown(t *testing.T) {
	runtimeState := RuntimeState{Build: Info{Executable: filepath.Join(t.TempDir(), "missing"), GitCommit: "runtime"}, ExecutableSHA256: "old-hash"}
	health := runtimeState.CheckInstalled()
	if health.Status != "unknown" || health.RestartRequired || health.Installed.Error == "" {
		t.Fatalf("unreadable install was not reported unknown: %#v", health)
	}
}

func TestRuntimeRetainsStartupPluginObservation(t *testing.T) {
	expected, err := pluginidentity.Expected("codex")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for name, body := range expected {
		path := filepath.Join(root, name)
		os.MkdirAll(filepath.Dir(path), 0o700)
		os.WriteFile(path, body, 0o600)
	}
	state := CaptureRuntime()
	state.Plugin = pluginidentity.Inspect("codex", root, expected)
	before := state.CheckInstalled()
	if before.PluginStatus != "current" {
		t.Fatal(before)
	}
	os.WriteFile(filepath.Join(root, "skills/update/SKILL.md"), []byte("changed after start"), 0o600)
	after := state.CheckInstalled()
	if after.PluginStatus != "stale" || !after.RestartRequired || after.Runtime.Plugin.SHA256 != before.Runtime.Plugin.SHA256 {
		t.Fatal(after)
	}
}
