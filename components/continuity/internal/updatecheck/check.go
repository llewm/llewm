// Package updatecheck compares installed identity with an explicit source or
// distribution target. It never installs, builds, checks out code or migrates.
package updatecheck

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/llewm/llewm/components/continuity/internal/buildinfo"
	"github.com/llewm/llewm/components/continuity/internal/gitenv"
)

type Asset struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Target string `json:"target,omitempty"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}
type Manifest struct {
	SchemaVersion        int     `json:"schema_version"`
	Version              string  `json:"version"`
	Channel              string  `json:"channel"`
	Commit               string  `json:"commit"`
	BuildTime            string  `json:"build_time"`
	MCPAPIVersion        int     `json:"mcp_api_version"`
	RuntimeQualification string  `json:"runtime_qualification"`
	Assets               []Asset `json:"assets"`
}
type Report struct {
	Method                  string         `json:"method"`
	State                   string         `json:"state"`
	Installed               buildinfo.Info `json:"installed"`
	SourceRoot              string         `json:"source_root,omitempty"`
	SourceDirty             bool           `json:"source_dirty"`
	Remote                  string         `json:"remote,omitempty"`
	Branch                  string         `json:"branch,omitempty"`
	Fetched                 bool           `json:"fetched"`
	TargetCommit            string         `json:"target_commit"`
	TargetVersion           string         `json:"target_version,omitempty"`
	Asset                   *Asset         `json:"asset,omitempty"`
	ArtifactVerified        bool           `json:"artifact_verified"`
	PublisherAuthentication string         `json:"publisher_authentication"`
	NextAction              string         `json:"next_action"`
}

var hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
var remotePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

// Source compares against checkout HEAD, or with fetch against the exact fetched
// branch tip. Fetch only updates Git objects/FETCH_HEAD; it never merges or resets.
func Source(ctx context.Context, installed buildinfo.Info, root, remote, branch string, fetch bool) (Report, error) {
	r := Report{Method: "source", Installed: installed, PublisherAuthentication: "not_verified"}
	root, err := filepath.Abs(root)
	if err != nil {
		return r, err
	}
	run := func(args ...string) (string, error) {
		out, err := gitenv.Command(ctx, append([]string{"-C", root}, args...)...).Output()
		if err != nil {
			return "", fmt.Errorf("git %s failed: %w", args[0], err)
		}
		return strings.TrimSpace(string(out)), nil
	}
	top, err := run("rev-parse", "--show-toplevel")
	if err != nil {
		return r, err
	}
	root = top
	r.SourceRoot = top
	mod, err := os.ReadFile(filepath.Join(root, "components/continuity/go.mod"))
	if err != nil {
		return r, fmt.Errorf("source root must be a Llewm checkout: %w", err)
	}
	// Git checkouts on Windows may use CRLF line endings.
	if !strings.HasPrefix(strings.ReplaceAll(string(mod), "\r\n", "\n"), "module github.com/llewm/llewm/components/continuity\n") {
		return r, fmt.Errorf("source checkout has an unexpected module identity")
	}
	status, err := run("status", "--porcelain")
	if err != nil {
		return r, err
	}
	r.SourceDirty = status != ""
	ref := "HEAD"
	if fetch {
		if !remotePattern.MatchString(remote) || strings.HasPrefix(branch, "-") {
			return r, fmt.Errorf("use a configured remote name and a valid branch")
		}
		if _, err = run("check-ref-format", "--branch", branch); err != nil {
			return r, err
		}
		if _, err = run("fetch", "--no-tags", "--no-recurse-submodules", remote, "refs/heads/"+branch); err != nil {
			return r, err
		}
		ref = "FETCH_HEAD"
		r.Fetched = true
		r.Remote = remote
		r.Branch = branch
	}
	r.TargetCommit, err = run("rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return r, err
	}
	r.State = "unknown"
	if installed.BuildDirty || !commitPattern.MatchString(installed.GitCommit) {
		r.NextAction = "Identify the dirty or unstamped installed build before selecting an update."
		return r, nil
	}
	switch {
	case installed.GitCommit == r.TargetCommit:
		r.State = "current"
	default:
		if _, err := run("merge-base", "--is-ancestor", installed.GitCommit, r.TargetCommit); err == nil {
			r.State = "update_available"
		} else if _, err := run("merge-base", "--is-ancestor", r.TargetCommit, installed.GitCommit); err == nil {
			r.State = "installed_ahead"
		} else {
			r.State = "diverged_or_history_unavailable"
		}
	}
	r.NextAction = "Use the update skill to build the exact target in a separate clean checkout, verify it, back up, migrate explicitly and refresh owned plugins."
	if r.State == "current" {
		r.NextAction = "Installed commit matches this checked target; independently verify loaded client identity."
	}
	if r.SourceDirty {
		r.NextAction = "Preserve dirty source files. Prepare a separate clean checkout of the exact target; do not reset or overwrite this checkout."
	}
	return r, nil
}

// Release accepts the manifest emitted by package-preview. It does not assert
// latest-version discovery or publisher authenticity from a locally supplied file.
func Release(installed buildinfo.Info, manifestPath, target, artifact string) (Report, error) {
	r := Report{Method: "binary", Installed: installed, PublisherAuthentication: "not_verified"}
	f, err := os.Open(manifestPath)
	if err != nil {
		return r, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return r, err
	}
	if len(b) > 1<<20 {
		return r, fmt.Errorf("manifest exceeds 1 MiB")
	}
	var m Manifest
	if err = json.Unmarshal(b, &m); err != nil {
		return r, err
	}
	if m.SchemaVersion != 1 || m.Version == "" || !commitPattern.MatchString(m.Commit) || m.MCPAPIVersion < 1 || len(m.Assets) == 0 || len(m.Assets) > 64 {
		return r, fmt.Errorf("unsupported or incomplete distribution manifest")
	}
	for _, a := range m.Assets {
		if a.Name == "" || strings.ContainsAny(a.Name, "/\\") || a.Name == "." || a.Name == ".." || !hashPattern.MatchString(a.SHA256) || a.Size <= 0 {
			return r, fmt.Errorf("invalid manifest asset")
		}
		if a.Kind == "binary-bundle" && a.Target == target {
			if r.Asset != nil {
				return r, fmt.Errorf("duplicate binary bundle for %s", target)
			}
			copy := a
			r.Asset = &copy
		}
	}
	if r.Asset == nil {
		return r, fmt.Errorf("manifest has no binary bundle for %s", target)
	}
	r.TargetCommit = m.Commit
	r.TargetVersion = m.Version
	r.State = "different_target"
	if installed.GitCommit == m.Commit && !installed.BuildDirty {
		r.State = "current"
	}
	if artifact != "" {
		f, err := os.Open(artifact)
		if err != nil {
			return r, err
		}
		defer f.Close()
		h := sha256.New()
		size, err := io.Copy(h, io.LimitReader(f, r.Asset.Size+1))
		if err != nil {
			return r, err
		}
		if size != r.Asset.Size || hex.EncodeToString(h.Sum(nil)) != r.Asset.SHA256 {
			return r, fmt.Errorf("artifact size or SHA-256 does not match the manifest")
		}
		r.ArtifactVerified = true
	}
	r.NextAction = "Confirm publisher authenticity and intended release/channel, verify the downloaded artifact, then use the update skill for staged install, backup, explicit migrations and client verification. Checksums alone do not authenticate the publisher."
	return r, nil
}
