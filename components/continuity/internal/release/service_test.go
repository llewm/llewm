package release

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/database"
)

func TestReviewedReleaseLifecycleAndProjection(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := database.Initialize(ctx, filepath.Join(root, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO projects(id,root,name,created_at) VALUES('p',?,'test','2026-01-01T00:00:00Z')`, root); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO roadmap_nodes(project_id,node_id,revision,title,status,priority,source_path,authority,recorded_at,schema_version,kind,evidence_refs_json) VALUES('p','node',1,'Node','completed',10,'docs/roadmap.md','caller_asserted','2026-01-01T00:00:00Z',1,'work_item','[]')`); err != nil {
		t.Fatal(err)
	}
	payload := `{"id":"narrative-1","project_id":"p","record_type":"work_narrative","title":"Change","trigger":"manual","claims":[],"anchors":[],"creator":"test","authority":"caller_asserted","recorded_at":"2026-01-01T00:00:00Z","schema_version":1}`
	if _, err := db.Exec(`INSERT INTO work_narratives(id,project_id,title,trigger_kind,creator,semantic_sha256,recorded_at,schema_version,payload_json) VALUES('narrative-1','p','Change','manual','test',?, '2026-01-01T00:00:00Z',1,?)`, strings.Repeat("a", 64), payload); err != nil {
		t.Fatal(err)
	}
	service := Service{DB: db, Root: root, Now: func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) }}
	entries := []Entry{{Category: "feature", Wording: "Add reviewed releases.", NodeID: "node", NodeRevision: 1, NarrativeIDs: []string{"narrative-1"}}}
	draft, err := service.Append(ctx, Record{ProjectID: "p", ID: "unreleased", Version: "Unreleased", Title: "Draft", Status: "draft", Entries: entries})
	if err != nil {
		t.Fatal(err)
	}
	if draft.Revision != 1 || draft.ContentSHA256 == "" {
		t.Fatalf("draft = %#v", draft)
	}
	reviewed, err := service.Append(ctx, Record{ProjectID: "p", ID: "v1.0.0", Version: "1.0.0", Title: "First", Status: "reviewed", Reviewer: "qj", Entries: entries})
	if err != nil {
		t.Fatal(err)
	}
	publishedAt := time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)
	published, err := service.Append(ctx, Record{ProjectID: "p", ID: "v1.0.0", Version: "1.0.0", Title: "First", Status: "published", Reviewer: "qj", PublishedAt: &publishedAt, Entries: entries})
	if err != nil {
		t.Fatal(err)
	}
	if published.Revision != 2 || published.ContentSHA256 != reviewed.ContentSHA256 {
		t.Fatalf("published = %#v reviewed = %#v", published, reviewed)
	}
	projection, err := service.Render(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# Changelog", "## Unreleased", "## 1.0.0", "### Features", "Add reviewed releases."} {
		if !strings.Contains(projection.Markdown, want) {
			t.Fatalf("projection lacks %q:\n%s", want, projection.Markdown)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "CHANGELOG.md"), []byte(projection.Markdown), 0o600); err != nil {
		t.Fatal(err)
	}
	check, err := service.Check(ctx, "p", "CHANGELOG.md")
	if err != nil || check.State != "current" {
		t.Fatalf("check = %#v err=%v", check, err)
	}
}

func TestPublishedReleaseMustMatchReviewedRevision(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := database.Initialize(ctx, filepath.Join(root, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO projects(id,root,name,created_at) VALUES('p',?,'test','2026-01-01T00:00:00Z')`, root); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO roadmap_nodes(project_id,node_id,revision,title,status,priority,source_path,authority,recorded_at,schema_version,kind,evidence_refs_json) VALUES('p','node',1,'Node','completed',10,'docs/roadmap.md','caller_asserted','2026-01-01T00:00:00Z',1,'work_item','[]')`); err != nil {
		t.Fatal(err)
	}
	payload := `{"id":"n","project_id":"p","record_type":"work_narrative","title":"N","trigger":"manual","claims":[],"anchors":[],"creator":"test","authority":"caller_asserted","recorded_at":"2026-01-01T00:00:00Z","schema_version":1}`
	if _, err := db.Exec(`INSERT INTO work_narratives(id,project_id,title,trigger_kind,creator,semantic_sha256,recorded_at,schema_version,payload_json) VALUES('n','p','N','manual','test',?,'2026-01-01T00:00:00Z',1,?)`, strings.Repeat("b", 64), payload); err != nil {
		t.Fatal(err)
	}
	service := Service{DB: db, Root: root}
	entries := []Entry{{Category: "fix", Wording: "Original.", NodeID: "node", NodeRevision: 1, NarrativeIDs: []string{"n"}}}
	if _, err := service.Append(ctx, Record{ProjectID: "p", ID: "v1", Version: "1", Title: "One", Status: "reviewed", Reviewer: "qj", Entries: entries}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	entries[0].Wording = "Changed after review."
	if _, err := service.Append(ctx, Record{ProjectID: "p", ID: "v1", Version: "1", Title: "One", Status: "published", Reviewer: "qj", PublishedAt: &now, Entries: entries}); err == nil || !strings.Contains(err.Error(), "exactly match") {
		t.Fatalf("changed publication err=%v", err)
	}
}
