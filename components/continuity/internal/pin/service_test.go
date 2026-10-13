package pin

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/decision"
	"github.com/llewm/llewm/components/continuity/internal/resource"
	"github.com/llewm/llewm/components/continuity/internal/topic"
	"github.com/llewm/llewm/components/continuity/internal/trackplan"
)

func TestScopesAndUnpin(t *testing.T) {
	ctx := context.Background()
	db, err := database.Initialize(ctx, filepath.Join(t.TempDir(), "continuity.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	projectID, err := (activity.Store{DB: db}).EnsureProject(ctx, filepath.Join(t.TempDir(), "repo"), "test")
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { clock = clock.Add(time.Second); return clock }
	resources := resource.Service{DB: db, Now: now}
	for _, id := range []string{"box", "forgejo", "laptop"} {
		if _, err := resources.Record(ctx, resource.Resource{ProjectID: projectID, ID: id, Kind: "host", Title: "Host " + id, Creator: "owner"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := (decision.Service{DB: db, Now: now}).Append(ctx, decision.Record{ProjectID: projectID, ID: "d1", Title: "Decide", Rationale: "r", Disposition: "accepted", Actor: "owner", ActorType: "human", Authority: "caller_asserted"}); err != nil {
		t.Fatal(err)
	}
	topics := topic.Service{DB: db, Now: now}
	if _, err := topics.Record(ctx, topic.Topic{ProjectID: projectID, ID: "sync", Title: "Sync", Creator: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err := topics.AddLink(ctx, topic.Link{ProjectID: projectID, TopicID: "sync", TargetKind: "roadmap_node", TargetID: "node-a", Creator: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO roadmap_nodes(project_id,node_id,revision,title,status,priority,source_path,authority,recorded_at,schema_version) VALUES(?,'node-a',1,'Node A','active',10,'docs/roadmap.md','caller_asserted','2026-09-29T12:00:00Z',1)`, projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO roadmap_task_bindings(project_id,roadmap_node_id,task_id,revision,roadmap_node_revision,status,reason,request_key,request_sha256,authority,recorded_at,schema_version) VALUES(?,?,?,?,?,?,?,?,?,?,?,1)`, projectID, "node-a", "task-a", 1, 1, "active", "r", "k", "h", "caller_asserted", "2026-09-29T12:00:00Z"); err != nil {
		t.Fatal(err)
	}
	service := Service{DB: db, Now: now}
	set := func(targetKind, targetID, scopeKind, scopeID, action string) {
		t.Helper()
		if _, err := service.Set(ctx, Pin{ProjectID: projectID, TargetKind: targetKind, TargetID: targetID, ScopeKind: scopeKind, ScopeID: scopeID, Action: action, Creator: "owner"}); err != nil {
			t.Fatal(err)
		}
	}
	set("resource", "box", "task", "task-a", "pin")
	set("resource", "forgejo", "roadmap_node", "node-a", "pin")
	set("decision", "d1", "topic", "sync", "pin")
	set("topic", "sync", "project", "", "pin")
	set("resource", "laptop", "task", "task-b", "pin")
	set("resource", "laptop", "project", "", "pin")
	set("resource", "laptop", "project", "", "unpin")

	refs := func(lines []Line) map[string]string {
		result := map[string]string{}
		for _, line := range lines {
			result[line.Ref] = line.Scope
		}
		return result
	}
	got := refs(must(ForTask(ctx, db, projectID, "task-a")))
	want := map[string]string{"resource:box": "task:task-a", "resource:forgejo": "roadmap_node:node-a", "decision:d1": "topic:sync", "topic:sync": "project"}
	if len(got) != len(want) {
		t.Fatalf("task-a pins: %#v", got)
	}
	for ref, scope := range want {
		if got[ref] != scope {
			t.Fatalf("task-a pins: %#v", got)
		}
	}
	if projectOnly := refs(must(ForTask(ctx, db, projectID, ""))); len(projectOnly) != 1 || projectOnly["topic:sync"] != "project" {
		t.Fatalf("project pins: %#v", projectOnly)
	}
	if all := must(Current(ctx, db, projectID)); len(all) != 5 || all[0].Scope != "project" {
		t.Fatalf("all pins: %#v", all)
	}
	for name, value := range map[string]Pin{
		"missing target":  {ProjectID: projectID, TargetKind: "resource", TargetID: "nope", ScopeKind: "project", Creator: "o"},
		"card target":     {ProjectID: projectID, TargetKind: "card", TargetID: "x", ScopeKind: "project", Creator: "o"},
		"project with id": {ProjectID: projectID, TargetKind: "resource", TargetID: "box", ScopeKind: "project", ScopeID: "x", Creator: "o"},
		"task without id": {ProjectID: projectID, TargetKind: "resource", TargetID: "box", ScopeKind: "task", Creator: "o"},
		"no creator":      {ProjectID: projectID, TargetKind: "resource", TargetID: "box", ScopeKind: "project"},
		"model authority": {ProjectID: projectID, TargetKind: "resource", TargetID: "box", ScopeKind: "project", Creator: "o", Authority: "model_suggested"},
	} {
		if _, err := service.Set(ctx, value); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func must(lines []Line, err error) []Line {
	if err != nil {
		panic(err)
	}
	return lines
}

// TestTrackPlansScopeTasksWithoutBindings mirrors a second machine: task
// bindings do not sync, but track plans do.
func TestTrackPlansScopeTasksWithoutBindings(t *testing.T) {
	ctx := context.Background()
	db, err := database.Initialize(ctx, filepath.Join(t.TempDir(), "continuity.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	projectID, err := (activity.Store{DB: db}).EnsureProject(ctx, filepath.Join(t.TempDir(), "repo"), "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (resource.Service{DB: db}).Record(ctx, resource.Resource{ProjectID: projectID, ID: "box", Kind: "host", Title: "Box", Creator: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err := (trackplan.Service{DB: db}).AppendPlan(ctx, trackplan.Plan{ProjectID: projectID, ID: "plan", RoadmapNodeID: "node-b", TaskID: "task-b", Title: "Plan", Summary: "s", Status: "accepted", Creator: "owner", Items: []trackplan.Item{{ID: "one", Position: 1, Title: "One", Status: "pending"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := (Service{DB: db}).Set(ctx, Pin{ProjectID: projectID, TargetKind: "resource", TargetID: "box", ScopeKind: "roadmap_node", ScopeID: "node-b", Creator: "owner"}); err != nil {
		t.Fatal(err)
	}
	if lines := must(ForTask(ctx, db, projectID, "task-b")); len(lines) != 1 || lines[0].Ref != "resource:box" {
		t.Fatalf("track-plan scope: %#v", lines)
	}
}
