package clientinstall

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type fakeClient struct {
	client, root, version, fail string
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
			items = append(items, installedPlugin{ID: Selector, PluginID: Selector, Scope: "user", Version: f.version, Enabled: f.enabled, Installed: true})
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
	if strings.HasPrefix(key, "plugin add ") || strings.HasPrefix(key, "plugin install ") {
		p, err := Bundled()
		if err != nil {
			return nil, err
		}
		f.version = p.Version
		f.enabled = true
	}
	if strings.HasPrefix(key, "plugin enable ") {
		f.enabled = true
	}
	return []byte(`{}`), nil
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
			if client == "claude-code" && !reflect.DeepEqual(f.writes[2], []string{"plugin", "enable", Selector, "--scope", "user", "--json"}) {
				t.Fatalf("wrong enable ordering: %#v", f.writes)
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
