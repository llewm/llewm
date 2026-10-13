package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadRebasesAStoreRecordedAtAFormerRoot(t *testing.T) {
	t.Setenv("CONTINUITY_HOME", "")
	root := t.TempDir()
	dir := filepath.Join(root, ".continuity")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := "[project]\nname = \"a\"\nroot = \"/old/a\"\n\n[storage]\ndatabase = \"/old/a/.continuity/continuity.db\"\n"
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Storage.Database != filepath.Join(root, ".continuity", "continuity.db") || cfg.Project.Root != root {
		t.Fatalf("moved config: %#v", cfg)
	}
}

func TestLoadKeepsAnAbsoluteStoreOutsideTheFormerRoot(t *testing.T) {
	t.Setenv("CONTINUITY_HOME", "")
	root := t.TempDir()
	dir := filepath.Join(root, ".continuity")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	custom := "[project]\nroot = \"/old/a\"\n\n[storage]\ndatabase = \"/srv/state/continuity.db\"\n"
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(custom), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Storage.Database != "/srv/state/continuity.db" {
		t.Fatalf("custom store was rebased: %s", cfg.Storage.Database)
	}
}

func TestWriteDefaultRecordsPathsRelativeToTheCheckout(t *testing.T) {
	t.Setenv("CONTINUITY_HOME", "")
	root := t.TempDir()
	path, err := WriteDefault(root)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	if strings.Contains(text, root) || !strings.Contains(text, `database = ".continuity/continuity.db"`) || !strings.Contains(text, `dir = ".continuity/backups"`) {
		t.Fatalf("default config is not relocatable:\n%s", text)
	}
	cfg, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Storage.Database != filepath.Join(root, ".continuity", "continuity.db") {
		t.Fatalf("loaded database = %s", cfg.Storage.Database)
	}
	if cfg.Backups.Dir != filepath.Join(root, ".continuity", "backups") {
		t.Fatalf("loaded backup dir = %s", cfg.Backups.Dir)
	}
	if cfg.Backups.KeepLast != 3 || cfg.Backups.KeepDays != 30 {
		t.Fatalf("loaded backup retention = %#v", cfg.Backups)
	}
}

func TestWriteDefaultKeepsContinuityHomeAbsolute(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CONTINUITY_HOME", home)
	path, err := WriteDefault(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), filepath.Join(home, "continuity.db")) || !strings.Contains(string(content), filepath.Join(home, "backups")) {
		t.Fatalf("CONTINUITY_HOME store was not absolute:\n%s", content)
	}
}

func TestLoadResolvesConfiguredBackupDirectory(t *testing.T) {
	t.Setenv("CONTINUITY_HOME", "")
	root := t.TempDir()
	dir := filepath.Join(root, ".continuity")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	content := "[project]\nroot = \"/old/a\"\n\n[storage]\ndatabase = \".continuity/continuity.db\"\n\n[backups]\ndir = \"var/snapshots\"\n"
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Backups.Dir != filepath.Join(root, "var", "snapshots") {
		t.Fatalf("loaded backup dir = %s", cfg.Backups.Dir)
	}
}

func TestSyncSessionTimeoutDefaultsAndStaysInsideTheHookLimit(t *testing.T) {
	for configured, want := range map[int]time.Duration{0: DefaultSessionTimeout, -1: DefaultSessionTimeout, 3: 3 * time.Second, 10: 10 * time.Second, 60: MaxSessionTimeout} {
		if got := (Sync{SessionTimeoutSeconds: configured}).SessionTimeout(); got != want {
			t.Errorf("session_timeout_seconds = %d: got %s, want %s", configured, got, want)
		}
	}
}
