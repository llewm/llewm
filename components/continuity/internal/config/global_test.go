package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGlobalDir(t *testing.T) {
	t.Setenv("CONTINUITY_GLOBAL_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())
	t.Setenv("CONTINUITY_HOME", t.TempDir())
	base, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	got, err := GlobalDir()
	if err != nil || got != filepath.Join(base, "llewm", "continuity") {
		t.Fatalf("default global directory: %s, %v", got, err)
	}
	t.Setenv("CONTINUITY_GLOBAL_HOME", "relative")
	if _, err := GlobalDir(); err == nil {
		t.Fatal("accepted relative override")
	}
	dir := t.TempDir()
	t.Setenv("CONTINUITY_GLOBAL_HOME", dir)
	if got, err := GlobalDir(); err != nil || got != dir {
		t.Fatalf("override: %s, %v", got, err)
	}
}
