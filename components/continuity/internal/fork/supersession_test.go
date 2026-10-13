package fork

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/database"
)

func TestSupersessionForksReportTheOutermostDivergence(t *testing.T) {
	ctx := context.Background()
	db, err := database.Initialize(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`INSERT INTO projects(id,root,name,created_at) VALUES ('p','/test','test','2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	minute := 0
	narrative := func(id, supersedes string) {
		t.Helper()
		minute++
		at := time.Date(2026, 9, 30, 12, minute, 0, 0, time.UTC).Format(time.RFC3339Nano)
		if _, err := db.Exec(`INSERT INTO work_narratives(id,project_id,title,trigger_kind,creator,semantic_sha256,supersedes_id,recorded_at,schema_version,payload_json) VALUES(?,'p',?,'manual','human','hash',NULLIF(?,''),?,1,'{}')`, id, "Title "+id, supersedes, at); err != nil {
			t.Fatal(err)
		}
	}
	forks := func() []SupersessionFork {
		t.Helper()
		values, err := SupersessionForks(ctx, db, "p", "")
		if err != nil {
			t.Fatal(err)
		}
		return values
	}
	// root -> a -> {a1, a2}, root -> b: one fork at root with three heads, not
	// a second one at a.
	narrative("root", "")
	narrative("a", "root")
	if len(forks()) != 0 {
		t.Fatal("a single successor chain is not a fork")
	}
	narrative("b", "root")
	narrative("a1", "a")
	narrative("a2", "a")
	values := forks()
	if len(values) != 1 || values[0].Ref() != "narrative:root" || values[0].Title != "Title root" || len(values[0].Heads) != 3 || values[0].Proposed().ID != "a2" || values[0].Proposed().Depth != 2 || values[0].Heads[2].ID != "b" || values[0].Heads[2].Depth != 1 {
		t.Fatalf("forks: %#v", values)
	}
	insert := func(id, subject, accepted string, rejected ...string) {
		t.Helper()
		if err := Insert(ctx, db, Resolution{ProjectID: "p", ID: id, Family: FamilySupersession, Subject: subject, AcceptedHead: accepted, RejectedHeads: rejected, Resolver: "owner", Authority: "user_reported", RecordedAt: time.Now(), SchemaVersion: SchemaVersion}); err != nil {
			t.Fatal(err)
		}
	}
	// A resolution that rejects only b leaves the fork open at root.
	insert("r1", "narrative:root", "a1", "b")
	values = forks()
	if len(values) != 1 || values[0].Ref() != "narrative:root" || len(values[0].Heads) != 2 || values[0].Heads[1].ID != "a1" {
		t.Fatalf("after rejecting b: %#v", values)
	}
	if only, err := SupersessionForks(ctx, db, "p", "narrative:a"); err != nil || len(only) != 0 {
		t.Fatalf("a nested fork was reported on its own: %#v %v", only, err)
	}
	insert("r2", "narrative:root", "a1", "a2")
	if values = forks(); len(values) != 0 {
		t.Fatalf("after resolving: %#v", values)
	}
	var superseded int
	if err := db.QueryRow(`SELECT COUNT(*) FROM superseded_records WHERE project_id='p' AND family='narrative' AND id IN ('b','a2','a1')`).Scan(&superseded); err != nil || superseded != 2 {
		t.Fatalf("superseded_records lists %d of the rejected heads, %v", superseded, err)
	}
	// A rejected head continued on a machine that had not synced reopens it.
	narrative("b2", "b")
	if values = forks(); len(values) != 1 || values[0].Ref() != "narrative:root" || len(values[0].Heads) != 2 {
		t.Fatalf("after continuing a rejected head: %#v", values)
	}

	for _, bad := range []string{"task:x", "topic_revision:x@2", "evidence:x", "narrative:", "unknown:x", "decision:a@1"} {
		if SupersessionSubject(bad) {
			t.Fatalf("%q was taken for a supersession subject", bad)
		}
	}
	if _, err := Validate(Resolution{ID: "r", Family: FamilySupersession, Subject: "decision:x", AcceptedHead: "a", RejectedHeads: []string{"b"}, Continuation: "c", Resolver: "owner", Authority: "user_reported", RecordedAt: time.Now(), SchemaVersion: SchemaVersion}); err == nil {
		t.Fatal("a supersession resolution with a continuation was accepted")
	}
}
