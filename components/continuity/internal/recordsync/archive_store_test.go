package recordsync

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

func archiveStoreFixture(t testing.TB) (context.Context, archiveStore) {
	t.Helper()
	ctx, db, project := indexedTestProject(t, "durable-archive")
	s := archiveStore{DB: db, ProjectID: project, Lineage: lineage}
	archiveTransaction(t, s, func(tx *sql.Tx) error { return s.initializeWithin(ctx, tx) })
	return ctx, s
}

func openTestArchive(t testing.TB, db *sql.DB, project string) (Archive, error) {
	t.Helper()
	return OpenArchive(context.Background(), db, project, lineage, filepath.Join(t.TempDir(), "unused-legacy"))
}

func standaloneTestArchive(t testing.TB) (Archive, error) {
	t.Helper()
	_, db, project := indexedTestProject(t, "standalone-archive")
	return openTestArchive(t, db, project)
}

func archiveTransaction(t testing.TB, s archiveStore, fn func(*sql.Tx) error) {
	t.Helper()
	tx, err := s.DB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func archiveObject(t testing.TB, id, payload string) portable.EncodedEnvelope {
	t.Helper()
	object, err := portable.EncodeEnvelope(portable.EnvelopeSpec{LineageID: lineage, RecordType: "decision", RecordSchemaVersion: 1, RecordID: id, RecordRevision: 1, RecordedAt: time.Unix(1234567890, 0), Authority: "caller_asserted", Payload: []byte(payload)})
	if err != nil {
		t.Fatal(err)
	}
	return object
}

// Independent inventory from the source inputs, not from the new store/index.
func archiveSourceInventory(objects []portable.EncodedEnvelope, origins map[string][]string) archiveInventory {
	var inventory archiveInventory
	var hashes, pairs []string
	seen := map[string]bool{}
	for _, object := range objects {
		hash := object.Envelope.EnvelopeSHA256
		if !seen[hash] {
			seen[hash] = true
			hashes = append(hashes, hash+"\x00")
			inventory.Records++
			inventory.Bytes += int64(len(object.Bytes) + 1)
		}
	}
	for hash, values := range origins {
		for _, origin := range values {
			pairs = append(pairs, hash+origin+"\x00")
			inventory.Origins++
		}
	}
	sort.Strings(hashes)
	sort.Strings(pairs)
	inventory.EnvelopesSHA256 = Hash(strings.Join(hashes, ""))
	inventory.OriginsSHA256 = Hash(strings.Join(pairs, ""))
	return inventory
}

func activateEmptyArchive(t testing.TB, ctx context.Context, s archiveStore) {
	t.Helper()
	archiveTransaction(t, s, func(tx *sql.Tx) error {
		return s.activateWithin(ctx, tx, archiveSourceInventory(nil, nil))
	})
}

func TestDurableArchiveStagingForksProvenanceAndIndexIndependence(t *testing.T) {
	ctx, s := archiveStoreFixture(t)
	a, b := archiveObject(t, "fork", `{"version":"a"}`), archiveObject(t, "fork", `{"version":"b"}`)
	foreign, other, local := Hash("git:source"), Hash("folder:other"), Hash("local")
	archiveTransaction(t, s, func(tx *sql.Tx) error {
		if _, err := s.stampWithin(ctx, tx, "ready"); !errors.Is(err, errArchiveNotReady) {
			t.Fatalf("staging accepted as active: %v", err)
		}
		if added, err := s.putWithin(ctx, tx, "staging", foreign, []portable.EncodedEnvelope{a, b, a}); err != nil || added != 2 {
			t.Fatalf("stage forks: %d %v", added, err)
		}
		// Exported parsed fields cannot override the exact bytes being archived.
		tampered := a
		tampered.Envelope.RecordID = "forged-parsed-id"
		if added, err := s.putWithin(ctx, tx, "staging", local, []portable.EncodedEnvelope{tampered}); err != nil || added != 0 {
			t.Fatalf("add provenance: %d %v", added, err)
		}
		return nil
	})
	expected := archiveSourceInventory([]portable.EncodedEnvelope{a, b}, map[string][]string{a.Envelope.EnvelopeSHA256: {foreign, local}, b.Envelope.EnvelopeSHA256: {foreign}})
	archiveTransaction(t, s, func(tx *sql.Tx) error {
		wrong := expected
		wrong.EnvelopesSHA256 = Hash("incomplete-source")
		if err := s.activateWithin(ctx, tx, wrong); err == nil {
			t.Fatal("migration activated different source inventory")
		}
		if _, _, _, err := s.changesWithin(ctx, tx, Hash("consumer"), 10); !errors.Is(err, errArchiveNotReady) {
			t.Fatalf("staging journal exposed to normal consumer: %v", err)
		}
		return s.activateWithin(ctx, tx, expected)
	})
	archiveTransaction(t, s, func(tx *sql.Tx) error {
		for _, object := range []portable.EncodedEnvelope{a, b} {
			stored, err := s.readObjectWithin(ctx, tx, object.Envelope.EnvelopeSHA256)
			if err != nil || !bytes.Equal(stored.Bytes, object.Bytes) || stored.Envelope.RecordID != "fork" {
				t.Fatalf("exact fork lost: %v", err)
			}
		}
		for _, check := range []struct {
			object portable.EncodedEnvelope
			origin string
			relay  bool
			want   bool
		}{{a, other, false, true}, {b, other, false, false}, {b, foreign, false, true}, {b, other, true, true}} {
			allowed, err := s.authorizedWithin(ctx, tx, check.object.Envelope.EnvelopeSHA256, check.origin, check.relay)
			if err != nil || allowed != check.want {
				t.Fatalf("origin policy: got%t want%t %v", allowed, check.want, err)
			}
		}
		before, err := s.stampWithin(ctx, tx, "ready")
		if err != nil {
			return err
		}
		if _, err := s.putWithin(ctx, tx, "ready", foreign, []portable.EncodedEnvelope{a, b}); err != nil {
			return err
		}
		after, err := s.stampWithin(ctx, tx, "ready")
		if err != nil || before != after {
			t.Fatalf("replay changed durable state: %v", err)
		}
		return nil
	})
	block, err := EncodeBlock(lineage, []portable.EncodedEnvelope{a, b})
	if err != nil {
		t.Fatal(err)
	}
	index := Index{DB: s.DB, ProjectID: s.ProjectID, Lineage: lineage}
	if err := index.AddBlock(ctx, block); err != nil {
		t.Fatal(err)
	}
	if err := index.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	archiveTransaction(t, s, func(tx *sql.Tx) error {
		audit, err := s.auditWithin(ctx, tx)
		if err != nil || audit.Inventory != expected {
			t.Fatalf("index deletion lost durable history: %+v %v", audit.Inventory, err)
		}
		return nil
	})
}

func TestDurableArchiveRejectsInvalidBatchAndBorrowedReceipt(t *testing.T) {
	ctx, s := archiveStoreFixture(t)
	activateEmptyArchive(t, ctx, s)
	object := archiveObject(t, "valid", `{}`)
	invalid := object
	invalid.Bytes = append(append([]byte(nil), object.Bytes...), ' ')
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.putWithin(ctx, tx, "ready", Hash("local"), []portable.EncodedEnvelope{object, invalid}); err == nil {
		t.Fatal("invalid batch accepted")
	}
	stamp, err := s.stampWithin(ctx, tx, "ready")
	if err != nil || stamp.Records != 0 || stamp.Origins != 0 || stamp.Changes != 0 {
		t.Fatalf("invalid batch partially staged: %+v %v", stamp, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	consumer := Hash("one-policy")
	archiveTransaction(t, s, func(tx *sql.Tx) error {
		stamp, err := s.stampWithin(ctx, tx, "ready")
		if err != nil {
			return err
		}
		_, err = s.rememberWithin(ctx, tx, consumer, stamp)
		return err
	})
	// Even an otherwise intact receipt/checksum cannot be borrowed under a
	// different consumer key. Endpoints and policies have separate progress.
	if _, err := s.DB.Exec(`INSERT INTO history_archive_cursors SELECT project_id,?,snapshot_json,checksum FROM history_archive_cursors WHERE project_id=? AND consumer=?`, Hash("other-policy"), s.ProjectID, consumer); err != nil {
		t.Fatal(err)
	}
	archiveTransaction(t, s, func(tx *sql.Tx) error {
		_, _, reason, err := s.changesWithin(ctx, tx, Hash("other-policy"), 10)
		if err != nil || reason != "damaged_cursor" {
			t.Fatalf("borrowed progress accepted: %s %v", reason, err)
		}
		return nil
	})
}

func TestDurableArchiveAtomicJournalCursorsAndRestart(t *testing.T) {
	ctx, s := archiveStoreFixture(t)
	activateEmptyArchive(t, ctx, s)
	consumer := Hash("import-policy")
	var empty, changed archiveStamp
	archiveTransaction(t, s, func(tx *sql.Tx) error {
		var err error
		empty, _, _, err = s.changesWithin(ctx, tx, consumer, 10)
		if err != nil {
			return err
		}
		ok, err := s.rememberWithin(ctx, tx, consumer, empty)
		if !ok {
			t.Fatal("could not acknowledge empty verified archive")
		}
		return err
	})
	a, b := archiveObject(t, "a", `{}`), archiveObject(t, "b", `{}`)
	archiveTransaction(t, s, func(tx *sql.Tx) error {
		if _, err := s.putWithin(ctx, tx, "ready", Hash("local"), []portable.EncodedEnvelope{a, b}); err != nil {
			return err
		}
		if ok, err := s.rememberWithin(ctx, tx, consumer, empty); err != nil || ok {
			t.Fatalf("stale snapshot acknowledged later writes: %t %v", ok, err)
		}
		return nil
	})
	// Materialization/acknowledgement can fail after archive ingestion committed.
	// Its receipt is rolled back and must replay the exact journal after restart.
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	changed, delta, reason, err := s.changesWithin(ctx, tx, consumer, 10)
	if err != nil || reason != "" || len(delta) != 4 || changed.Records != 2 || changed.Origins != 2 {
		t.Fatalf("committed ingestion delta: %+v %s %v", delta, reason, err)
	}
	if ok, err := s.rememberWithin(ctx, tx, consumer, changed); err != nil || !ok {
		t.Fatalf("prospective receipt: %t %v", ok, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var sequence int
	var name, path string
	if err := s.DB.QueryRow(`PRAGMA database_list`).Scan(&sequence, &name, &path); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Close(); err != nil {
		t.Fatal(err)
	}
	s.DB, err = database.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	archiveTransaction(t, s, func(tx *sql.Tx) error {
		next, replay, reason, err := s.changesWithin(ctx, tx, consumer, 10)
		if err != nil || reason != "" || next != changed || !reflect.DeepEqual(replay, delta) {
			t.Fatalf("restart did not replay unacknowledged changes: %+v %s %v", replay, reason, err)
		}
		_, _, reason, err = s.changesWithin(ctx, tx, consumer, 1)
		if err != nil || reason != "delta_limit" {
			t.Fatalf("unbounded delta: %s %v", reason, err)
		}
		ok, err := s.rememberWithin(ctx, tx, consumer, next)
		if !ok {
			t.Fatal("fresh archive receipt refused")
		}
		return err
	})
	measured, work := syncwork.WithWorkMetrics(ctx)
	archiveTransaction(t, s, func(tx *sql.Tx) error {
		_, changes, reason, err := s.changesWithin(measured, tx, consumer, 10)
		if err != nil || reason != "" || len(changes) != 0 || work.Counts()["archive_journal_rows_read"] != 0 || work.Counts()["archive_audit_envelopes"] != 0 {
			t.Fatalf("unchanged archive replayed history: %+v %s %v", changes, reason, err)
		}
		return nil
	})
}

func TestDurableArchiveRollbackAndProjectIsolation(t *testing.T) {
	ctx, s := archiveStoreFixture(t)
	activateEmptyArchive(t, ctx, s)
	consumer := Hash("snapshot")
	var before archiveStamp
	archiveTransaction(t, s, func(tx *sql.Tx) error {
		var err error
		before, err = s.stampWithin(ctx, tx, "ready")
		return err
	})
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.putWithin(ctx, tx, "ready", Hash("local"), []portable.EncodedEnvelope{archiveObject(t, "rolled-back", `{}`)}); err != nil {
		t.Fatal(err)
	}
	stamp, err := s.stampWithin(ctx, tx, "ready")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.rememberWithin(ctx, tx, consumer, stamp); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	other, err := (activity.Store{DB: s.DB}).EnsureProject(ctx, filepath.Join(t.TempDir(), "other"), "other")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (portable.Service{DB: s.DB}).Initialize(ctx, other, lineage); err != nil {
		t.Fatal(err)
	}
	second := archiveStore{DB: s.DB, ProjectID: other, Lineage: lineage}
	archiveTransaction(t, second, func(tx *sql.Tx) error { return second.initializeWithin(ctx, tx) })
	activateEmptyArchive(t, ctx, second)
	archiveTransaction(t, second, func(tx *sql.Tx) error {
		_, err := second.putWithin(ctx, tx, "ready", Hash("local"), []portable.EncodedEnvelope{archiveObject(t, "other", `{}`)})
		return err
	})
	archiveTransaction(t, s, func(tx *sql.Tx) error {
		after, err := s.stampWithin(ctx, tx, "ready")
		if err != nil || before != after {
			t.Fatalf("rollback/other project changed stamp: %+v %v", after, err)
		}
		_, _, reason, err := s.changesWithin(ctx, tx, consumer, 10)
		if err != nil || reason != "missing_cursor" {
			t.Fatalf("rolled-back cursor survived: %s %v", reason, err)
		}
		return nil
	})
}

func TestDurableArchiveIntegrityFailureFallbacks(t *testing.T) {
	for _, mode := range []string{"body", "framing", "journal", "cursor", "sequence", "counts"} {
		t.Run(mode, func(t *testing.T) {
			ctx, s := archiveStoreFixture(t)
			a, b := archiveObject(t, "damaged", `{}`), archiveObject(t, "healthy", `{}`)
			origin, consumer := Hash("git:source"), Hash("consumer")
			archiveTransaction(t, s, func(tx *sql.Tx) error {
				if _, err := s.putWithin(ctx, tx, "staging", origin, []portable.EncodedEnvelope{a, b}); err != nil {
					return err
				}
				return s.activateWithin(ctx, tx, archiveSourceInventory([]portable.EncodedEnvelope{a, b}, map[string][]string{a.Envelope.EnvelopeSHA256: {origin}, b.Envelope.EnvelopeSHA256: {origin}}))
			})
			archiveTransaction(t, s, func(tx *sql.Tx) error {
				stamp, err := s.stampWithin(ctx, tx, "ready")
				if err != nil {
					return err
				}
				_, err = s.rememberWithin(ctx, tx, consumer, stamp)
				return err
			})
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := s.DB.Exec(query, args...); err != nil {
					t.Fatal(err)
				}
			}
			wantReason := "archive_changed"
			switch mode {
			case "body", "framing":
				if _, err := s.DB.Exec(`UPDATE history_archive_envelopes SET envelope_json=? WHERE project_id=? AND envelope_hash=?`, []byte(`{}`), s.ProjectID, a.Envelope.EnvelopeSHA256); err == nil {
					t.Fatal("immutable archive body was mutable")
				}
				exec(`DROP TRIGGER history_archive_envelope_immutable_update`)
				damaged := []byte(`{}`)
				if mode == "framing" {
					damaged = append(append([]byte(nil), a.Bytes...), '\n')
				}
				exec(`UPDATE history_archive_envelopes SET envelope_json=? WHERE project_id=? AND envelope_hash=?`, damaged, s.ProjectID, a.Envelope.EnvelopeSHA256)
			case "journal":
				exec(`DROP TRIGGER history_archive_change_immutable_delete`)
				exec(`DELETE FROM history_archive_changes WHERE sequence=(SELECT min(sequence) FROM history_archive_changes WHERE project_id=?)`, s.ProjectID)
			case "cursor":
				exec(`UPDATE history_archive_cursors SET checksum=? WHERE project_id=?`, Hash("damaged"), s.ProjectID)
				wantReason = "damaged_cursor"
			case "sequence":
				exec(`UPDATE history_archive_state SET last_sequence=last_sequence+1 WHERE project_id=?`, s.ProjectID)
				wantReason = "archive_changed"
			case "counts":
				exec(`UPDATE history_archive_state SET record_bytes=record_bytes+1 WHERE project_id=?`, s.ProjectID)
				wantReason = "archive_changed"
			}
			archiveTransaction(t, s, func(tx *sql.Tx) error {
				_, changes, reason, err := s.changesWithin(ctx, tx, consumer, 10)
				if err != nil || reason != wantReason || changes != nil {
					t.Fatalf("damage accepted as delta: %s %v", reason, err)
				}
				_, err = s.auditWithin(ctx, tx)
				if mode == "cursor" {
					if err != nil {
						t.Fatalf("derived cursor damage affected durable audit: %v", err)
					}
				} else if !errors.Is(err, errArchiveIntegrity) {
					t.Fatalf("full audit missed damage: %v", err)
				}
				if mode == "body" || mode == "framing" {
					if _, err := s.readObjectWithin(ctx, tx, a.Envelope.EnvelopeSHA256); !errors.Is(err, errArchiveIntegrity) {
						t.Fatalf("damaged access did not verify bytes: %v", err)
					}
					if _, err := s.readObjectWithin(ctx, tx, b.Envelope.EnvelopeSHA256); err != nil {
						t.Fatalf("healthy immutable object unreadable: %v", err)
					}
				}
				return nil
			})
		})
	}
}
