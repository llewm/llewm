package updatecheck

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/buildinfo"
	"github.com/llewm/llewm/components/continuity/internal/gitenv"
)

func TestSourceCheckPreservesDirtyWorkAndClassifiesAncestry(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	git := func(args ...string) string {
		t.Helper()
		out, err := gitenv.Command(ctx, append([]string{"-C", root}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	if err := os.MkdirAll(filepath.Join(root, "components/continuity"), 0700); err != nil {
		t.Fatal(err)
	}
	mod := filepath.Join(root, "components/continuity/go.mod")
	if err := os.WriteFile(mod, []byte("module github.com/llewm/llewm/components/continuity\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "one")
	first := git("rev-parse", "HEAD")
	git("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-qm", "two")
	second := git("rev-parse", "HEAD")
	r, err := Source(ctx, buildinfo.Info{GitCommit: first}, root, "origin", "main", false)
	if err != nil || r.State != "update_available" || r.TargetCommit != second || r.Fetched {
		t.Fatalf("check %+v %v", r, err)
	}
	if err = os.WriteFile(mod, []byte("module github.com/llewm/llewm/components/continuity\n// user change\n"), 0600); err != nil {
		t.Fatal(err)
	}
	before := git("status", "--porcelain")
	r, err = Source(ctx, buildinfo.Info{GitCommit: second}, root, "origin", "main", false)
	if err != nil || !r.SourceDirty || git("status", "--porcelain") != before || git("rev-parse", "HEAD") != second {
		t.Fatalf("dirty check modified work %+v %v", r, err)
	}
	r, err = Source(ctx, buildinfo.Info{GitCommit: "unknown"}, root, "origin", "main", false)
	if err != nil || r.State != "unknown" {
		t.Fatalf("unstamped %+v %v", r, err)
	}
	// Fetch an explicitly selected local remote; the target is fetched without
	// checking it out, modifying the dirty worktree or advancing local HEAD.
	upstream := t.TempDir()
	if out, err := gitenv.Command(ctx, "clone", "-q", root, upstream).CombinedOutput(); err != nil {
		t.Fatalf("clone %s %v", out, err)
	}
	if out, err := gitenv.Command(ctx, "-C", upstream, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-qm", "upstream").CombinedOutput(); err != nil {
		t.Fatalf("commit %s %v", out, err)
	}
	branch := strings.TrimSpace(git("branch", "--show-current"))
	git("remote", "add", "fixture", upstream)
	r, err = Source(ctx, buildinfo.Info{GitCommit: second}, root, "fixture", branch, true)
	if err != nil || r.State != "update_available" || !r.Fetched || git("rev-parse", "HEAD") != second || git("status", "--porcelain") != before {
		t.Fatalf("fetch modified checkout %+v %v", r, err)
	}
}

func TestSourceCheckModuleLineEndings(t *testing.T) {
	const module = "module github.com/llewm/llewm/components/continuity"
	for _, tc := range []struct {
		name    string
		body    string
		wantErr bool
	}{
		{name: "LF", body: module + "\n\ngo 1.25.0\n"},
		{name: "CRLF", body: module + "\r\n\r\ngo 1.25.0\r\n"},
		{name: "wrong module LF", body: module + "-other\n", wantErr: true},
		{name: "wrong module CRLF", body: module + "-other\r\n", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			ctx := context.Background()
			git := func(args ...string) string {
				t.Helper()
				out, err := gitenv.Command(ctx, append([]string{"-C", root}, args...)...).CombinedOutput()
				if err != nil {
					t.Fatalf("git: %v %s", err, out)
				}
				return strings.TrimSpace(string(out))
			}
			git("init", "-q")
			git("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-qm", "fixture")
			head := git("rev-parse", "HEAD")
			if err := os.MkdirAll(filepath.Join(root, "components/continuity"), 0700); err != nil {
				t.Fatal(err)
			}
			mod := filepath.Join(root, "components/continuity/go.mod")
			if err := os.WriteFile(mod, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			r, err := Source(ctx, buildinfo.Info{GitCommit: head}, root, "", "", false)
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "unexpected module identity") {
					t.Fatalf("expected module identity error, got %v", err)
				}
			} else if err != nil || r.State != "current" || r.TargetCommit != head {
				t.Fatalf("source check: %+v, %v", r, err)
			}
			body, err := os.ReadFile(mod)
			if err != nil || string(body) != tc.body {
				t.Fatalf("source check changed go.mod: %q, %v", body, err)
			}
		})
	}
}

func TestBinaryManifestSelectsPlatformAndVerifiesArtifact(t *testing.T) {
	dir := t.TempDir()
	body := []byte("fixture bundle")
	sum := sha256.Sum256(body)
	artifact := filepath.Join(dir, "bundle.zip")
	if err := os.WriteFile(artifact, body, 0600); err != nil {
		t.Fatal(err)
	}
	m := Manifest{SchemaVersion: 1, Version: "0.2.0-preview.1", Channel: "preview", Commit: strings.Repeat("a", 40), MCPAPIVersion: 127, Assets: []Asset{{Name: "bundle.zip", Kind: "binary-bundle", Target: "linux/amd64", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body))}}}
	path := filepath.Join(dir, "manifest.json")
	write := func() {
		b, _ := json.Marshal(m)
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	r, err := Release(buildinfo.Info{GitCommit: strings.Repeat("b", 40)}, path, "linux/amd64", artifact)
	if err != nil || r.State != "different_target" || !r.ArtifactVerified || r.PublisherAuthentication != "not_verified" {
		t.Fatalf("manifest %+v %v", r, err)
	}
	if _, err = Release(buildinfo.Info{}, path, "windows/arm64", ""); err == nil {
		t.Fatal("accepted missing platform")
	}
	if err = os.WriteFile(artifact, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Release(buildinfo.Info{}, path, "linux/amd64", artifact); err == nil {
		t.Fatal("accepted corrupt artifact")
	}
	m.Assets[0].Name = "../bundle.zip"
	write()
	if _, err = Release(buildinfo.Info{}, path, "linux/amd64", ""); err == nil {
		t.Fatal("accepted unsafe asset name")
	}
}
