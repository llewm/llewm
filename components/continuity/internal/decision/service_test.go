package decision

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/database"
)

func decisionFixture(t *testing.T) (Service, string) {
	t.Helper()
	ctx := context.Background()
	db, err := database.Initialize(ctx, t.TempDir()+"/decisions.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	const projectID = "project-one"
	if _, err = db.ExecContext(ctx, `INSERT INTO projects(id,root,name,created_at) VALUES(?,?,?,?)`, projectID, "/project-one", "one", time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `INSERT INTO projects(id,root,name,created_at) VALUES(?,?,?,?)`, "project-two", "/project-two", "two", time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	return Service{DB: db, Now: func() time.Time { return time.Date(2026, 9, 26, 8, 0, 0, 0, time.UTC) }}, projectID
}

func sampleRecord(projectID, id string) Record {
	return Record{ID: id, ProjectID: projectID, Title: "Choose local state", Rationale: "Keep token=private-value in local state", Disposition: "accepted", Actor: "owner", ActorType: "human", Authority: "user_reported", ReviewSource: "checkpoint:review-1", Links: []Link{{Kind: "subject", TargetKind: "roadmap_node", TargetID: "hygiene", TargetRevision: "5"}, {Kind: "supports", TargetKind: "decision", TargetID: "later-decision"}}}
}

func TestAppendReplayCollisionAndBacklinks(t *testing.T) {
	ctx := context.Background()
	service, projectID := decisionFixture(t)
	input := sampleRecord(projectID, "choice-a")
	created, err := service.Append(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if created.Replay || !strings.Contains(created.Rationale, "[REDACTED]") || strings.Contains(created.Rationale, "private-value") || created.RecordSHA256 == "" {
		t.Fatalf("unexpected redaction or hash: %#v", created)
	}
	replayed, err := service.Append(ctx, input)
	if err != nil || !replayed.Replay || replayed.RecordSHA256 != created.RecordSHA256 {
		t.Fatalf("replay: %#v %v", replayed, err)
	}
	input.Title = "Conflicting choice"
	if _, err = service.Append(ctx, input); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("collision accepted: %v", err)
	}
	shown, err := service.Show(ctx, projectID, "decision:choice-a", 0)
	if err != nil || len(shown.Links) != 2 || shown.Replay || shown.Rationale != created.Rationale {
		t.Fatalf("show: %#v %v", shown, err)
	}
	if _, err = service.Show(ctx, "project-two", "choice-a", 0); err != sql.ErrNoRows {
		t.Fatalf("cross-project show: %v", err)
	}
	otherProject := sampleRecord("project-two", "choice-a")
	otherProject.Links = nil
	if _, err = service.Append(ctx, otherProject); err != nil {
		t.Fatalf("same stable ID in another project: %v", err)
	}
	list, err := service.List(ctx, projectID, ListOptions{TargetKind: "roadmap_node", TargetID: "hygiene", TargetRevision: "5", Limit: 1})
	if err != nil || list.Total != 1 || len(list.Decisions) != 1 || list.BodiesLoaded != 0 {
		t.Fatalf("list: %#v %v", list, err)
	}
	links, err := service.Backlinks(ctx, projectID, "decision", "later-decision", "", 1, 0)
	if err != nil || links.Total != 1 || len(links.Backlinks) != 1 || links.Backlinks[0].TargetResolution != "missing" || len(links.IncludedFamilies) != 4 || links.BodiesLoaded != 0 {
		t.Fatalf("backlinks: %#v %v", links, err)
	}
	if _, err = service.Backlinks(ctx, "project-two", "decision", "later-decision", "", 1, 0); err != nil {
		t.Fatal(err)
	}
	other, err := service.Backlinks(ctx, "project-two", "decision", "later-decision", "", 1, 0)
	if err != nil || other.Total != 0 {
		t.Fatalf("cross-project backlinks: %#v %v", other, err)
	}
}

func TestSupersessionAndReadBounds(t *testing.T) {
	ctx := context.Background()
	service, projectID := decisionFixture(t)
	first := sampleRecord(projectID, "first")
	first.Links = nil
	if _, err := service.Append(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := sampleRecord(projectID, "second")
	second.SupersedesID = "first"
	second.Links = []Link{{Kind: "supersedes", TargetKind: "decision", TargetID: "first"}}
	if _, err := service.Append(ctx, second); err != nil {
		t.Fatal(err)
	}
	shown, err := service.Show(ctx, projectID, "first", 0)
	if err != nil || shown.SupersededBy != "second" {
		t.Fatalf("supersession: %#v %v", shown, err)
	}
	page, err := service.List(ctx, projectID, ListOptions{Limit: 1})
	if err != nil || page.Total != 2 || len(page.Decisions) != 1 || !page.Truncated || page.Decisions[0].ID != "second" {
		t.Fatalf("page: %#v %v", page, err)
	}
	backlinks, err := service.Backlinks(ctx, projectID, "decision", "first", "", 1, 0)
	if err != nil || backlinks.Total != 1 || backlinks.Backlinks[0].TargetResolution != "resolved" {
		t.Fatalf("resolved backlink: %#v %v", backlinks, err)
	}
	third := sampleRecord(projectID, "third")
	third.SupersedesID = "first"
	third.Links = nil
	if _, err = service.Append(ctx, third); err == nil {
		t.Fatal("accepted conflicting successor")
	}
	if _, err = service.Show(ctx, projectID, "second", 512); err == nil {
		t.Fatal("accepted undersized exact-show byte limit")
	}
	if _, err = service.List(ctx, projectID, ListOptions{Limit: 101}); err == nil {
		t.Fatal("accepted unbounded list")
	}
}

func TestValidationRejectsUnreviewedAndDuplicateLinks(t *testing.T) {
	ctx := context.Background()
	service, projectID := decisionFixture(t)
	input := sampleRecord(projectID, "invalid")
	input.ActorType = "agent"
	if _, err := service.Append(ctx, input); err == nil {
		t.Fatal("accepted agent decision")
	}
	input.ActorType = "human"
	input.ReviewSource = ""
	if _, err := service.Append(ctx, input); err == nil {
		t.Fatal("accepted user-reported decision without review source")
	}
	input.ReviewSource = "checkpoint:review-1"
	input.Links = append(input.Links, input.Links[0])
	if _, err := service.Append(ctx, input); err == nil {
		t.Fatal("accepted duplicate link")
	}
	input.Links = input.Links[:1]
	input.Links[0].TargetKind = "file"
	if _, err := service.Append(ctx, input); err == nil {
		t.Fatal("accepted unsupported bare file link")
	}
}

func TestBacklinksJoinExactIndexedFamilies(t *testing.T) {
	ctx := context.Background()
	service, projectID := decisionFixture(t)
	db := service.DB
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO work_narratives(id,project_id,title,trigger_kind,creator,semantic_sha256,recorded_at,schema_version,payload_json) VALUES(?,?,'Narrative','manual','owner','hash','2026-09-26T08:01:00Z',1,'{}')`, []any{"narrative-a", projectID}},
		{`INSERT INTO work_narrative_anchors(project_id,narrative_id,ordinal,anchor_kind,target,target_revision,authority) VALUES(?, 'narrative-a',0,'roadmap_node','node-a','5','caller_asserted')`, []any{projectID}},
		{`INSERT INTO work_items(id,project_id,kind,title,body,creator,roadmap_node_id,authority,recorded_at,schema_version) VALUES(?,?,'idea','Test','Test','agent','node-a','model_suggested','2026-09-26T08:02:00Z',1)`, []any{"item-a", projectID}},
		{`INSERT INTO track_plan_revisions(project_id,id,revision,roadmap_node_id,title,summary,status,creator,authority,recorded_at,schema_version) VALUES(?, 'plan-a',2,'node-a','Plan','Plan','proposed','agent','model_suggested','2026-09-26T08:03:00Z',1)`, []any{projectID}},
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	// Work items and plans name no node revision, so only exact anchors match one.
	exact, err := service.Backlinks(ctx, projectID, "roadmap_node", "node-a", "5", 1, 0)
	if err != nil || exact.Total != 1 || exact.Truncated || exact.Backlinks[0].SourceKind != "narrative" || exact.BodiesLoaded != 0 {
		t.Fatalf("exact page: %#v %v", exact, err)
	}
	unversioned, err := service.Backlinks(ctx, projectID, "roadmap_node", "node-a", "", 2, 0)
	// An unversioned lookup spans every target revision and reports the exact one.
	if err != nil || unversioned.Total != 3 || !unversioned.Truncated || unversioned.Backlinks[0].SourceKind != "track_plan" || unversioned.Backlinks[0].SourceRevision != "2" || unversioned.Backlinks[1].SourceKind != "work_item" {
		t.Fatalf("unversioned lookup: %#v %v", unversioned, err)
	}
	second, err := service.Backlinks(ctx, projectID, "roadmap_node", "node-a", "", 2, 2)
	if err != nil || second.Total != 3 || second.Truncated || second.Backlinks[0].SourceKind != "narrative" || second.Backlinks[0].TargetRevision != "5" {
		t.Fatalf("unversioned lookup: %#v %v", unversioned, err)
	}
	other, err := service.Backlinks(ctx, projectID, "roadmap_node", "node-a", "4", 20, 0)
	if err != nil || other.Total != 0 {
		t.Fatalf("other exact revision: %#v %v", other, err)
	}
	isolated, err := service.Backlinks(ctx, "project-two", "roadmap_node", "node-a", "5", 20, 0)
	if err != nil || isolated.Total != 0 {
		t.Fatalf("cross-project backlinks: %#v %v", isolated, err)
	}
}

func TestAdditionalOriginBacklinks(t *testing.T) {
	ctx := context.Background()
	service, projectID := decisionFixture(t)
	db := service.DB
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO work_items(id,project_id,kind,title,body,creator,task_id,authority,recorded_at,schema_version) VALUES('item-task',?,'task','Task','Task','agent','task-a','model_suggested','2026-09-26T08:03:00Z',1)`, []any{projectID}},
		{`INSERT INTO track_plan_revisions(project_id,id,revision,roadmap_node_id,task_id,title,summary,status,creator,authority,recorded_at,schema_version) VALUES(?,'plan-a',1,'node-a','task-a','Plan','Plan','proposed','agent','model_suggested','2026-09-26T08:04:00Z',1)`, []any{projectID}},
		{`INSERT INTO track_plan_work_items(project_id,plan_id,plan_revision,item_id) VALUES(?,'plan-a',1,'item-task')`, []any{projectID}},
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	task, err := service.Backlinks(ctx, projectID, "task", "task-a", "", 20, 0)
	if err != nil || task.Total != 2 || task.Backlinks[0].SourceKind != "track_plan" || task.Backlinks[1].SourceKind != "work_item" {
		t.Fatalf("task backlinks: %#v %v", task, err)
	}
	item, err := service.Backlinks(ctx, projectID, "work_item", "item-task", "", 20, 0)
	if err != nil || item.Total != 1 || item.Backlinks[0].LinkKind != "includes_work_item" {
		t.Fatalf("plan work item link: %#v %v", item, err)
	}
	other, err := service.Backlinks(ctx, "project-two", "task", "task-a", "", 20, 0)
	if err != nil || other.Total != 0 {
		t.Fatalf("cross-project backlinks: %#v %v", other, err)
	}
}
