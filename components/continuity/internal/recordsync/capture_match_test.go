package recordsync

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

func TestCaptureMatchPreservesForeignOriginsAndNoWrites(t *testing.T) {
	ctx, s, _, objects := archiveMatchFixture(t, 65)
	a := Archive{DB: s.DB, ProjectID: s.ProjectID, Lineage: s.Lineage}
	archiveTransaction(t, s, func(tx *sql.Tx) error {
		operation, work := syncwork.WithWorkMetrics(ctx)
		before, err := s.stampWithin(ctx, tx, "ready")
		if err != nil {
			return err
		}
		var changes int64
		if err := tx.QueryRow(`SELECT total_changes()`).Scan(&changes); err != nil {
			return err
		}
		// Exported parsed metadata is untrusted; exact validated wire bytes govern.
		objects[0].Envelope.RecordID = "forged"
		if err := a.captureLocalWithin(operation, tx, objects); err != nil {
			return err
		}
		after, err := s.stampWithin(ctx, tx, "ready")
		var current, local int64
		if err != nil {
			return err
		}
		if err := tx.QueryRow(`SELECT total_changes()`).Scan(&current); err != nil {
			return err
		}
		if err := tx.QueryRow(`SELECT count(*) FROM history_archive_origins WHERE origin_hash=?`, Hash("local")).Scan(&local); err != nil {
			return err
		}
		if before != after || changes != current || local != 0 || work.Counts()["capture_match_records"] != 64 || work.Counts()["archive_match_queries"] != 2 {
			t.Fatalf("capture changed data/provenance/work: %+v", work.Counts())
		}
		return nil
	})
}
func TestCaptureMatchStopsBeforeWritesAndPreservesErrorOrder(t *testing.T) {
	for _, mode := range []string{"later_trigger_damage", "earlier_statement_error", "malformed_first_origin", "unknown_view", "temp_shadow", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			ctx, s, _, objects := archiveMatchFixture(t, 65)
			a := Archive{DB: s.DB, ProjectID: s.ProjectID, Lineage: s.Lineage}
			arrival := archiveObject(t, "arrival", `{}`)
			tx, err := s.DB.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			var statements []string
			switch mode {
			case "later_trigger_damage":
				objects = append(append(append([]portable.EncodedEnvelope(nil), objects[:32]...), arrival), objects[32:]...)
				statements = []string{`DROP TRIGGER history_archive_envelope_immutable_update`, fmt.Sprintf(`CREATE TRIGGER capture_damage AFTER INSERT ON history_archive_envelopes WHEN new.record_id='arrival' BEGIN UPDATE history_archive_envelopes SET envelope_json=CAST('{}' AS BLOB) WHERE envelope_hash='%s'; END`, objects[len(objects)-1].Envelope.EnvelopeSHA256)}
			case "earlier_statement_error":
				objects = append([]portable.EncodedEnvelope{arrival}, objects...)
				objects[10].Bytes = append(objects[10].Bytes, ' ')
				statements = []string{`CREATE TRIGGER capture_reject BEFORE INSERT ON history_archive_envelopes WHEN new.record_id='arrival' BEGIN SELECT RAISE(ABORT,'capture first error'); END`}
			case "malformed_first_origin":
				statements = []string{`PRAGMA ignore_check_constraints=ON`, fmt.Sprintf(`INSERT INTO history_archive_origins(project_id,envelope_hash,origin_hash) VALUES('%s','%s','!')`, s.ProjectID, objects[0].Envelope.EnvelopeSHA256)}
			case "unknown_view":
				statements = []string{`CREATE VIEW capture_unknown_reader AS SELECT random() AS n`}
			case "temp_shadow":
				statements = []string{`CREATE TEMP VIEW history_archive_origins AS SELECT * FROM main.history_archive_origins`}
			case "duplicate":
				objects = append([]portable.EncodedEnvelope{objects[0]}, objects...)
			}
			for _, statement := range statements {
				if _, err := tx.Exec(statement); err != nil {
					t.Fatal(err)
				}
			}
			operation, work := syncwork.WithWorkMetrics(ctx)
			// Run both implementations against the same starting database; all writes
			// and trigger effects are discarded before comparing the second result.
			if _, err := tx.Exec(`SAVEPOINT reference_capture`); err != nil {
				t.Fatal(err)
			}
			want := a.referenceCaptureLocalWithin(ctx, tx, objects)
			if _, err := tx.Exec(`ROLLBACK TO reference_capture`); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(`RELEASE reference_capture`); err != nil {
				t.Fatal(err)
			}
			got := a.captureLocalWithin(operation, tx, objects)
			if (got == nil) != (want == nil) || got != nil && got.Error() != want.Error() {
				t.Fatalf("error ordering differs: %v vs %v", got, want)
			}
			switch mode {
			case "later_trigger_damage":
				if !errors.Is(got, errArchiveIntegrity) || work.Counts()["capture_match_records"] != 32 {
					t.Fatalf("later write escaped sequential checks: %v %+v", got, work.Counts())
				}
			case "earlier_statement_error":
				if got == nil || !strings.Contains(got.Error(), "capture first error") {
					t.Fatalf("later malformed frame hid first SQL error: %v", got)
				}
			case "malformed_first_origin":
				if got == nil {
					t.Fatal("invalid selected origin accepted")
				}
			case "unknown_view", "temp_shadow", "duplicate":
				if got != nil || work.Counts()["capture_match_records"] != 0 {
					t.Fatalf("uncertainty optimized: %v %+v", got, work.Counts())
				}
			}
		})
	}
}
func TestSingleArchiveObjectSkipsBatchMatcher(t *testing.T) {
	ctx, s, origin, objects := archiveMatchFixture(t, 1)
	archiveTransaction(t, s, func(tx *sql.Tx) error {
		ctx, work := syncwork.WithWorkMetrics(ctx)
		_, err := s.putValidatedWithin(ctx, tx, "ready", origin, objects)
		if work.Counts()["archive_match_queries"] != 0 || work.Counts()["archive_match_shortcuts"] != 0 {
			t.Fatal("single record used batch machinery")
		}
		return err
	})
}

