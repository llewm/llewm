package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLegacyGuidanceRequiresExplicitPreservation(t *testing.T) {
	t.Setenv("CONTINUITY_GLOBAL_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())
	base, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(base, "llmem", "continuity")
	if err := os.MkdirAll(old, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := GlobalDir(); err == nil || !strings.Contains(err.Error(), "existing global guidance") {
		t.Fatalf("silently omitted existing guidance: %v", err)
	}
	t.Setenv("CONTINUITY_GLOBAL_HOME", old)
	if got, err := GlobalDir(); err != nil || got != old {
		t.Fatalf("explicit retained directory: %q %v", got, err)
	}
}

func TestLegacyCloneCannotBecomeAnEmptyNewClone(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_DATA_HOME", base)
	remote := "https://example.invalid/owner/memory.git"
	next, err := DefaultMemoryClone(remote)
	if err != nil {
		t.Fatal(err)
	}
	old := strings.Replace(next, filepath.Join(base, "llewm"), filepath.Join(base, "llmem"), 1)
	if err := os.MkdirAll(old, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := DefaultMemoryClone(remote); err == nil || !strings.Contains(err.Error(), "existing memory clone") {
		t.Fatalf("silently selected an empty clone: %v", err)
	}
	if got, err := (Sync{Remote: remote, Clone: old}).MemoryClone(); err != nil || got != old {
		t.Fatalf("explicit retained clone: %q %v", got, err)
	}
}
