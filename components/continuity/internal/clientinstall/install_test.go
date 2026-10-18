package clientinstall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/llewm/llewm/components/continuity/plugins"
)

type fakeClient struct {
	skipCache                   bool
	client, root, version, fail string
	cacheRoot                   string
	registered, enabled         bool
	writes                      [][]string
}

func (f *fakeClient) run(_ context.Context, args ...string) ([]byte, error) {
	key := strings.Join(args, " ")
	if key == "--version" {
		return []byte("fixture 1.0"), nil
	}
	if strings.HasSuffix(key, "--help") {
		return []byte("--json --scope"), nil
	}
	if key == "plugin marketplace list --json" {
		items := []marketplace{}
		if f.registered {
			items = append(items, marketplace{Name: "llewm", Root: f.root, Path: f.root})
		}
		if f.client == "codex" {
			return json.Marshal(map[string]any{"marketplaces": items})
		}
		return json.Marshal(items)
	}
	if key == "plugin list --json" {
		items := []installedPlugin{}
		if f.version != "" {
			items = append(items, installedPlugin{InstallPath: f.cacheRoot, ID: Selector, PluginID: Selector, Scope: "user", Version: f.version, Enabled: f.enabled, Installed: true})
		}
		if f.client == "codex" {
			return json.Marshal(map[string]any{"installed": items})
		}
		return json.Marshal(items)
	}
	f.writes = append(f.writes, append([]string{}, args...))
	if f.fail != "" && strings.Contains(key, f.fail) {
		return nil, errors.New("fixture client failure")
	}
	if strings.HasPrefix(key, "plugin marketplace add ") {
		f.registered = true
		f.root = args[3]
	}
	if strings.HasPrefix(key, "plugin install ") && f.version != "" {
		return []byte(`{"outcome":"ok","message":"already installed"}`), nil
	}
	if strings.HasPrefix(key, "plugin add ") || strings.HasPrefix(key, "plugin install ") || strings.HasPrefix(key, "plugin update ") {
		p, err := Bundled()
		if err != nil {
			return nil, err
		}
		if err := f.materialize(p); err != nil {
			return nil, err
		}
		f.version = p.Version
		if !strings.HasPrefix(key, "plugin update ") {
			f.enabled = true
		}
	}
	if strings.HasPrefix(key, "plugin enable ") {
		if f.enabled {
			return nil, errors.New("already enabled")
		}
		f.enabled = true
	}
	return []byte(`{}`), nil
}

