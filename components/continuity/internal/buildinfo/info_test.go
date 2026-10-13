package buildinfo

import (
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