func TestCaptureMatchMatchesSequentialStateAcrossBatchBoundaries(t *testing.T) {
	ctx, s, _, all := archiveMatchFixture(t, 65)
	a := Archive{DB: s.DB, ProjectID: s.ProjectID, Lineage: s.Lineage}
	arrival := archiveObject(t, "arrival", `{}`)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	state := func() string {
		stamp, err := s.stampWithin(ctx, tx, "ready")
		if err != nil {
			t.Fatal(err)
		}
		parts := []string{fmt.Sprintf("%+v", stamp)}
		rows, err := tx.Query(`SELECT envelope_hash,record_id,initial_origin_hash,hex(envelope_json) FROM history_archive_envelopes WHERE project_id=? ORDER BY envelope_hash`, s.ProjectID)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var hash, id, origin, body string
			if err := rows.Scan(&hash, &id, &origin, &body); err != nil {
				t.Fatal(err)
			}
			parts = append(parts, hash+":"+id+":"+origin+":"+body)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		rows, err = tx.Query(`SELECT envelope_hash,origin_hash FROM history_archive_origins WHERE project_id=? ORDER BY envelope_hash,origin_hash`, s.ProjectID)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var hash, origin string
			if err := rows.Scan(&hash, &origin); err != nil {
				t.Fatal(err)
			}
			parts = append(parts, hash+":"+origin)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		return strings.Join(parts, "\n")
	}
	for _, n := range []int{0, 1, 31, 32, 33, 64, 65} {
		for _, add := range []bool{false, true} {
			t.Run(fmt.Sprintf("n%d-arrival%v", n, add), func(t *testing.T) {
				objects := append([]portable.EncodedEnvelope(nil), all[:n]...)
				if add {
					objects = append(objects, arrival)
				}
				if _, err := tx.Exec(`SAVEPOINT compare_capture`); err != nil {
					t.Fatal(err)
				}
				if err := a.referenceCaptureLocalWithin(ctx, tx, objects); err != nil {
					t.Fatal(err)
				}
				want := state()
				if _, err := tx.Exec(`ROLLBACK TO compare_capture`); err != nil {
					t.Fatal(err)
				}
				if err := a.captureLocalWithin(ctx, tx, objects); err != nil {
					t.Fatal(err)
				}
				got := state()
				if got != want {
					t.Fatal("batching changed exact archive/provenance/stamp state")
				}
				if _, err := tx.Exec(`ROLLBACK TO compare_capture`); err != nil {
					t.Fatal(err)
				}
				if _, err := tx.Exec(`RELEASE compare_capture`); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