func (f *fakeClient) materialize(p Package) error {
	if f.skipCache {
		return nil
	}
	f.cacheRoot = filepath.Join(filepath.Dir(f.root), "client-cache")
	if err := os.RemoveAll(f.cacheRoot); err != nil {
		return err
	}
	prefix := "plugins/" + f.client + "/"
	for name, body := range p.Files {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		path := filepath.Join(f.cacheRoot, strings.TrimPrefix(name, prefix))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(path, body, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func TestClientInstallPreviewApplyAndRepeat(t *testing.T) {
	for _, client := range []string{"codex", "claude-code"} {
		t.Run(client, func(t *testing.T) {
			p, err := Bundled()
			if err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(t.TempDir(), "installer", "marketplace")
			f := &fakeClient{client: client}
			s := Service{Client: client, Root: root, Package: p, Run: f.run}
			preview, err := s.Inspect(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(preview.Steps) < 2 || len(f.writes) != 0 {
				t.Fatalf("preview mutated client: %#v", preview)
			}
			if _, err := os.Stat(filepath.Dir(root)); !os.IsNotExist(err) {
				t.Fatalf("preview wrote directory: %v", err)
			}
			r, err := s.Apply(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if !r.PluginInstalled || r.LiveVerified || r.Permissions != "needs_review" || r.HookTrust != "needs_client_review" {
				t.Fatalf("misleading receipt: %#v", r)
			}
			writes := len(f.writes)
			r, err = s.Apply(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(f.writes) != writes || len(r.Steps) != 0 || r.Backup != "" {
				t.Fatalf("repeat did not converge: %#v", r)
			}
		})
	}
}

func TestClientInstallConflictAndPartialRecovery(t *testing.T) {
	p, _ := Bundled()
	root := filepath.Join(t.TempDir(), "installer", "marketplace")
	f := &fakeClient{client: "codex", registered: true, root: "/another/marketplace"}
	s := Service{Client: "codex", Root: root, Package: p, Run: f.run}
	if _, err := s.Apply(context.Background()); err == nil || len(f.writes) > 0 {
		t.Fatal("overwrote foreign marketplace")
	}
	f.registered = false
	f.fail = "plugin add"
	r, err := s.Apply(context.Background())
	if err == nil || r.State != "partial" || r.CompletedSteps != 1 || r.PluginInstalled {
		t.Fatalf("failure receipt: %#v %v", r, err)
	}
	if _, err := verifyOwned(root); err != nil {
		t.Fatal("partial install lost package", err)
	}
	f.fail = ""
	r, err = s.Apply(context.Background())
	if err != nil || !r.PluginInstalled {
		t.Fatal(r, err)
	}
}

func TestClientInstallRejectsUnknownClientProtocol(t *testing.T) {
	p, _ := Bundled()
	s := Service{Client: "codex", Root: filepath.Join(t.TempDir(), "marketplace"), Package: p, Run: func(_ context.Context, args ...string) ([]byte, error) { return []byte(`{}`), nil }}
	if _, err := s.Inspect(context.Background()); err == nil {
		t.Fatal("accepted unknown protocol")
	}
}

func TestClientInstallMissingClientWritesNothing(t *testing.T) {
	p, _ := Bundled()
	root := filepath.Join(t.TempDir(), "installer", "marketplace")
	s := Service{Client: "codex", Root: root, Package: p, Run: func(context.Context, ...string) ([]byte, error) { return nil, errors.New("client unavailable") }}
	if _, err := s.Apply(context.Background()); err == nil {
		t.Fatal("accepted missing client")
	}
	if _, err := os.Stat(filepath.Dir(root)); !os.IsNotExist(err) {
		t.Fatal("wrote before client check")
	}
}

func TestBundledManifestsAndLicenses(t *testing.T) {
	p, err := Bundled()
	if err != nil {
		t.Fatal(err)
	}
	for _, client := range []string{"codex", "claude-code"} {
		prefix := "plugins/" + client + "/"
		if !strings.Contains(string(p.Files[prefix+"LICENSE"]), "MIT License") {
			t.Fatal("plugin license absent")
		}
		if !json.Valid(p.Files[prefix+".mcp.json"]) || !json.Valid(p.Files[prefix+"hooks/hooks.json"]) {
			t.Fatal("invalid bundled client config")
		}
	}
}

func TestPackageOwnershipAndBackup(t *testing.T) {
	p, _ := Bundled()
	root := filepath.Join(t.TempDir(), "marketplace")
	if _, err := p.Stage(root); err != nil {
		t.Fatal(err)
	}
	oldMarker, _ := os.ReadFile(filepath.Join(root, ".llewm-installer.json"))
	p.Digest = strings.Repeat("a", 64)
	p.Files["plugins/codex/skills/checkpoint/SKILL.md"] = []byte("new revision")
	backup, err := p.Stage(root)
	if err != nil || backup == "" {
		t.Fatal(backup, err)
	}
	saved, _ := os.ReadFile(filepath.Join(backup, ".llewm-installer.json"))
	if !reflect.DeepEqual(oldMarker, saved) {
		t.Fatal("backup changed")
	}
	if err := os.WriteFile(filepath.Join(root, "user.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Stage(root); err == nil {
		t.Fatal("overwrote extra user file")
	}
}

func TestPackageRejectsSymlinkAndUnmanagedDirectory(t *testing.T) {
	p, _ := Bundled()
	base := t.TempDir()
	root := filepath.Join(base, "marketplace")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Stage(root); err == nil {
		t.Fatal("overwrote unmanaged directory")
	}
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), root); err != nil {
		t.Skip(err)
	}
	if _, err := p.Stage(root); err == nil {
		t.Fatal("followed symlink")
	}
}

func TestUpgradeAndEnableTransitions(t *testing.T) {
	for _, client := range []string{"codex", "claude-code"} {
		for _, stale := range []bool{false, true} {
			for _, enabled := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/stale=%t/enabled=%t", client, stale, enabled), func(t *testing.T) {
					p, _ := Bundled()
					root := filepath.Join(t.TempDir(), "marketplace")
					version := p.Version
					if stale {
						version = "0.1.0+installer.old"
					}
					f := &fakeClient{client: client, root: root, version: version, registered: true, enabled: enabled}
					if err := f.materialize(p); err != nil {
						t.Fatal(err)
					}
					r, err := (Service{Client: client, Root: root, Package: p, Run: f.run}).Apply(context.Background())
					if err != nil || !r.PluginInstalled {
						t.Fatalf("%#v %v", r, err)
					}
					if client == "claude-code" && stale && f.writes[0][1] != "update" {
						t.Fatal(f.writes)
					}
					for _, write := range f.writes {
						if client == "claude-code" && write[1] == "install" {
							t.Fatal("reinstalled existing plugin", write)
						}
					}
				})
			}
		}
	}
}

func TestEveryPackageInputChangesCacheIdentity(t *testing.T) {
	original, err := Bundled()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"plugins/codex/skills/update/SKILL.md", "plugins/claude-code/hooks/hooks.json", ".agents/plugins/marketplace.json", ".claude-plugin/marketplace.json"} {
		t.Run(name, func(t *testing.T) {
			changed, err := bundled(plugins.Assets, func(files map[string][]byte) { files[name] = append(append([]byte{}, files[name]...), '\n') })
			if err != nil {
				t.Fatal(err)
			}
			if changed.Digest == original.Digest || changed.Version == original.Version {
				t.Fatal("input omitted from cache identity")
			}
			root := filepath.Join(t.TempDir(), "marketplace")
			if _, err := original.Stage(root); err != nil {
				t.Fatal(err)
			}
			backup, err := changed.Stage(root)
			if err != nil || backup == "" {
				t.Fatal("changed package skipped", err)
			}
			got, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
			if !reflect.DeepEqual(got, changed.Files[name]) {
				t.Fatal("new input not staged")
			}
		})
	}
}

func TestCorrectVersionWithStaleCacheDoesNotPass(t *testing.T) {
	p, _ := Bundled()
	root := filepath.Join(t.TempDir(), "marketplace")
	f := &fakeClient{client: "claude-code"}
	s := Service{Client: f.client, Root: root, Package: p, Run: f.run}
	if _, err := s.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.cacheRoot, "skills/update/SKILL.md"), []byte("stale bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.skipCache = true
	receipt, err := s.Apply(context.Background())
	if err == nil || receipt.State != "partial" || receipt.PluginCache.Status != "stale" || receipt.LiveVerified {
		t.Fatal(receipt, err)
	}
	f.skipCache = false
	if receipt, err := s.Apply(context.Background()); err != nil || receipt.PluginCache.Status != "current" {
		t.Fatal(receipt, err)
	}
}
