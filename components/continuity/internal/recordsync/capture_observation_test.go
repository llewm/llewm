//go:build linux

package recordsync

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/decision"
	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

func TestCaptureObservationGuardsAndRecovery(t *testing.T) {
	for _, mode := range []string{"local", "canonical-change", "trigger-change", "damaged-cursor", "missing-cursor", "archive-change", "foreign", "interrupted", "commit-failure"} {
		t.Run(mode, func(t *testing.T) {
			ctx, db, project := indexedTestProject(t, "observation")
			a, err := OpenArchive(t.TempDir(), lineage)
			if err != nil {
				t.Fatal(err)
			}
			appendDecision := func(id string) {
				t.Helper()
				if _, err := (decision.Service{DB: db}).Append(ctx, decision.Record{ProjectID: project, ID: id, Title: id, Rationale: "Observation", Disposition: "accepted", Actor: "test", ActorType: "human", Authority: "caller_asserted"}); err != nil {
					t.Fatal(err)
				}
			}
			appendDecision("captured")
			export, err := portable.EncodeMemoryExport(ctx, db, project)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "foreign" {
				for _, object := range export.Envelopes {
					if _, err := a.Put(object, "foreign"); err != nil {
						t.Fatal(err)
					}
				}
			}
			for i := 0; i < 4; i++ {
				if _, err := a.CaptureProject(ctx, db, project); err != nil {
					t.Fatal(err)
				}
			}
			consumer, err := a.captureRoute()
			if err != nil {
				t.Fatal(err)
			}
			cursor := func() string {
				t.Helper()
				var state string
				if err := db.QueryRow(`SELECT state FROM history_sync_cursors WHERE project_id=? AND consumer=?`, project, consumer).Scan(&state); err != nil {
					t.Fatal(err)
				}
				return state
			}
			before := cursor()
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := db.Exec(query, args...); err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "canonical-change":
				appendDecision("uncaptured")
			case "trigger-change":
				exec(`CREATE TRIGGER observation_changes_canonical AFTER INSERT ON history_index_publications BEGIN UPDATE projects SET name='changed' WHERE id=new.project_id; END`)
			case "damaged-cursor":
				exec(`UPDATE history_sync_cursors SET checksum='damaged' WHERE project_id=?`, project)
			case "missing-cursor":
				exec(`DELETE FROM history_sync_cursors WHERE project_id=?`, project)
			case "archive-change":
				if err := os.WriteFile(filepath.Join(a.Directory, "unexpected"), []byte("unaccounted"), 0600); err != nil {
					t.Fatal(err)
				}
			case "interrupted":
				exec(`CREATE TRIGGER fail_observation BEFORE INSERT ON history_index_publications BEGIN SELECT RAISE(ABORT,'interrupted'); END`)
			case "commit-failure":
				exec(`CREATE TABLE observation_failure(project_id TEXT REFERENCES projects(id) DEFERRABLE INITIALLY DEFERRED)`)
				exec(`CREATE TRIGGER fail_observation AFTER INSERT ON history_index_publications BEGIN INSERT INTO observation_failure VALUES('missing'); END`)
			}
			block, err := EncodeBlock(lineage, export.Envelopes)
			if err != nil {
				t.Fatal(err)
			}
			index := Index{DB: db, ProjectID: project, Lineage: lineage}
			measured, metrics := syncwork.WithWorkMetrics(ctx)
			_, err = index.ObservePublication(measured, a, "git:destination", block)
			if mode == "interrupted" || mode == "commit-failure" {
				if err == nil || cursor() != before {
					t.Fatalf("interrupted observation advanced receipt: %v", err)
				}
				exec(`DROP TRIGGER fail_observation`)
				if _, err := index.ObservePublication(ctx, a, "git:destination", block); err != nil {
					t.Fatal(err)
				}
				if cursor() != before {
					t.Fatal("retry blessed files from interrupted transaction")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if mode == "local" {
				stamp, err := a.ChangeStamp(ctx)
				if err != nil || cursor() == before || cursor() != stamp || metrics.Counts()["capture_observation_receipts"] != 1 {
					t.Fatalf("local provenance not acknowledged: %v %v", err, metrics.Counts())
				}
				if reused, err := a.CaptureProject(ctx, db, project); err != nil || !reused {
					t.Fatalf("capture did not reuse extended receipt: %t %v", reused, err)
				}
				measured, metrics = syncwork.WithWorkMetrics(ctx)
				if _, err := index.ObservePublication(measured, a, "git:destination", block); err != nil || metrics.Counts()["archive_metadata_entries_visited"] != 0 {
					t.Fatalf("unchanged observation walked metadata: %v %v", err, metrics.Counts())
				}
			} else if metrics.Counts()["capture_observation_receipts"] != 0 {
				t.Fatalf("uncertain observation acknowledged: %v", metrics.Counts())
			} else if mode != "missing-cursor" && cursor() != before {
				t.Fatal("uncertain observation advanced archive state")
			}
		})
	}
}
