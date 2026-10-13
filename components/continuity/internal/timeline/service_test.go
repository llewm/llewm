package timeline

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/database"
)

func TestTimelinePagesAndFiltersSavedRecords(t *testing.T) {
	ctx := context.Background()
	db, err := database.Initialize(ctx, filepath.Join(t.TempDir(), "timeline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		`INSERT INTO projects(id,root,name,created_at) VALUES ('p','/test','test','2026-01-01T00:00:00Z')`,
		`INSERT INTO checkpoints(id,project_id,task_title,status,summary,next_action,payload_json,created_at,task_id) VALUES ('c1','p','First','in_progress','summary','next','{}','2026-01-02T00:00:00Z','task-a')`,
		`INSERT INTO checkpoints(id,project_id,task_title,status,summary,next_action,payload_json,created_at,task_id) VALUES ('c2','p','Second','completed','summary','next','{}','2026-01-03T00:00:00Z','task-a')`,
		`INSERT INTO roadmap_nodes(project_id,node_id,revision,title,status,priority,source_path,authority,recorded_at,schema_version) VALUES ('p','node-a',1,'Roadmap','active',1,'docs/roadmap.md','caller_asserted','2026-01-04T00:00:00Z',1)`,
		`INSERT INTO events(id,occurred_at,received_at,project_id,client,event_type,category,actor_type,schema_version,content_hash) VALUES ('e1','2026-01-05T00:00:00Z','2026-01-05T00:00:01Z','p','codex','posttooluse','tool','cloud_model',1,'hash')`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	s := Service{DB: db}
	first, err := s.List(ctx, "p", Options{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Entries) != 1 || first.Entries[0].SourceRef != "node:node-a@1" || first.NextCursor == "" {
		t.Fatalf("first page: %+v", first)
	}
	second, err := s.List(ctx, "p", Options{Limit: 1, Cursor: first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Entries) != 1 || second.Entries[0].SourceRef != "checkpoint:c2" || second.NextCursor == "" {
		t.Fatalf("second page: %+v", second)
	}
	third, err := s.List(ctx, "p", Options{Limit: 1, Cursor: second.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(third.Entries) != 1 || third.Entries[0].SourceRef != "checkpoint:c1" || third.NextCursor != "" {
		t.Fatalf("third page: %+v", third)
	}
	filtered, err := s.List(ctx, "p", Options{TaskID: "task-a", Kind: "checkpoint", Limit: 5})
	if err != nil || len(filtered.Entries) != 2 {
		t.Fatalf("filtered: %+v, %v", filtered, err)
	}
	if _, err := s.List(ctx, "p", Options{TaskID: "another", Cursor: first.NextCursor}); err == nil {
		t.Fatal("accepted cursor with changed filters")
	}
	if _, err := s.List(ctx, "p", Options{Kind: "invented"}); err == nil {
		t.Fatal("accepted unknown kind")
	}
	activity, err := s.List(ctx, "p", Options{Kind: "activity_event", Actor: "codex", Limit: 5})
	if err != nil || len(activity.Entries) != 1 || activity.Entries[0].SourceRef != "event:e1" || activity.Entries[0].OccurredAt == activity.Entries[0].RecordedAt {
		t.Fatalf("explicit activity event: %+v, %v", activity, err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM checkpoints`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("read mutated saved records: count=%d err=%v", count, err)
	}
}
