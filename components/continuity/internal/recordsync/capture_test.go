//go:build linux

package recordsync

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/attribution"
	"github.com/llewm/llewm/components/continuity/internal/decision"
	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
)

func TestCaptureProjectReuseAndRepair(t *testing.T) {
	ctx, db, project := indexedTestProject(t, "capture")
	a, err := OpenArchive(t.TempDir(), lineage)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (decision.Service{DB: db}).Append(ctx, decision.Record{ProjectID: project, ID: "decision", Title: "Decision", Rationale: "Observation", Disposition: "accepted", Actor: "test", ActorType: "human", Authority: "caller_asserted"}); err != nil {
		t.Fatal(err)
	}
	warm := func() {
		t.Helper()
		for i := 0; i < 4; i++ {
			reused, err := a.CaptureProject(ctx, db, project)
			if err != nil {
				t.Fatal(err)
			}
			if reused {
				return
			}
		}
		t.Fatal("capture never reused")
	}
	warm()
	at := time.Now()
	for _, item := range []struct{ id, parent string }{{"root", ""}, {"child", "root"}} {
		if _, err := (attribution.Service{DB: db}).AppendInvocation(ctx, attribution.Invocation{ID: item.id, ProjectID: project, ParentInvocationID: item.parent, ActorType: "agent", IdentitySource: "caller_asserted", OccurredAt: at, ReceivedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	if reused, err := a.CaptureProject(ctx, db, project); err != nil || !reused {
		t.Fatalf("raw attribution invalidated capture: %t %v", reused, err)
	}
	if _, err := (worknarrative.Service{DB: db}).Append(ctx, worknarrative.Record{ID: "work", ProjectID: project, Title: "Work", Trigger: "manual", Creator: "test", CreatorInvocationID: "child", Claims: []worknarrative.Claim{{Kind: "outcome", Text: "Observation", Authority: "caller_asserted"}}}); err != nil {
		t.Fatal(err)
	}
	if reused, err := a.CaptureProject(ctx, db, project); err != nil || reused {
		t.Fatalf("new dependency skipped: %t %v", reused, err)
	}
	warm()
	export, err := portable.EncodeMemoryExport(ctx, db, project)
	if err != nil {
		t.Fatal(err)
	}
	if len(export.Envelopes) != 4 {
		t.Fatalf("missing attribution ancestors: %d", len(export.Envelopes))
	}
	path := filepath.Join(a.Directory, portable.MemoryRecordPath(export.Envelopes[0].Envelope))
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if reused, err := a.CaptureProject(ctx, db, project); err != nil || reused {
		t.Fatalf("missing archive file not repaired: %t %v", reused, err)
	}
	warm()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	body := portable.MemoryFileBytes(export.Envelopes[0])
	body[len(body)-2] ^= 1
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if reused, err := a.CaptureProject(ctx, db, project); err == nil || reused {
		t.Fatalf("corrupt archive file reused: %t %v", reused, err)
	}
}

func TestCaptureProjectConsumesOnlyDerivedChanges(t *testing.T) {
	ctx, db, project := indexedTestProject(t, "cursor")
	a, err := OpenArchive(t.TempDir(), lineage)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = (decision.Service{DB: db}).Append(ctx, decision.Record{ProjectID: project, ID: "decision", Title: "Decision", Rationale: "Observation", Disposition: "accepted", Actor: "test", ActorType: "human", Authority: "caller_asserted"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if _, err = a.CaptureProject(ctx, db, project); err != nil {
			t.Fatal(err)
		}
	}
	// Index bookkeeping changes the generation but cannot change export bytes.
	if _, err = db.Exec(`INSERT INTO history_index_blocks(project_id,block_hash,index_version) VALUES(?,'metadata-only',1)`, project); err != nil {
		t.Fatal(err)
	}
	if reused, err := a.CaptureProject(ctx, db, project); err != nil || !reused {
		t.Fatalf("metadata delta: %t %v", reused, err)
	}
	var pending int
	if err = db.QueryRow(`SELECT COUNT(*) FROM history_sync_changes c JOIN history_sync_cursors p ON c.project_id=p.project_id WHERE c.sequence>p.sequence AND c.project_id=?`, project).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("cursor failed to advance: %d %v", pending, err)
	}
	// A qualified canonical arrival publishes its delta and acknowledges both
	// that publication and its own retention writes without a second full export.
	if _, err = (decision.Service{DB: db}).Append(ctx, decision.Record{ProjectID: project, ID: "second", Title: "Second", Rationale: "Arrival", Disposition: "accepted", Actor: "test", ActorType: "human", Authority: "caller_asserted"}); err != nil {
		t.Fatal(err)
	}
	if reused, err := a.CaptureProject(ctx, db, project); err != nil || reused {
		t.Fatalf("canonical change: %t %v", reused, err)
	}
	reference, err := portable.EncodeMemoryExport(ctx, db, project)
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range reference.Envelopes {
		body, err := os.ReadFile(filepath.Join(a.Directory, portable.MemoryRecordPath(object.Envelope)))
		if err != nil || string(body) != string(portable.MemoryFileBytes(object)) {
			t.Fatalf("reference differs: %v", err)
		}
	}
	if reused, err := a.CaptureProject(ctx, db, project); err != nil || !reused {
		t.Fatalf("canonical delta did not acknowledge retention/publication: %t %v", reused, err)
	}
	if _, err = db.Exec(`DELETE FROM history_sync_changes WHERE sequence=(SELECT MIN(sequence) FROM history_sync_changes)`); err != nil {
		t.Fatal(err)
	}
	if reused, err := a.CaptureProject(ctx, db, project); err != nil || reused {
		t.Fatalf("journal damage: %t %v", reused, err)
	}
	if _, err = db.Exec(`UPDATE history_sync_cursors SET sequence=sequence+1`); err != nil {
		t.Fatal(err)
	}
	if reused, err := a.CaptureProject(ctx, db, project); err != nil || reused {
		t.Fatalf("cursor damage: %t %v", reused, err)
	}
}
