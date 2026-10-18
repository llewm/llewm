package clientinstall

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/buildinfo"
	"github.com/llewm/llewm/components/continuity/internal/pluginidentity"
	"github.com/llewm/llewm/components/continuity/internal/redact"
)

const Selector = "llewm@llewm"

type Runner func(context.Context, ...string) ([]byte, error)

// NativeRunner can invoke only a resolved client executable. It never invokes a
// shell, passes an arbitrary recipe, or retains the client's configuration dump.
func NativeRunner(executable string) Runner {
	return func(ctx context.Context, args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, executable, args...)
		var out, stderr limitedBuffer
		command.Stdout, command.Stderr = &out, &stderr
		if err := command.Run(); err != nil {
			message := redact.String(stderr.String())
			if len(message) > 512 {
				message = message[:512]
			}
			return nil, fmt.Errorf("client command failed: %w: %s", err, message)
		}
		if out.overflow {
			return nil, fmt.Errorf("client response exceeds 1 MiB")
		}
		return out.Bytes(), nil
	}
}

type limitedBuffer struct {
	bytes.Buffer
	overflow bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	available := (1 << 20) - b.Len()
	if len(p) > available {
		b.overflow = true
		p = p[:available]
	}
	_, err := b.Buffer.Write(p)
	return n, err
}

type Receipt struct {
	PluginCache     pluginidentity.Snapshot `json:"plugin_cache"`
	Executable      buildinfo.Info          `json:"executable"`
	Client          string                  `json:"client"`
	Scope           string                  `json:"scope"`
	ClientVersion   string                  `json:"client_version,omitempty"`
	PluginVersion   string                  `json:"plugin_version"`
	BundleSHA256    string                  `json:"bundle_sha256"`
	Directory       string                  `json:"directory"`
	Backup          string                  `json:"backup,omitempty"`
	State           string                  `json:"state"`
	PluginInstalled bool                    `json:"plugin_installed"`
	Permissions     string                  `json:"permissions"`
	HookTrust       string                  `json:"hook_trust"`
	RestartRequired bool                    `json:"restart_required"`
	LiveVerified    bool                    `json:"live_verified"`
	Steps           [][]string              `json:"steps"`
	CompletedSteps  int                     `json:"completed_steps"`
	Next            []string                `json:"next"`
	Problem         string                  `json:"problem,omitempty"`
}

type Service struct {
	Client  string
	Root    string
	Package Package
	Run     Runner
}

type marketplace struct {
	Name string `json:"name"`
	Root string `json:"root"`
	Path string `json:"path"`
}
type installedPlugin struct {
	InstallPath    string `json:"installPath"`
	ReadFromFolder string `json:"readFromFolder"`
	ID             string `json:"id"`
	PluginID       string `json:"pluginId"`
	Version        string `json:"version"`
	Scope          string `json:"scope"`
	Installed      bool   `json:"installed"`
	Enabled        bool   `json:"enabled"`
}

func (s Service) marketplaces(ctx context.Context) ([]marketplace, error) {
	data, err := s.Run(ctx, "plugin", "marketplace", "list", "--json")
	if err != nil {
		return nil, err
	}
	if s.Client == "codex" {
		var value struct {
			Items *[]marketplace `json:"marketplaces"`
		}
		if err := json.Unmarshal(data, &value); err != nil || value.Items == nil {
			return nil, fmt.Errorf("unsupported Codex marketplace response")
		}
		return *value.Items, nil
	}
	var items []marketplace
	if err := json.Unmarshal(data, &items); err != nil || items == nil {
		return nil, fmt.Errorf("unsupported Claude marketplace response")
	}
	return items, nil
}

func (s Service) installed(ctx context.Context) (*installedPlugin, error) {
	data, err := s.Run(ctx, "plugin", "list", "--json")
	if err != nil {
		return nil, err
	}
	var items []installedPlugin
	if s.Client == "codex" {
		var value struct {
			Items *[]installedPlugin `json:"installed"`
		}
		if err := json.Unmarshal(data, &value); err != nil || value.Items == nil {
			return nil, fmt.Errorf("unsupported Codex plugin response")
		}
		items = *value.Items
	} else if err := json.Unmarshal(data, &items); err != nil || items == nil {
		return nil, fmt.Errorf("unsupported Claude plugin response")
	}
	for _, item := range items {
		if item.ID != Selector && item.PluginID != Selector {
			continue
		}
		if s.Client == "claude-code" && item.Scope != "user" {
			return nil, fmt.Errorf("Llewm already has a %s-scope Claude installation; review it before user-scope setup", item.Scope)
		}
		if s.Client != "codex" || item.Installed {
			return &item, nil
		}
	}
	return nil, nil
}

