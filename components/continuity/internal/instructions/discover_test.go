package instructions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverSections(t *testing.T) {
	root := t.TempDir()
	body := "# Project\n\nOverview.\n\n## Tests\n\nRun go test.\n"
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	sections, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(sections) != 2 {
		t.Fatalf("got %d sections, want 2", len(sections))
	}
	if sections[1].HeadingPath != "Tests" {
		t.Fatalf("second heading = %q", sections[1].HeadingPath)
	}
}

func TestGlobalSourcesBoundedAndExplicit(t *testing.T) {
	root := t.TempDir()
	if got, err := DiscoverGlobal(root); err != nil || len(got) != 0 {
		t.Fatalf("empty global sources: %v, %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(root, "unrelated.txt"), []byte("Not guidance"), 0o600); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("x", 100_000)
	path := filepath.Join(root, "AGENTS.md")
	if err := os.WriteFile(path, []byte("# Rules\n"+long), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := DiscoverGlobal(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Content != long || got[0].Precedence != 50 || got[0].Source != path || got[0].Hash == "" {
		t.Fatalf("incorrect global section: count %d", len(got))
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", maxSourceBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := DiscoverGlobal(root); err == nil {
		t.Fatal("accepted oversized instruction file")
	}
}
