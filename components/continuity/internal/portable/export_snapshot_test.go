package portable

import (
	"bytes"
	"database/sql"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/syncwork"
	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
)

func TestExportRetentionSnapshotGuard(t *testing.T) {
	for _, mode := range []string{"unchanged", "committed", "rolled_back", "unknown_version"} {
		t.Run(mode, func(t *testing.T) {
			ctx, db, project := operationalProject(t, "export-snapshot")
			if _, err := (Service{DB: db}).Initialize(ctx, project, "lineage:abababababababababababababababab"); err != nil {
				t.Fatal(err)
			}
			appendRecord := func(id string) {
				t.Helper()
				if _, err := (worknarrative.Service{DB: db}).Append(ctx, worknarrative.Record{ID: id, ProjectID: project, Title: id, Trigger: "manual", Creator: "test", Claims: []worknarrative.Claim{{Kind: "outcome", Text: "Observation", Authority: "caller_asserted"}}}); err != nil {
					t.Fatal(err)
				}
			}
			appendRecord("initial")
			conn, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			var version int64
			if err := conn.QueryRowContext(ctx, `PRAGMA data_version`).Scan(&version); err != nil {
				t.Fatal(err)
			}
			read, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
			if err != nil {
				t.Fatal(err)
			}
			measured, work := syncwork.WithWorkMetrics(ctx)
			snapshot, err := loadMemoryExportSnapshot(measured, read, project)
			if err != nil {
				read.Rollback()
				t.Fatal(err)
			}
			if err := read.Rollback(); err != nil {
				t.Fatal(err)
			}
			// The pinned connection forces mutations onto another connection.
			switch mode {
			case "committed":
				appendRecord("concurrent")
			case "rolled_back":
				writer, err := db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := writer.ExecContext(ctx, `UPDATE projects SET name='discarded' WHERE id=?`, project); err != nil {
					writer.Rollback()
					t.Fatal(err)
				}
				if err := writer.Rollback(); err != nil {
					t.Fatal(err)
				}
			case "unknown_version":
				version = 0
			}
			export, err := retainMemoryExportSnapshot(measured, conn, project, version, snapshot)
			if err != nil {
				t.Fatal(err)
			}
			wantLoads, wantReuse, wantFallback := int64(1), int64(1), int64(0)
			if mode == "committed" || mode == "unknown_version" {
				wantLoads, wantReuse, wantFallback = 2, 0, 1
			}
			counts := work.Counts()
			if counts["operational_full_loads"] != wantLoads || counts["export_writer_snapshot_reuses"] != wantReuse || counts["fallback_export_writer_retry"] != wantFallback {
				t.Fatalf("unexpected export work: %v", counts)
			}
			if mode == "committed" && len(export.Envelopes) != len(snapshot.specs)+1 {
				t.Fatal("concurrent record was lost")
			}
			fresh, err := EncodeMemoryExport(ctx, db, project)
			if err != nil {
				t.Fatal(err)
			}
			if len(export.Envelopes) != len(fresh.Envelopes) || export.Identity != fresh.Identity || export.DatabaseSchema != fresh.DatabaseSchema {
				t.Fatal("export snapshot differs from full export")
			}
			for i, object := range fresh.Envelopes {
				if !bytes.Equal(export.Envelopes[i].Bytes, object.Bytes) {
					t.Fatal("export bytes differ from full export")
				}
			}
		})
	}
}
