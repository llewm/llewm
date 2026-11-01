package portable

import (
	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/decision"
	"path/filepath"
	"testing"
)

func TestMemoryDeltaRetentionRejectsSideEffectsAndSuppressedWrites(t *testing.T) {
	for _, mode := range []string{"recovery", "foreign", "extra", "ignored", "forged journal", "commit"} {
		t.Run(mode, func(t *testing.T) {
			ctx, db, project := operationalProject(t, "retention-fence")
			if _, err := (Service{DB: db}).Initialize(ctx, project, "lineage:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err != nil {
				t.Fatal(err)
			}
			foreign, err := (activity.Store{DB: db}).EnsureProject(ctx, filepath.Join(t.TempDir(), "foreign"), "foreign")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`CREATE TABLE fixture_export_retention_control(owner TEXT,target TEXT,enabled INTEGER)`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO fixture_export_retention_control VALUES(?,?,1)`, project, foreign); err != nil {
				t.Fatal(err)
			}
			if mode == "commit" {
				db.SetMaxOpenConns(1)
				if _, err := db.Exec(`CREATE TABLE fixture_export_commit_parent(id INTEGER PRIMARY KEY); CREATE TABLE fixture_export_commit_child(id INTEGER PRIMARY KEY,parent_id INTEGER REFERENCES fixture_export_commit_parent(id) DEFERRABLE INITIALLY DEFERRED)`); err != nil {
					t.Fatal(err)
				}
			}
			timing, body := "AFTER", `INSERT INTO redaction_recoveries(project_id,id,record_type,record_id,record_revision,original_sha256,derived_sha256,original_content_sha256,derived_content_sha256,envelope_json) VALUES(new.project_id,'unexpected-proof','work_narrative','unexpected',0,'original','derived','original-content','derived-content','{}');`
			switch mode {
			case "foreign":
				body = `INSERT INTO portable_envelopes(project_id,record_type,record_id,record_revision,envelope_json,origin) VALUES((SELECT target FROM fixture_export_retention_control),new.record_type,new.record_id,new.record_revision,new.envelope_json,'import');`
			case "extra":
				body = `INSERT INTO portable_envelopes(project_id,record_type,record_id,record_revision,envelope_json,origin) VALUES(new.project_id,new.record_type,'unselected',new.record_revision,new.envelope_json,'import');`
			case "ignored":
				timing, body = "BEFORE", `SELECT RAISE(IGNORE);`
			case "commit":
				body = `INSERT INTO fixture_export_commit_child(id,parent_id) VALUES(1,999);`
			case "forged journal":
				timing, body = "BEFORE", `INSERT INTO history_sync_changes(project_id,table_name,row_key,operation) VALUES(new.project_id,'portable_envelopes',json_array(new.project_id,new.record_type,new.record_id,new.record_revision),'insert'); SELECT RAISE(IGNORE);`
			}
			// Install before the receipt so this reaches the writer, rather than merely
			// invalidating the receipt through its schema-version fence.
			if _, err := db.Exec(`CREATE TRIGGER fixture_export_retention_sideeffect ` + timing + ` INSERT ON portable_envelopes WHEN new.project_id=(SELECT owner FROM fixture_export_retention_control) AND new.record_id='arrival' AND (SELECT enabled FROM fixture_export_retention_control)=1 BEGIN ` + body + ` END`); err != nil {
				t.Fatal(err)
			}
			if _, err := EncodeMemoryExport(ctx, db, project); err != nil {
				t.Fatal(err)
			}
			baseline, _, _, err := database.ReadSyncChanges(ctx, db, project, "fixture", "archive", 4096)
			if err != nil {
				t.Fatal(err)
			}
			if ok, err := database.RememberSyncCursor(ctx, db, baseline, "archive"); err != nil || !ok {
				t.Fatalf("baseline: %t %v", ok, err)
			}
			if _, err := (decision.Service{DB: db}).Append(ctx, decision.Record{ProjectID: project, ID: "arrival", Title: "Arrival", Rationale: "Fixture", Disposition: "accepted", Actor: "owner", ActorType: "human", Authority: "caller_asserted"}); err != nil {
				t.Fatal(err)
			}
			before, changes, reason, err := database.ReadSyncChanges(ctx, db, project, "fixture", "archive", 4096)
			if err != nil || reason != "" {
				t.Fatalf("input: %s %v", reason, err)
			}
			if _, _, used, err := EncodeMemoryDelta(ctx, db, before, changes); err == nil || used {
				t.Fatalf("unexpected retention side effect accepted: used=%t %v", used, err)
			}
			for _, query := range []string{`SELECT count(*) FROM portable_envelopes`, `SELECT count(*) FROM redaction_recoveries`} {
				var count int
				if err := db.QueryRow(query).Scan(&count); err != nil || count != 0 {
					t.Fatalf("partial writes survived: %d %v", count, err)
				}
			}
			if mode == "commit" {
				var count int
				if err := db.QueryRow(`SELECT count(*) FROM fixture_export_commit_child`).Scan(&count); err != nil || count != 0 {
					t.Fatalf("failed native commit leaked: %d %v", count, err)
				}
			}
			after, pending, reason, err := database.ReadSyncChanges(ctx, db, project, "fixture", "archive", 4096)
			if err != nil || reason != "" || after.Generation != before.Generation || after.Sequence != before.Sequence || len(pending) != len(changes) {
				t.Fatalf("rejected retention advanced state: %+v %s %v", after, reason, err)
			}
			if _, err := db.Exec(`UPDATE fixture_export_retention_control SET enabled=0`); err != nil {
				t.Fatal(err)
			}
			export, receipt, used, err := EncodeMemoryDelta(ctx, db, before, changes)
			if err != nil || !used || len(export.Envelopes) != 1 || receipt.Sequence <= before.Sequence {
				t.Fatalf("clean retry: %+v %t %v", receipt, used, err)
			}
			if ok, err := database.RememberSyncCursor(ctx, db, receipt, "archive"); err != nil || !ok {
				t.Fatalf("ack: %t %v", ok, err)
			}
			_, pending, reason, err = database.ReadSyncChanges(ctx, db, project, "fixture", "archive", 4096)
			if err != nil || reason != "" || len(pending) != 0 {
				t.Fatalf("retention not consumed: %d %s %v", len(pending), reason, err)
			}
		})
	}
}
