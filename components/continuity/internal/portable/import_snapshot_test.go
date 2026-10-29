package portable

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
)

func TestAlreadyPresentImportSharesValidatedSnapshot(t *testing.T) {
	ctx, db, project := operationalProject(t, "present-snapshot")
	if _, err := (Service{DB: db}).Initialize(ctx, project, "lineage:abababababababababababababababab"); err != nil {
		t.Fatal(err)
	}
	if _, err := (worknarrative.Service{DB: db}).Append(ctx, worknarrative.Record{ID: "snapshot", ProjectID: project, Title: "Snapshot", Trigger: "manual", Creator: "test", Claims: []worknarrative.Claim{{Kind: "outcome", Text: "Observation", Authority: "caller_asserted"}}}); err != nil {
		t.Fatal(err)
	}
	export, err := EncodeMemoryExport(ctx, db, project)
	if err != nil {
		t.Fatal(err)
	}
	var specs []EnvelopeSpec
	for _, envelope := range export.Envelopes {
		spec, err := DecodeMemoryEnvelope(envelope.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		specs = append(specs, spec)
	}
	called := false
	plan, receipt, err := ImportMemoryRecordsWithin(ctx, db, project, specs, time.Now(), ImportMemoryHooks{
		Appended: func(context.Context, *sql.Tx) error { t.Fatal("already-present import appended records"); return nil },
		Present: func(ctx context.Context, tx *sql.Tx, snapshot map[string]EncodedEnvelope) error {
			called = true
			fresh, err := LocalEnvelopesForImport(ctx, tx, project)
			if err != nil {
				return err
			}
			if len(fresh) != len(snapshot) {
				t.Fatalf("snapshot size differs: %d/%d", len(snapshot), len(fresh))
			}
			for key, object := range fresh {
				if !bytes.Equal(snapshot[key].Bytes, object.Bytes) {
					t.Fatalf("snapshot differs for %s", key)
				}
			}
			return nil
		},
	})
	if err != nil || !called || !plan.ApplyAllowed || plan.Counts[PlanAppend] != 0 || receipt != nil {
		t.Fatalf("present import: called=%t plan=%+v receipt=%+v err=%v", called, plan, receipt, err)
	}
}

func TestPresentSnapshotRejectsConcurrentCommitAndRollsBackHook(t *testing.T) {
	ctx, db, project := operationalProject(t, "snapshot-change")
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var version int64
	if err := conn.QueryRowContext(ctx, `PRAGMA data_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	// The pinned connection cannot be used by this write. Its data_version
	// must detect another connection's commit before the callback can run.
	if _, err := db.ExecContext(ctx, `UPDATE projects SET name='concurrent' WHERE id=?`, project); err != nil {
		t.Fatal(err)
	}
	called := false
	err = verifyPresentSnapshot(ctx, conn, project, version, nil, func(context.Context, *sql.Tx, map[string]EncodedEnvelope) error { called = true; return nil })
	if !errors.Is(err, ErrImportSnapshotChanged) || called {
		t.Fatalf("stale snapshot used: called=%t err=%v", called, err)
	}
	if err := conn.QueryRowContext(ctx, `PRAGMA data_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	rejected := errors.New("reject proof")
	err = verifyPresentSnapshot(ctx, conn, project, version, nil, func(ctx context.Context, tx *sql.Tx, _ map[string]EncodedEnvelope) error {
		if _, err := tx.ExecContext(ctx, `UPDATE projects SET name='must roll back' WHERE id=?`, project); err != nil {
			return err
		}
		return rejected
	})
	if !errors.Is(err, rejected) {
		t.Fatal(err)
	}
	var name string
	if err := db.QueryRowContext(ctx, `SELECT name FROM projects WHERE id=?`, project).Scan(&name); err != nil || name != "concurrent" {
		t.Fatalf("hook escaped rollback: %q %v", name, err)
	}
}
