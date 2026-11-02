package recordsync

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

func archiveMatchFixture(t *testing.T, count int) (context.Context, archiveStore, string, []portable.EncodedEnvelope) {
	t.Helper()
	ctx, s := archiveStoreFixture(t)
	activateEmptyArchive(t, ctx, s)
	origin := Hash("batch-source")
	objects := make([]portable.EncodedEnvelope, count)
	for i := range objects {
		objects[i] = archiveObject(t, fmt.Sprintf("record-%d", i), fmt.Sprintf(`{"n":%d}`, i))
	}
	archiveTransaction(t, s, func(tx *sql.Tx) error {
		_, err := s.putWithin(ctx, tx, "ready", origin, objects)
		return err
	})
	return ctx, s, origin, objects
}

func TestArchiveBatchMatchPreservesExactRowsAndProvenance(t *testing.T) {
	ctx, s, origin, objects := archiveMatchFixture(t, 65)
	archiveTransaction(t, s, func(tx *sql.Tx) error {
		ctx, work := syncwork.WithWorkMetrics(ctx)
		before, err := s.stampWithin(ctx, tx, "ready")
		if err != nil {
			return err
		}
		var changes int64
		if err := tx.QueryRowContext(ctx, `SELECT total_changes()`).Scan(&changes); err != nil {
			return err
		}
		if added, err := s.putValidatedWithin(ctx, tx, "ready", origin, objects); err != nil || added != 0 {
			t.Fatalf("unchanged batch: %d %v", added, err)
		}
		after, err := s.stampWithin(ctx, tx, "ready")
		var current int64
		if err := tx.QueryRowContext(ctx, `SELECT total_changes()`).Scan(&current); err != nil {
			return err
		}
		counts := work.Counts()
		if err != nil || before != after || changes != current || counts["archive_match_shortcuts"] != 1 || counts["archive_match_queries"] != 3 || counts["archive_match_records"] != 65 || counts["archive_verified_bytes_reused"] != 65 {
			t.Fatalf("match changed state or skipped comparisons: %v %+v", err, counts)
		}
		// A new target origin must use the original writer, even when every
		// envelope and its original provenance were already present.
		if added, err := s.putValidatedWithin(ctx, tx, "ready", Hash("second-source"), objects); err != nil || added != 0 {
			t.Fatalf("new provenance: %d %v", added, err)
		}
		after, err = s.stampWithin(ctx, tx, "ready")
		if err != nil || after.Records != before.Records || after.Origins != before.Origins+65 || work.Counts()["archive_match_shortcuts"] != 1 {
			t.Fatalf("provenance missing: %+v %v", after, err)
		}
		// Duplicates retain the original per-record behavior.
		duplicate := append(append([]portable.EncodedEnvelope(nil), objects...), objects[0])
		if added, err := s.putValidatedWithin(ctx, tx, "ready", origin, duplicate); err != nil || added != 0 || work.Counts()["archive_match_shortcuts"] != 1 {
			t.Fatalf("duplicate changed behavior: %d %v", added, err)
		}
		return nil
	})
}

func TestArchiveBatchMatchRejectsDamageWithoutRepair(t *testing.T) {
	for _, mode := range []string{"metadata", "bytes", "initial_origin"} {
		t.Run(mode, func(t *testing.T) {
			ctx, s, origin, objects := archiveMatchFixture(t, 2)
			tx, err := s.DB.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			var statements []string
			switch mode {
			case "metadata":
				statements = []string{`DROP TRIGGER history_archive_envelope_immutable_update`, `UPDATE history_archive_envelopes SET record_id='damaged'`}
			case "bytes":
				statements = []string{`DROP TRIGGER history_archive_envelope_immutable_update`, `UPDATE history_archive_envelopes SET envelope_json=CAST('{}' AS BLOB)`}
			case "initial_origin":
				statements = []string{`PRAGMA defer_foreign_keys=ON`, `DROP TRIGGER history_archive_origin_immutable_delete`, `DELETE FROM history_archive_origins`}
			}
			for _, statement := range statements {
				if _, err := tx.ExecContext(ctx, statement); err != nil {
					t.Fatal(err)
				}
			}
			ctx, work := syncwork.WithWorkMetrics(ctx)
			if added, err := s.putValidatedWithin(ctx, tx, "ready", origin, objects); !errors.Is(err, errArchiveIntegrity) || added != 0 || work.Counts()["archive_match_shortcuts"] != 0 {
				t.Fatalf("damage accepted/repaired: %d %v %+v", added, err, work.Counts())
			}
		})
	}
}

func TestArchiveBatchMatchPreservesSequentialTriggerEffects(t *testing.T) {
	ctx, s, origin, objects := archiveMatchFixture(t, 2)
	newObject := archiveObject(t, "new", `{}`)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, statement := range []string{
		`DROP TRIGGER history_archive_envelope_immutable_update`,
		`CREATE TRIGGER fixture_archive_effect AFTER INSERT ON history_archive_envelopes WHEN new.record_id='new' BEGIN UPDATE history_archive_envelopes SET record_id='changed-by-trigger' WHERE record_id='record-1'; END`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	ctx, work := syncwork.WithWorkMetrics(ctx)
	if _, err := s.putValidatedWithin(ctx, tx, "ready", origin, []portable.EncodedEnvelope{objects[0], newObject, objects[1]}); !errors.Is(err, errArchiveIntegrity) || work.Counts()["archive_match_shortcuts"] != 0 {
		t.Fatalf("prefetched comparison hid a later trigger write: %v %+v", err, work.Counts())
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM history_archive_envelopes WHERE project_id=? AND record_id IN ('record-0','record-1')`, s.ProjectID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("rollback damaged old rows: %d %v", count, err)
	}
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM history_archive_envelopes WHERE project_id=? AND record_id='new'`, s.ProjectID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rollback retained new row: %d %v", count, err)
	}
}

func TestArchiveBatchMatchRefusesUnknownReaders(t *testing.T) {
	ctx, s, origin, objects := archiveMatchFixture(t, 2)
	for _, statement := range []string{
		`CREATE VIEW fixture_archive_reader AS SELECT random()`,
		`CREATE TEMP VIEW history_archive_origins AS SELECT * FROM main.history_archive_origins`,
	} {
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
		operation, work := syncwork.WithWorkMetrics(ctx)
		if added, err := s.putValidatedWithin(operation, tx, "ready", origin, objects); err != nil || added != 0 || work.Counts()["archive_match_shortcuts"] != 0 || work.Counts()["archive_verified_bytes_reused"] != 2 {
			t.Fatalf("uncertain reader skipped ordinary path: %d %v %+v", added, err, work.Counts())
		}
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
	}
}
