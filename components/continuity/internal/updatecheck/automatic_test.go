package updatecheck

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/buildinfo"
	"github.com/llewm/llewm/components/continuity/internal/gitenv"
)

func TestAutomaticDisabledCachingFailureAndInvalidation(t *testing.T) {
	ctx := context.Background()
	dir, root := t.TempDir(), t.TempDir()
	now := time.Now().UTC()
	installed := buildinfo.Info{GitCommit: strings.Repeat("a", 40)}
	if r, err := Automatic(ctx, dir, installed, now); err != nil || r.Line() != "" {
		t.Fatalf("disabled: %+v %v", r, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "update-check-cache.json")); !os.IsNotExist(err) {
		t.Fatal("disabled check wrote cache")
	}
	c := DefaultAutomaticConfig()
	c.Enabled = true
	c.SourceRoot = root
	if err := SaveAutomaticConfig(dir, c); err != nil {
		t.Fatal(err)
	}
	failed, err := Automatic(ctx, dir, installed, now)
	if err != nil || failed.Error == "" || !strings.Contains(failed.Line(), "unavailable") {
		t.Fatalf("failure: %+v %v", failed, err)
	}
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
	if err := os.WriteFile(filepath.Join(root, "components/continuity/go.mod"), []byte("module github.com/llewm/llewm/components/continuity\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "initial")
	installed.GitCommit = git("rev-parse", "HEAD")
	// Unchanged installed identity retains an offline failure for the interval.
	old := installed
	old.GitCommit = strings.Repeat("a", 40)
	r, err := Automatic(ctx, dir, old, now.Add(time.Minute))
	if err != nil || !r.CheckedAt.Equal(now) || r.Error == "" {
		t.Fatalf("failure cache: %+v %v", r, err)
	}
	// Installing a different build immediately invalidates the cached failure.
	r, err = Automatic(ctx, dir, installed, now.Add(2*time.Minute))
	if err != nil || r.Report == nil || r.Report.State != "current" {
		t.Fatalf("identity invalidation: %+v %v", r, err)
	}
	git("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-qm", "new")
	r, err = Automatic(ctx, dir, installed, now.Add(3*time.Minute))
	if err != nil || r.Report.State != "current" {
		t.Fatalf("interval cache: %+v %v", r, err)
	}
	r, err = Automatic(ctx, dir, installed, now.Add(25*time.Hour))
	if err != nil || r.Report == nil || r.Report.State != "update_available" {
		t.Fatalf("expired cache: %+v %v", r, err)
	}
	c.Interval = "1m"
	if err := SaveAutomaticConfig(dir, c); err != nil {
		t.Fatal(err)
	}
	r, err = Automatic(ctx, dir, installed, now.Add(25*time.Hour+time.Second))
	if err != nil || !r.CheckedAt.Equal(now.Add(25*time.Hour+time.Second)) {
		t.Fatalf("config invalidation: %+v %v", r, err)
	}
	// Corruption and a backwards clock never reuse an apparently fresh result.
	if err := os.WriteFile(filepath.Join(dir, "update-check-cache.json"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	r, err = Automatic(ctx, dir, installed, now)
	if err != nil || r.Report == nil {
		t.Fatalf("corruption: %+v %v", r, err)
	}
	r, err = Automatic(ctx, dir, installed, now.Add(-time.Hour))
	if err != nil || !r.CheckedAt.Equal(now.Add(-time.Hour)) {
		t.Fatalf("clock rollback: %+v %v", r, err)
	}
}

func TestAutomaticValidationAndCancelledCheck(t *testing.T) {
	c := DefaultAutomaticConfig()
	c.Enabled = true
	c.SourceRoot = t.TempDir()
	for _, mutate := range []func(*AutomaticConfig){func(c *AutomaticConfig) { c.SourceRoot = "relative" }, func(c *AutomaticConfig) { c.Interval = "0s" }, func(c *AutomaticConfig) { c.Timeout = "1h" }, func(c *AutomaticConfig) { c.Fetch = true; c.Remote = "-bad" }} {
		bad := c
		mutate(&bad)
		if err := bad.Validate(); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	dir := t.TempDir()
	if err := SaveAutomaticConfig(dir, c); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, err := Automatic(ctx, dir, buildinfo.Info{}, time.Now())
	if err != nil || r.Error == "" {
		t.Fatalf("cancel: %+v %v", r, err)
	}
}
