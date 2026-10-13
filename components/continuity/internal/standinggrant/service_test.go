package standinggrant

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/procedures"
)

func setup(t *testing.T) (*sql.DB, string, *time.Time, Service) {
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
	clock := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	return db, projectID, &clock, Service{DB: db, Now: func() time.Time { return clock }}
}

func runbook(projectID, id string, steps ...procedures.Step) procedures.Procedure {
	if len(steps) == 0 {
		steps = []procedures.Step{{ID: "run", Description: "Run it"}}
	}
	return procedures.Procedure{ProjectID: projectID, ID: id, Title: "Install " + id, Purpose: "Install the build", Kind: procedures.KindRunbook, Status: "accepted",
		Platforms: []string{"linux/amd64"}, Parameters: []procedures.Parameter{{Name: id + "-bin", Description: "binary", Required: true}},
		DeclaredEffects: []string{"replaces the binary"}, Steps: steps, Source: "docs/installation.md"}
}

func record(t *testing.T, db *sql.DB, value procedures.Procedure) {
	t.Helper()
	if _, err := (procedures.Service{DB: db}).Append(context.Background(), value); err != nil {
		t.Fatal(err)
	}
}

func TestCreateCoversNamedProceduresAndVoidsOnRevision(t *testing.T) {
	ctx := context.Background()
	db, projectID, clock, service := setup(t)
	record(t, db, runbook(projectID, "local", procedures.Step{ID: "build", Description: "Build", ArgvTemplate: []string{"go", "build"}}))
	record(t, db, runbook(projectID, "remote", procedures.Step{ID: "copy", Description: "Copy"}))
	record(t, db, runbook(projectID, "both",
		procedures.Step{ID: "local", Description: "Run procedure:local on this machine."},
		procedures.Step{ID: "remote", Description: "Run procedure:remote, then procedure:local again"}))

	grant, err := service.Create(ctx, Request{ProjectID: projectID, ProcedureID: "procedure:both", Parameters: map[string]string{"remote-bin": "/opt/c"}, Creator: "engineer", Reason: "installs are routine"})
	if err != nil {
		t.Fatal(err)
	}
	if grant.Ref() != "procedure:both@1" || grant.Trigger != TriggerAfterSliceCommit || grant.State != Active || grant.Authority != "user_reported" {
		t.Fatalf("grant: %+v", grant)
	}
	if len(grant.Covers) != 3 || grant.Covers[1].ProcedureID != "local" || grant.Covers[2].ProcedureID != "remote" {
		t.Fatalf("covers: %+v", grant.Covers)
	}
	notice, err := Notice(ctx, db, projectID, *clock)
	if err != nil || !strings.Contains(notice, "procedure:both@1 after slice commit (covers local@1, remote@1), by engineer") {
		t.Fatalf("notice %q %v", notice, err)
	}
	if got, err := service.ForProcedure(ctx, projectID, "remote"); err != nil || len(got) != 1 {
		t.Fatalf("for procedure: %+v %v", got, err)
	}

	// Revising a named procedure voids the grant; nothing else changes it.
	next := runbook(projectID, "remote", procedures.Step{ID: "copy", Description: "Copy with scp"})
	next.Revision = 2
	record(t, db, next)
	got, err := service.Get(ctx, projectID, grant.ID)
	if err != nil || got.State != Void || got.VoidReason != "procedure:remote is now at revision 2" || got.Covers[2].LatestRevision != 2 {
		t.Fatalf("void grant: %+v %v", got, err)
	}
	if active, err := service.List(ctx, projectID, false); err != nil || len(active) != 0 {
		t.Fatalf("active after void: %+v %v", active, err)
	}
	notice, err = Notice(ctx, db, projectID, *clock)
	if err != nil || !strings.HasPrefix(notice, "1 void because a covered procedure changed") {
		t.Fatalf("void notice %q %v", notice, err)
	}
}

