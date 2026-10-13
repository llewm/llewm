package researchcapture

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/database"
)

func TestReportForCheckpointRecommendsReviewAndRecognizesCoverage(t *testing.T) {
	ctx := context.Background()
	db, err := database.Initialize(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.ExecContext(ctx, `INSERT INTO projects(id,root,name,created_at) VALUES('p','/p','p','2026-10-05T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `INSERT INTO sessions(id,project_id,client,started_at,status) VALUES('s','p','codex','2026-10-05T00:00:00Z','active')`); err != nil {
		t.Fatal(err)
	}
	tools := []map[string]string{
		{"tool": "mcp__continuity__continuity_history_search"},
		{"tool": "mcp__continuity__continuity_history_show"},
		{"tool": "Read"}, {"tool": "Grep"},
		{"tool": "exec_command", "command": "rg -n history components"},
		{"tool": "exec_command", "command": "git log -5 --oneline"},
	}
	for index, payload := range tools {
		raw, _ := json.Marshal(payload)
		if _, err = db.ExecContext(ctx, `INSERT INTO events(id,occurred_at,received_at,project_id,session_id,client,event_type,category,actor_type,payload_json,schema_version,content_hash) VALUES(?, '2026-10-05T00:01:00Z','2026-10-05T00:01:00Z','p','s','codex','posttooluse','tool','cloud_model',?,1,'h')`, index, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	service := Service{DB: db}
	report, err := service.ReportForCheckpoint(ctx, "p", Candidate{SessionID: "s", TaskID: "task"})
	if err != nil {
		t.Fatal(err)
	}
	if report == nil || report.State != "review_recommended" || report.DiscoveryOperations != 6 || len(report.DiscoveryKinds) != 2 {
		t.Fatalf("unexpected report: %+v", report)
	}
	covered, err := service.ReportForCheckpoint(ctx, "p", Candidate{SessionID: "s", TaskID: "task", EvidenceRefs: 1})
	if err != nil || covered == nil || covered.State != "covered" {
		t.Fatalf("covered report: %+v err=%v", covered, err)
	}
}

func TestReportForCheckpointSkipsLightDiscovery(t *testing.T) {
	ctx := context.Background()
	db, err := database.Initialize(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.ExecContext(ctx, `INSERT INTO projects(id,root,name,created_at) VALUES('p','/p','p','2026-10-05T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if report, err := (Service{DB: db}).ReportForCheckpoint(ctx, "p", Candidate{}); err != nil || report != nil {
		t.Fatalf("light report: %+v err=%v", report, err)
	}
}
