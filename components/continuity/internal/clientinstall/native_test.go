package clientinstall

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/llewm/llewm/components/continuity/plugins"
)

// Opt-in, local-only native protocol regression. Dedicated Claude configuration
// and cwd isolate the user's registrations. No session/model is started.
func TestNativeClaudeUpgrade(t *testing.T) {
	if os.Getenv("LLEWM_TEST_CLAUDE") != "1" {
		t.Skip("set LLEWM_TEST_CLAUDE=1 to exercise installed Claude CLI")
	}
	executable, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	t.Chdir(base)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(base, "config"))
	t.Setenv("CLAUDE_CODE_PLUGIN_CACHE_DIR", filepath.Join(base, "config", "plugins"))
	t.Setenv("CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", "1")
	old, err := bundled(plugins.Assets, func(files map[string][]byte) {
		files["plugins/claude-code/skills/update/SKILL.md"] = []byte("---\nname: update\ndescription: Native upgrade regression fixture.\n---\nOld fixture content.\n")
	})
	if err != nil {
		t.Fatal(err)
	}
	s := Service{Client: "claude-code", Root: filepath.Join(base, "marketplace"), Package: old, Run: NativeRunner(executable)}
	first, err := s.Apply(context.Background())
	if err != nil {
		t.Fatalf("first install: %#v %v", first, err)
	}
	if _, err := s.Run(context.Background(), "plugin", "disable", Selector, "--scope", "user", "--json"); err != nil {
		t.Fatal(err)
	}
	s.Package, err = Bundled()
	if err != nil {
		t.Fatal(err)
	}
	updated, err := s.Apply(context.Background())
	if err != nil {
		t.Fatalf("upgrade: %#v %v", updated, err)
	}
	if updated.PluginCache.Status != "current" || updated.PluginVersion == first.PluginVersion || updated.Backup == "" {
		t.Fatal(updated)
	}
	if _, err := s.Run(context.Background(), "plugin", "disable", Selector, "--scope", "user", "--json"); err != nil {
		t.Fatal(err)
	}
	enabled, err := s.Apply(context.Background())
	if err != nil || len(enabled.Steps) != 1 || enabled.Steps[0][1] != "enable" {
		t.Fatal(enabled, err)
	}
	repeated, err := s.Apply(context.Background())
	if err != nil || len(repeated.Steps) != 0 {
		t.Fatal(repeated, err)
	}
	t.Logf("native Claude %s: %s -> %s; cache verified; repeat has zero mutations", updated.ClientVersion, first.PluginVersion, updated.PluginVersion)
}
