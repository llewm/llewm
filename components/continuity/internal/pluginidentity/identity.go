// Package pluginidentity compares inert plugin files with the executable's
// embedded payload. It never executes client commands or reads client settings.
package pluginidentity

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/llewm/llewm/components/continuity/plugins"
)

type Snapshot struct {
	Client         string `json:"client,omitempty"`
	Root           string `json:"root,omitempty"`
	RootBasis      string `json:"root_basis,omitempty"`
	LaunchVersion  string `json:"launch_version,omitempty"`
	Version        string `json:"version,omitempty"`
	SHA256         string `json:"sha256,omitempty"`
	ExpectedSHA256 string `json:"expected_sha256,omitempty"`
	Status         string `json:"status"`
	Reason         string `json:"reason"`
}

func Expected(client string) (map[string][]byte, error) {
	prefix := "llewm"
	if client == "claude-code" {
		prefix = "claude-code"
	} else if client != "codex" {
		return nil, fmt.Errorf("unknown plugin client")
	}
	result := map[string][]byte{}
	err := fs.WalkDir(plugins.Assets, prefix, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := plugins.Assets.ReadFile(path)
		if err != nil {
			return err
		}
		result[strings.TrimPrefix(path, prefix+"/")] = b
		return nil
	})
	return result, err
}

// CodexCacheRoot is the native local-marketplace layout. It is an inferred disk
// location, not a client attestation that this session loaded these skills.
func CodexCacheRoot(version string) string {
	if version == "" || version == "." || version == ".." || strings.ContainsAny(version, "/\\\x00") {
		return ""
	}
	base := os.Getenv("CODEX_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".codex")
	}
	if !filepath.IsAbs(base) {
		return ""
	}
	return filepath.Join(base, "plugins", "cache", "llewm", "llewm", version)
}

// Capture uses only the named non-secret launch fields. Claude supplies the
// expanded root; Codex's legacy MCP format does not expand a plugin-root token.
func Capture() Snapshot {
	client, version, root := os.Getenv("LLEWM_PLUGIN_CLIENT"), os.Getenv("LLEWM_PLUGIN_VERSION"), os.Getenv("LLEWM_PLUGIN_ROOT")
	basis := "client_launch"
	if root == "" && client == "codex" {
		root = CodexCacheRoot(version)
		basis = "codex_version_cache_layout"
	}
	expected, err := Expected(client)
	if err != nil || version == "" || root == "" {
		return Snapshot{Status: "unknown", Reason: "plugin launch identity unavailable"}
	}
	result := Inspect(client, root, expected)
	result.RootBasis, result.LaunchVersion = basis, version
	if result.Status == "current" && result.Version != version {
		result.Status, result.Reason = "stale", "plugin launch version differs from observed package"
	}
	return result
}

func Inspect(client, root string, expected map[string][]byte) Snapshot {
	result := Snapshot{Client: client, Root: root, Status: "unknown", Reason: "plugin files could not be verified"}
	if !filepath.IsAbs(root) || len(expected) == 0 {
		return result
	}
	want, _, err := fingerprint(expected)
	if err != nil {
		return result
	}
	result.ExpectedSHA256 = want
	actual := map[string][]byte{}
	count, total := 0, 0
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		count++
		if count > 512 {
			return fmt.Errorf("too many entries")
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("non-regular plugin file")
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		// Source checkouts keep the common license outside the plugin directory.
		// It is verified separately in the installer-owned package receipt.
		if rel == "LICENSE" {
			if _, required := expected[rel]; !required {
				return nil
			}
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		b, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
		f.Close()
		total += len(b)
		if err != nil || len(b) > 1<<20 || total > 4<<20 {
			return fmt.Errorf("plugin file limit")
		}
		actual[rel] = b
		return nil
	})
	if err != nil {
		return result
	}
	got, version, err := fingerprint(actual)
	if err != nil {
		return result
	}
	result.SHA256, result.Version = got, version
	result.Status, result.Reason = "stale", "plugin files differ from the executable's bundled plugin"
	if want == got {
		result.Status, result.Reason = "current", "plugin files match the executable's bundled plugin; session skill activation still requires fresh-client verification"
	}
	return result
}

// Normalize only the installer version suffix and JSON formatting of manifests
// and launch configuration. All skill/hook bytes and every filename are hashed.
func fingerprint(files map[string][]byte) (string, string, error) {
	keys := make([]string, 0, len(files))
	for key := range files {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	h := sha256.New()
	version := ""
	for _, key := range keys {
		body := files[key]
		if key == ".codex-plugin/plugin.json" || key == ".claude-plugin/plugin.json" || key == ".mcp.json" {
			var value map[string]any
			if err := json.Unmarshal(body, &value); err != nil {
				return "", "", err
			}
			if key != ".mcp.json" {
				v, ok := value["version"].(string)
				if !ok || v == "" {
					return "", "", fmt.Errorf("missing version")
				}
				version = v
				value["version"] = baseVersion(v)
			} else if servers, ok := value["mcpServers"].(map[string]any); ok {
				if server, ok := servers["continuity"].(map[string]any); ok {
					if env, ok := server["env"].(map[string]any); ok {
						if v, ok := env["LLEWM_PLUGIN_VERSION"].(string); ok {
							env["LLEWM_PLUGIN_VERSION"] = baseVersion(v)
						}
					}
				}
			}
			var err error
			body, err = json.Marshal(value)
			if err != nil {
				return "", "", err
			}
		}
		fmt.Fprintf(h, "%s\x00%d\x00", key, len(body))
		h.Write(body)
	}
	if version == "" {
		return "", "", fmt.Errorf("missing manifest")
	}
	return hex.EncodeToString(h.Sum(nil)), version, nil
}

func baseVersion(version string) string {
	base, suffix, ok := strings.Cut(version, "+installer.")
	if ok && len(suffix) == 12 {
		if _, err := hex.DecodeString(suffix); err == nil {
			return base
		}
	}
	return version
}