// cache inspects the registered cache, not just the marketplace source. Claude
// may load a local source folder directly, so verify that folder separately too.
func (s Service) cache(item *installedPlugin) pluginidentity.Snapshot {
	root := item.InstallPath
	basis := "client_registration"
	if root == "" && s.Client == "codex" {
		root = pluginidentity.CodexCacheRoot(item.Version)
		basis = "codex_version_cache_layout"
	}
	expected := map[string][]byte{}
	prefix := "plugins/" + s.Client + "/"
	for name, body := range s.Package.Files {
		if strings.HasPrefix(name, prefix) {
			expected[strings.TrimPrefix(name, prefix)] = body
		}
	}
	observed := pluginidentity.Inspect(s.Client, root, expected)
	observed.RootBasis = basis
	if observed.Status == "current" && item.ReadFromFolder != "" {
		folder := pluginidentity.Inspect(s.Client, item.ReadFromFolder, expected)
		if folder.Status != "current" {
			observed.Status, observed.Reason = folder.Status, "client's direct-load folder does not match the bundled plugin"
		}
	}
	return observed
}

func (s Service) Inspect(ctx context.Context) (Receipt, error) {
	r := Receipt{PluginCache: pluginidentity.Snapshot{Status: "unknown", Reason: "plugin is not installed"}, Client: s.Client, Scope: "user", PluginVersion: s.Package.Version, BundleSHA256: s.Package.Digest, Directory: s.Root, State: "preview", Permissions: "needs_review", HookTrust: "needs_client_review", RestartRequired: true, Steps: [][]string{}, Next: []string{"Run llewm init separately in each Git project.", "Review permissions with llewm client permissions --client " + s.Client + " --write --scope user (or install --permissions after init).", "Review hooks in the client; restart the client/parent IDE and start a fresh session.", "Compare MCP continuity_info with llewm version and verify bootstrap/checkpoint recovery in the fresh client."}}
	r.Executable = buildinfo.Current()
	if s.Client != "codex" && s.Client != "claude-code" {
		return r, fmt.Errorf("client must be codex or claude-code")
	}
	if !filepath.IsAbs(s.Root) {
		return r, fmt.Errorf("installer directory must be absolute")
	}
	if err := noSymlinks(s.Root); err != nil {
		return r, err
	}
	if _, err := os.Lstat(s.Root); err == nil {
		if _, err := verifyOwned(s.Root); err != nil {
			return r, err
		}
	} else if !os.IsNotExist(err) {
		return r, err
	}
	version, err := s.Run(ctx, "--version")
	if err != nil {
		return r, err
	}
	r.ClientVersion = strings.TrimSpace(redact.String(string(version)))
	if len(r.ClientVersion) > 256 {
		return r, fmt.Errorf("unsupported client version response")
	}
	install := "install"
	if s.Client == "codex" {
		install = "add"
	}
	for _, args := range [][]string{{"plugin", "marketplace", "add", "--help"}, {"plugin", install, "--help"}, {"plugin", "list", "--help"}, {"plugin", "marketplace", "list", "--help"}} {
		help, err := s.Run(ctx, args...)
		if err != nil {
			return r, err
		}
		if !bytes.Contains(help, []byte("--json")) {
			return r, fmt.Errorf("client command %s does not support --json; update the client", strings.Join(args, " "))
		}
	}
	if s.Client == "claude-code" {
		for _, operation := range []string{"install", "update", "enable"} {
			help, err := s.Run(ctx, "plugin", operation, "--help")
			if err != nil {
				return r, err
			}
			if !bytes.Contains(help, []byte("--scope")) || !bytes.Contains(help, []byte("--json")) {
				return r, fmt.Errorf("Claude plugin %s must support --scope and --json", operation)
			}
		}
	}
	markets, err := s.marketplaces(ctx)
	if err != nil {
		return r, err
	}
	found := false
	for _, market := range markets {
		if market.Name != "llewm" {
			continue
		}
		found = true
		path := market.Root
		if s.Client == "claude-code" {
			path = market.Path
		}
		if filepath.Clean(path) != filepath.Clean(s.Root) {
			return r, fmt.Errorf("marketplace llewm points elsewhere (%s); review/remove that client marketplace before installer setup", path)
		}
	}
	if !found {
		args := []string{"plugin", "marketplace", "add", s.Root, "--json"}
		if s.Client == "claude-code" {
			args = append(args, "--scope", "user")
		}
		r.Steps = append(r.Steps, args)
	}
	item, err := s.installed(ctx)
	if err != nil {
		return r, err
	}
	r.PluginInstalled = item != nil && item.Version == s.Package.Version && item.Enabled
	if item != nil {
		r.PluginCache = s.cache(item)
	}
	if item == nil || item.Version != s.Package.Version || r.PluginCache.Status != "current" || (s.Client == "codex" && !item.Enabled) {
		operation := install
		if s.Client == "claude-code" && item != nil {
			operation = "update"
		}
		args := []string{"plugin", operation, Selector, "--json"}
		if s.Client == "claude-code" {
			args = append(args, "--scope", "user")
		}
		r.Steps = append(r.Steps, args)
	}
	// Install enables a new plugin; enable on an already-enabled Claude plugin
	// fails. Updating an existing plugin preserves its enablement.
	if s.Client == "claude-code" && item != nil && !item.Enabled {
		r.Steps = append(r.Steps, []string{"plugin", "enable", Selector, "--scope", "user", "--json"})
	}
	return r, nil
}