func TestCreateValidation(t *testing.T) {
	ctx := context.Background()
	db, projectID, clock, service := setup(t)
	record(t, db, runbook(projectID, "local"))
	record(t, db, runbook(projectID, "broken", procedures.Step{ID: "x", Description: "Run procedure:missing"}))
	past := clock.Add(-time.Hour)
	for name, request := range map[string]Request{
		"no creator":      {ProjectID: projectID, ProcedureID: "local"},
		"unknown":         {ProjectID: projectID, ProcedureID: "nope", Creator: "e"},
		"old revision":    {ProjectID: projectID, ProcedureID: "local", Revision: 2, Creator: "e"},
		"missing named":   {ProjectID: projectID, ProcedureID: "broken", Creator: "e"},
		"trigger":         {ProjectID: projectID, ProcedureID: "local", Trigger: "always", Creator: "e"},
		"parameter":       {ProjectID: projectID, ProcedureID: "local", Parameters: map[string]string{"host": "x"}, Creator: "e"},
		"secret value":    {ProjectID: projectID, ProcedureID: "local", Parameters: map[string]string{"local-bin": "token=abcdefghijklmnop"}, Creator: "e"},
		"expiry in past":  {ProjectID: projectID, ProcedureID: "local", ExpiresAt: &past, Creator: "e"},
		"multiline param": {ProjectID: projectID, ProcedureID: "local", Parameters: map[string]string{"local-bin": "a\nb"}, Creator: "e"},
	} {
		if _, err := service.Create(ctx, request); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	proposed := runbook(projectID, "draft")
	proposed.Status = "proposed"
	record(t, db, proposed)
	if _, err := service.Create(ctx, Request{ProjectID: projectID, ProcedureID: "draft", Creator: "e"}); err == nil {
		t.Error("granted a proposed procedure")
	}
	// A superseded latest revision voids an existing grant.
	grant, err := service.Create(ctx, Request{ProjectID: projectID, ProcedureID: "local", Creator: "e"})
	if err != nil {
		t.Fatal(err)
	}
	superseded := runbook(projectID, "local")
	superseded.Revision, superseded.Status = 2, "superseded"
	record(t, db, superseded)
	if got, err := service.Get(ctx, projectID, grant.ID); err != nil || got.State != Void {
		t.Fatalf("superseded: %+v %v", got, err)
	}
}

func TestExpiryAndRevocation(t *testing.T) {
	ctx := context.Background()
	db, projectID, clock, service := setup(t)
	record(t, db, runbook(projectID, "local"))
	until := clock.Add(24 * time.Hour)
	expiring, err := service.Create(ctx, Request{ProjectID: projectID, ProcedureID: "local", ExpiresAt: &until, Creator: "e"})
	if err != nil {
		t.Fatal(err)
	}
	lasting, err := service.Create(ctx, Request{ProjectID: projectID, ProcedureID: "local", Creator: "e"})
	if err != nil {
		t.Fatal(err)
	}
	notice, _ := Notice(ctx, db, projectID, *clock)
	if !strings.Contains(notice, "until 2026-10-01") {
		t.Fatalf("notice %q", notice)
	}
	*clock = until
	if got, _ := service.Get(ctx, projectID, expiring.ID); got.State != Expired {
		t.Fatalf("not expired: %+v", got)
	}
	revoked, err := service.Revoke(ctx, projectID, lasting.ID, "e", "no longer needed")
	if err != nil || revoked.State != Revoked || revoked.Revocation.Reason != "no longer needed" {
		t.Fatalf("revoke: %+v %v", revoked, err)
	}
	if _, err := service.Revoke(ctx, projectID, lasting.ID, "e", ""); err == nil {
		t.Fatal("revoked twice")
	}
	if _, err := service.Revoke(ctx, projectID, "nope", "e", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown revoke: %v", err)
	}
	all, err := service.List(ctx, projectID, true)
	if err != nil || len(all) != 2 {
		t.Fatalf("all: %+v %v", all, err)
	}
	if notice, _ := Notice(ctx, db, projectID, *clock); notice != "" {
		t.Fatalf("notice after expiry and revocation: %q", notice)
	}
}
