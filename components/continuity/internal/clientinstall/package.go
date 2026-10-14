package clientinstall

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/llewm/llewm/components/continuity/plugins"
)

type Package struct {
	Digest  string
	Version string
	Files   map[string][]byte
}

// Bundled assembles an inert, content-addressed package, not an executable
// recipe. The cache version changes whenever a bundled skill or hook changes.
func Bundled() (Package, error) {
	p := Package{Files: map[string][]byte{}}
	err := fs.WalkDir(plugins.Assets, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := plugins.Assets.ReadFile(path)
		if err != nil {
			return err
		}
		if path == "LICENSE" {
			p.Files["plugins/codex/LICENSE"] = data
			p.Files["plugins/claude-code/LICENSE"] = data
			return nil
		}
		target := strings.Replace(path, "llewm/", "plugins/codex/", 1)
		if strings.HasPrefix(path, "claude-code/") {
			target = "plugins/" + path
		}
		p.Files[target] = data
		return nil
	})
	if err != nil {
		return p, err
	}
	keys := make([]string, 0, len(p.Files))
	for key := range p.Files {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, key := range keys {
		fmt.Fprintf(h, "%s\x00%d\x00", key, len(p.Files[key]))
		h.Write(p.Files[key])
	}
	p.Digest = hex.EncodeToString(h.Sum(nil))
	for _, manifest := range []string{"plugins/codex/.codex-plugin/plugin.json", "plugins/claude-code/.claude-plugin/plugin.json"} {
		var value map[string]any
		if err := json.Unmarshal(p.Files[manifest], &value); err != nil {
			return p, err
		}
		version, ok := value["version"].(string)
		if !ok || version == "" {
			return p, fmt.Errorf("plugin version missing")
		}
		version += "+installer." + p.Digest[:12]
		if p.Version != "" && p.Version != version {
			return p, fmt.Errorf("bundled client versions differ")
		}
		p.Version = version
		value["version"] = version
		p.Files[manifest], err = json.MarshalIndent(value, "", "  ")
		if err != nil {
			return p, err
		}
	}
	p.Files[".agents/plugins/marketplace.json"] = []byte(`{"name":"llewm","plugins":[{"name":"llewm","source":{"source":"local","path":"./plugins/codex"},"policy":{"installation":"AVAILABLE","authentication":"ON_INSTALL"},"category":"Productivity"}]}`)
	p.Files[".claude-plugin/marketplace.json"] = []byte(`{"name":"llewm","owner":{"name":"Llewm contributors"},"plugins":[{"name":"llewm","source":"./plugins/claude-code"}]}`)
	p.Files[".llewm-installer.json"], err = json.Marshal(struct {
		Digest  string `json:"digest"`
		Version string `json:"version"`
	}{p.Digest, p.Version})
	return p, err
}

func noSymlinks(path string) error {
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.IsDir()) {
			return fmt.Errorf("installer directory must not traverse a symlink or file: %s", current)
		}
		if filepath.Dir(current) == current {
			return nil
		}
	}
}

// verifyOwned verifies every file, not merely the ownership marker. Modified
// or extra files are user state and must not be overwritten or removed.
func verifyOwned(root string) (string, error) {
	if err := noSymlinks(root); err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(root, ".llewm-installer.json"))
	if err != nil {
		return "", fmt.Errorf("unmanaged installer directory: %w", err)
	}
	var manifest struct {
		Digest string            `json:"digest"`
		Files  map[string]string `json:"files"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil || len(manifest.Digest) != 64 || len(manifest.Files) == 0 {
		return "", fmt.Errorf("invalid installer ownership receipt")
	}
	seen := 0
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("non-regular installer file: %s", path)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == ".llewm-installer.json" {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(body)
		if manifest.Files[rel] != hex.EncodeToString(sum[:]) {
			return fmt.Errorf("modified or extra installer file: %s", rel)
		}
		seen++
		return nil
	})
	if err == nil && seen != len(manifest.Files) {
		err = fmt.Errorf("installer package has missing files")
	}
	return manifest.Digest, err
}

// Stage installs a complete package via a sibling rename. Existing owned bytes
// are retained as a backup. It never touches client configuration or databases.
func (p Package) Stage(root string) (string, error) {
	for name := range p.Files {
		if !fs.ValidPath(name) || strings.Contains(name, "\\") {
			return "", fmt.Errorf("invalid bundled package path: %s", name)
		}
	}
	if err := noSymlinks(root); err != nil {
		return "", err
	}
	old := ""
	if _, err := os.Lstat(root); err == nil {
		old, err = verifyOwned(root)
		if err != nil {
			return "", err
		}
		if old == p.Digest {
			return "", nil
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	parent := filepath.Dir(root)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(parent, ".llewm-stage-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	hashes := map[string]string{}
	for name, body := range p.Files {
		if name == ".llewm-installer.json" {
			continue
		}
		path := filepath.Join(tmp, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return "", err
		}
		if err := os.WriteFile(path, body, 0o600); err != nil {
			return "", err
		}
		sum := sha256.Sum256(body)
		hashes[name] = hex.EncodeToString(sum[:])
	}
	marker, err := json.Marshal(struct {
		Digest  string            `json:"digest"`
		Version string            `json:"version"`
		Files   map[string]string `json:"files"`
	}{p.Digest, p.Version, hashes})
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(tmp, ".llewm-installer.json"), marker, 0o600); err != nil {
		return "", err
	}
	backup := ""
	if old != "" {
		backup, err = os.MkdirTemp(parent, "marketplace-backup-")
		if err != nil {
			return "", err
		}
		if err := os.Remove(backup); err != nil {
			return "", err
		}
		if err := os.Rename(root, backup); err != nil {
			return "", err
		}
	}
	if err := os.Rename(tmp, root); err != nil {
		if backup != "" {
			if restore := os.Rename(backup, root); restore != nil {
				return backup, fmt.Errorf("stage failed: %v; restore failed: %v", err, restore)
			}
		}
		return backup, err
	}
	return backup, nil
}
