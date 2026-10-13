package resource

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/database"
)

func testService(t *testing.T) (context.Context, Service, string) {
	t.Helper()
	ctx := context.Background()
	db, err := database.Initialize(ctx, filepath.Join(t.TempDir(), "continuity.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	projectID, err := (activity.Store{DB: db}).EnsureProject(ctx, filepath.Join(t.TempDir(), "repo"), "test")
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	return ctx, Service{DB: db, Now: func() time.Time { clock = clock.Add(time.Minute); return clock }}, projectID
}

func TestRecordShowListAndNotes(t *testing.T) {
	ctx, service, projectID := testService(t)
	box := Resource{ProjectID: projectID, ID: "resource:test-box", Kind: "host", Title: "Test box", Summary: "Second machine for sync tests",
		Attributes: map[string]string{"ssh": "flux@192.168.98.198", "auth": "plaintext credential fallback"}, Creator: "owner"}
	first, err := service.Record(ctx, box)
	if err != nil || first.ID != "test-box" || first.Revision != 1 || first.Status != "accepted" {
		t.Fatalf("record: %#v %v", first, err)
	}
	box.Summary = "Second machine; joined the Llewm history"
	if second, err := service.Record(ctx, box); err != nil || second.Revision != 2 {
		t.Fatalf("revision: %#v %v", second, err)
	}
	if _, err := service.Record(ctx, Resource{ProjectID: projectID, ID: "forgejo", Kind: "service", Title: "Forgejo", Creator: "agent", Model: "model-x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddNote(ctx, Note{ProjectID: projectID, ResourceID: "test-box", Text: "~/.local/bin is not on PATH over SSH", Authority: "machine_observed", Creator: "agent"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddNote(ctx, Note{ProjectID: projectID, ResourceID: "missing", Text: "x", Creator: "agent"}); err == nil {
		t.Fatal("note on a missing resource was accepted")
	}

	detail, err := service.Show(ctx, projectID, "test-box", 0, 0)
	if err != nil || detail.Resource.Revision != 2 || detail.Resource.Attributes["ssh"] != "flux@192.168.98.198" || len(detail.Notes) != 1 || detail.Notes[0].Authority != "machine_observed" {
		t.Fatalf("show: %#v %v", detail, err)
	}
	if old, err := service.Show(ctx, projectID, "test-box", 1, 0); err != nil || !strings.HasPrefix(old.Resource.Summary, "Second machine for") {
		t.Fatalf("show revision 1: %#v %v", old, err)
	}
	hosts, err := service.List(ctx, projectID, "host", "", 0)
	if err != nil || len(hosts) != 1 || hosts[0].Revision != 2 {
		t.Fatalf("list hosts: %#v %v", hosts, err)
	}
	proposed, err := service.List(ctx, projectID, "", "proposed", 0)
	if err != nil || len(proposed) != 1 || proposed[0].Authority != "model_suggested" {
		t.Fatalf("list proposed: %#v %v", proposed, err)
	}
	var hits int
	if err := service.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM history_chunks WHERE history_chunks MATCH 'PATH' AND family='resource' AND kind='note'`).Scan(&hits); err != nil || hits != 1 {
		t.Fatalf("note not searchable: %d %v", hits, err)
	}
}

func TestRefusesCredentials(t *testing.T) {
	ctx, service, projectID := testService(t)
	for name, attributes := range map[string]map[string]string{
		"password key":     {"password": "hunter2"},
		"token key":        {"api_token": "abc"},
		"private key name": {"ssh-private-key": "path"},
		"secret value":     {"auth": "token=abc123secretvalue"},
		"bad key":          {"Bad Key": "x"},
		"empty value":      {"ssh": " "},
	} {
		if _, err := service.Record(ctx, Resource{ProjectID: projectID, ID: "box", Kind: "host", Title: "Box", Creator: "owner", Attributes: attributes}); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	for name, value := range map[string]Resource{
		"unknown kind":           {ProjectID: projectID, ID: "box", Kind: "database", Title: "Box", Creator: "owner"},
		"model accepted":         {ProjectID: projectID, ID: "box", Kind: "host", Title: "Box", Creator: "agent", Model: "m", Status: "accepted"},
		"id with slash":          {ProjectID: projectID, ID: "a/b", Kind: "host", Title: "Box", Creator: "owner"},
		"note authority invalid": {},
	} {
		if name == "note authority invalid" {
			if _, err := ValidateNote(Note{ResourceID: "box", Text: "x", Creator: "o", Authority: "model_suggested"}); err == nil {
				t.Error("model_suggested note authority was accepted")
			}
			continue
		}
		if _, err := service.Record(ctx, value); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}