func (s Service) Apply(ctx context.Context) (Receipt, error) {
	r, err := s.Inspect(ctx)
	if err != nil {
		r.State = "blocked"
		r.Problem = err.Error()
		return r, err
	}
	parent := filepath.Dir(s.Root)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return r, err
	}
	lockPath := filepath.Join(parent, ".install-lock")
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return r, fmt.Errorf("installer is locked at %s; inspect any other installer before removing a stale lock: %w", lockPath, err)
	}
	lock.Close()
	defer os.Remove(lockPath)
	// Revalidate after taking the lock; a concurrent installer may have changed
	// either the local package or the client marketplace while we waited.
	r, err = s.Inspect(ctx)
	if err != nil {
		return r, err
	}
	r.Backup, err = s.Package.Stage(s.Root)
	if err != nil {
		r.State = "failed"
		r.Problem = err.Error()
		return r, err
	}
	for _, args := range r.Steps {
		if _, err = s.Run(ctx, args...); err != nil {
			r.State = "partial"
			r.PluginInstalled = false
			r.Problem = err.Error()
			r.Next = append([]string{"Package retained; correct the reported client failure and rerun install. No client rollback is inferred."}, r.Next...)
			return r, err
		}
		r.CompletedSteps++
	}
	item, err := s.installed(ctx)
	r.PluginInstalled = item != nil && item.Version == s.Package.Version && item.Enabled
	if err != nil || !r.PluginInstalled {
		if err == nil {
			err = fmt.Errorf("client did not report the expected enabled plugin version")
		}
		r.State = "partial"
		r.Problem = err.Error()
		return r, err
	}
	r.PluginCache = s.cache(item)
	if r.PluginCache.Status != "current" {
		err = fmt.Errorf("plugin registration succeeded but cached files did not verify: %s; review the exact plugin cache and rerun its native update", r.PluginCache.Reason)
		r.State, r.Problem = "partial", err.Error()
		return r, err
	}
	r.State = "installed"
	return r, nil
}

// WriteText prints only selected receipt fields, never raw client JSON/settings.
func WriteText(out io.Writer, r Receipt) {
	fmt.Fprintf(out, "Client: %s (%s scope)\nClient version: %s\nPlugin: %s\nPackage: %s\nState: %s\nPlugin installed: %t\nPermissions: %s\nHook trust: %s\nRestart required: %t\nLive verified: %t\n", r.Client, r.Scope, r.ClientVersion, r.PluginVersion, r.Directory, r.State, r.PluginInstalled, r.Permissions, r.HookTrust, r.RestartRequired, r.LiveVerified)
	fmt.Fprintf(out, "Plugin cache: %s (%s)\n", r.PluginCache.Status, r.PluginCache.Root)
	if r.Backup != "" {
		fmt.Fprintln(out, "Previous package retained:", r.Backup)
	}
	if r.Problem != "" {
		fmt.Fprintln(out, "Problem:", r.Problem)
	}
	for _, step := range r.Steps {
		fmt.Fprintf(out, "Client argv: %q\n", step)
	}
	for _, next := range r.Next {
		fmt.Fprintln(out, "Next:", next)
	}
}
