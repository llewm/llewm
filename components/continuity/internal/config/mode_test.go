package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestModePreservesConfigurationAndRejectsInvalidValues(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CONTINUITY_HOME", "")
	path, err := WriteDefault(root)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("\n[future]\nkeep = 'yes'\n")
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SetMode(root, "audit-only"); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(root)
	if err != nil || cfg.Mode != "audit-only" || cfg.Bootstrap.MaxBytes != 16384 {
		t.Fatalf("config: %+v %v", cfg, err)
	}
	before, _ := os.ReadFile(path)
	if !strings.Contains(string(before), "[future]") || !strings.Contains(string(before), "yes") {
		t.Fatal("unknown settings lost")
	}
	if _, err := SetMode(root, "typo"); err == nil {
		t.Fatal("invalid mode accepted")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("invalid mode wrote config")
	}
	if err := os.WriteFile(filepath.Join(StateDir(root), FileName), []byte("mode = 'typo'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root); err == nil {
		t.Fatal("invalid persisted mode accepted")
	}
}
