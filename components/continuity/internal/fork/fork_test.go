package fork

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
	"github.com/llewm/llewm/components/continuity/internal/database"
)

func TestCheckpointHeadsForkAndResolve(t *testing.T) {
	ctx := context.Background()
	db, err := database.Initialize(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`INSERT INTO projects(id,root,name,created_at) VALUES ('p','/test','test','2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	service := checkpoint.Service{DB: db}
	write := func(next, basedOn string) string {
		t.Helper()
		// Each save names the local tip; BasedOn records what the writer (another
		// machine, before syncing) actually continued.
		var tip string
		if err := db.QueryRow(`SELECT id FROM checkpoints WHERE project_id='p' AND task_id='task-a' ORDER BY created_at DESC,id DESC LIMIT 1`).Scan(&tip); err != nil {
			t.Fatal(err)
		}
		created, err := service.Create(ctx, checkpoint.Checkpoint{ProjectID: "p", TaskID: "task-a", PredecessorCheckpointID: tip, Task: "Work", Status: "in_progress", NextAction: next, BasedOn: basedOn})
		if err != nil {
			t.Fatal(err)
		}
		return created.ID
	}
	forks := func() []Fork {
		t.Helper()
		values, err := CheckpointForks(ctx, db, "p", "")
		if err != nil {
			t.Fatal(err)
		}
		return values
	}
	// A checkpoint without a context (written before contexts existed) is ignored.
	if _, err := db.Exec(`INSERT INTO checkpoints(id,project_id,task_id,task_title,status,summary,next_action,payload_json,created_at) VALUES ('legacy','p','task-a','Work','in_progress','','Old','{}','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	base := write("Start", "")
	write("Linear", "")
	if got := forks(); len(got) != 0 {
		t.Fatalf("a linear task forked: %#v", got)
	}

	// Two machines continued the same checkpoint.
	tip := write("Machine A", "")
	other := write("Machine B", base)
	got := forks()
	if len(got) != 1 || got[0].Ref() != "task:task-a" || len(got[0].Heads) != 2 || got[0].Proposed().CheckpointID != other || got[0].Base != base {
		t.Fatalf("fork: %#v", got)
	}

	// Resolving continues from the accepted head and rejects the other.
	continuation := write("Machine A continued", tip)
	if err := Insert(ctx, db, Resolution{ProjectID: "p", ID: "r1", Family: FamilyCheckpoint, Subject: "task-a", AcceptedHead: tip, RejectedHeads: []string{other}, Continuation: continuation, Resolver: "owner", Authority: "user_reported", RecordedAt: time.Now(), SchemaVersion: SchemaVersion}); err != nil {
		t.Fatal(err)
	}
	if got := forks(); len(got) != 0 {
		t.Fatalf("resolved fork still reported: %#v", got)
	}

	// A machine that had not synced continues the rejected head: the fork reopens.
	late := write("Machine B continued", other)
	got = forks()
	if len(got) != 1 || len(got[0].Heads) != 2 || got[0].Proposed().CheckpointID != late || got[0].Heads[1].CheckpointID != continuation || got[0].Base != base {
		t.Fatalf("reopened fork: %#v", got)
	}
	if _, err := db.Exec(`DELETE FROM fork_resolutions`); err == nil {
		t.Fatal("fork resolutions are not append-only")
	}
}

func TestResolutionValidation(t *testing.T) {
	valid := Resolution{ID: "r", Family: FamilyCheckpoint, Subject: "task", AcceptedHead: "a", RejectedHeads: []string{"c", "b"}, Resolver: "owner", Authority: "user_reported", RecordedAt: time.Now(), SchemaVersion: 1}
	normalized, err := Validate(valid)
	if err != nil || normalized.RejectedHeads[0] != "b" {
		t.Fatalf("valid resolution: %#v %v", normalized, err)
	}
	for name, change := range map[string]func(*Resolution){
		"no rejected":       func(r *Resolution) { r.RejectedHeads = nil },
		"accepted rejected": func(r *Resolution) { r.RejectedHeads = []string{"a"} },
		"duplicate":         func(r *Resolution) { r.RejectedHeads = []string{"b", "b"} },
		"family":            func(r *Resolution) { r.Family = "track_plan" },
		"authority":         func(r *Resolution) { r.Authority = "machine_observed" },
		"resolver":          func(r *Resolution) { r.Resolver = " " },
	} {
		value := valid
		value.RejectedHeads = append([]string{}, valid.RejectedHeads...)
		change(&value)
		if _, err := Validate(value); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	revision := valid
	revision.Family, revision.Subject, revision.Continuation, revision.SchemaVersion = FamilyRevision, "topic_revision:x@2", "", RevisionSchemaVersion
	if _, err := Validate(revision); err != nil {
		t.Fatalf("schema-2 revision resolution: %v", err)
	}
	checkpointV2 := valid
	checkpointV2.SchemaVersion = RevisionSchemaVersion
	if _, err := Validate(checkpointV2); err == nil {
		t.Fatal("schema-2 checkpoint resolution was accepted")
	}
}
