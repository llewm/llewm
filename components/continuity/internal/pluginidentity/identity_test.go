package pluginidentity

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func fixture(t *testing.T, client string) (string, map[string][]byte) {
	t.Helper()
	files, err := Expected(client)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for name, body := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root, files
}

func TestInspectFindsStaleMissingAndExtraContent(t *testing.T) {
	for _, client := range []string{"codex", "claude-code"} {
		for _, change := range []string{"same-version-skill", "missing-hook", "extra-skill", "symlink", "oversize"} {
			t.Run(client+"/"+change, func(t *testing.T) {
				root, files := fixture(t, client)
				before := Inspect(client, root, files)
				if before.Status != "current" {
					t.Fatal(before)
				}
				switch change {
				case "same-version-skill":
					os.WriteFile(filepath.Join(root, "skills/update/SKILL.md"), []byte("stale"), 0o600)
				case "missing-hook":
					os.Remove(filepath.Join(root, "hooks/hooks.json"))
				case "extra-skill":
					os.WriteFile(filepath.Join(root, "skills/extra.md"), []byte("old removed skill"), 0o600)
				case "symlink":
					os.Symlink(filepath.Join(root, ".mcp.json"), filepath.Join(root, "extra.json"))
				case "oversize":
					os.WriteFile(filepath.Join(root, "extra"), make([]byte, (1<<20)+1), 0o600)
				}
				after := Inspect(client, root, files)
				if after.Status == "current" {
					t.Fatal("accepted changed files", after)
				}
			})
		}
	}
}

func TestInstallerVersionNormalizationAndLaunchCapture(t *testing.T) {
	root, files := fixture(t, "codex")
	var manifest map[string]any
	json.Unmarshal(files[".codex-plugin/plugin.json"], &manifest)
	version := manifest["version"].(string) + "+installer.0123456789ab"
	manifest["version"] = version
	body, _ := json.Marshal(manifest)
	os.WriteFile(filepath.Join(root, ".codex-plugin/plugin.json"), body, 0o600)
	t.Setenv("LLEWM_PLUGIN_CLIENT", "codex")
	t.Setenv("LLEWM_PLUGIN_VERSION", version)
	t.Setenv("LLEWM_PLUGIN_ROOT", root)
	captured := Capture()
	if captured.Status != "current" || captured.Version != version || captured.RootBasis != "client_launch" {
		t.Fatal(captured)
	}
	t.Setenv("LLEWM_PLUGIN_VERSION", "0.1.0")
	if got := Capture(); got.Status != "stale" {
		t.Fatal("ignored stale launch metadata", got)
	}
	t.Setenv("LLEWM_PLUGIN_CLIENT", "")
	if got := Capture(); got.Status != "unknown" {
		t.Fatal(got)
	}
}

func TestCodexCacheRejectsUnsafeVersion(t *testing.T) {
	for _, v := range []string{"", "..", "../other", "a/b", "a\\b", "x\x00"} {
		if CodexCacheRoot(v) != "" {
			t.Fatal(v)
		}
	}
}
