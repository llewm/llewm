package recordsync

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/portable"
)

func archiveCutoverFixture(t testing.TB) (context.Context, archiveStore, archiveCutoverPaths) {
	t.Helper()
	ctx, store := archiveStoreFixture(t)
	parent := t.TempDir()
	paths := archiveCutoverPaths{Source: filepath.Join(parent, "history"), Retained: filepath.Join(parent, "retained-history"), DatabaseBackup: filepath.Join(parent, "before.db")}
	legacy, err := openLegacyArchiveFixture(paths.Source, store.Lineage)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Put(archiveObject(t, "before", `{"value":1}`), "git:peer"); err != nil {
		t.Fatal(err)
	}
	return ctx, store, paths
}

func TestArchiveCutoverPathsBindExactBytes(t *testing.T) {
	parent := t.TempDir()
	p := archiveCutoverPaths{Source: filepath.Join(parent, "history\xff"), Retained: filepath.Join(parent, "retained"), DatabaseBackup: filepath.Join(parent, "before.db")}
	other := p
	other.Source = filepath.Join(parent, "history\xfe")
	if p.hash() == other.hash() {
		t.Fatal("non-UTF8 path bytes collapsed to one recovery identity")
	}
	for _, bad := range []archiveCutoverPaths{
		{Source: p.Source, Retained: p.Source, DatabaseBackup: p.DatabaseBackup},
		{Source: p.Source, Retained: filepath.Join(parent, "different", "retained"), DatabaseBackup: p.DatabaseBackup},
		{Source: p.Source, Retained: p.Retained, DatabaseBackup: filepath.Join(p.Source, "inside.db")},
		{Source: "invalid\x00path", Retained: p.Retained, DatabaseBackup: p.DatabaseBackup},
	} {
		if _, err := bad.normalized(); err == nil {
			t.Fatalf("unsafe cutover paths accepted: %+v", bad)
		}
	}
}

func TestArchiveCutoverRejectsBackupParentAliasIntoSource(t *testing.T) {
	parent := t.TempDir()
	source := filepath.Join(parent, "history")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(parent, "backup-alias")
	if err := os.Symlink(source, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	paths := archiveCutoverPaths{Source: source, Retained: filepath.Join(parent, "retained"), DatabaseBackup: filepath.Join(alias, "before.db")}
	if _, err := paths.normalized(); err == nil {
		t.Fatal("backup parent alias allowed writes inside legacy archive")
	}
}

func TestDurableArchiveCutoverRecoversEveryBoundary(t *testing.T) {
	for _, phase := range []string{"prepared", "renamed", "fenced", "activated-with-arrival"} {
		t.Run(phase, func(t *testing.T) {
			ctx, store, paths := archiveCutoverFixture(t)
			receipt, err := prepareArchiveCutover(ctx, store, paths)
			if err != nil {
				t.Fatal(err)
			}
			again, err := prepareArchiveCutover(ctx, store, paths)
			if err != nil || receipt != again {
				t.Fatalf("preparation not idempotent: %+v %v", again, err)
			}
			backup, err := database.OpenReadOnly(ctx, paths.DatabaseBackup)
			if err != nil {
				t.Fatal(err)
			}
			var count int
			err = backup.QueryRow(`SELECT count(*) FROM history_archive_envelopes`).Scan(&count)
			backup.Close()
			if err != nil || count != 0 {
				t.Fatalf("backup was taken after staging: %d %v", count, err)
			}
			if phase == "renamed" || phase == "fenced" {
				if err := os.Rename(paths.Source, paths.Retained); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "fenced" {
				root, err := OpenRoot(filepath.Dir(paths.Source))
				if err != nil {
					t.Fatal(err)
				}
				err = publishArchiveFence(root, filepath.Base(paths.Source), receipt.fence())
				root.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := completeArchiveCutover(ctx, store, paths); err != nil {
				t.Fatal(err)
			}
			if phase == "activated-with-arrival" {
				archiveTransaction(t, store, func(tx *sql.Tx) error {
					_, err := store.putWithin(ctx, tx, "ready", Hash("local"), []portable.EncodedEnvelope{archiveObject(t, "after", `{}`)})
					return err
				})
			}
			if err := completeArchiveCutover(ctx, store, paths); err != nil {
				t.Fatalf("completion not idempotent: %v", err)
			}
			if _, err := openLegacyArchiveFixture(paths.Source, store.Lineage); err == nil {
				t.Fatal("legacy writer recreated fenced archive")
			}
			retained, err := collectArchiveMigration(ctx, paths.Retained, store.Lineage)
			if err != nil || retained.inventory != receipt.Inventory {
				t.Fatalf("retained source changed: %v", err)
			}
			archiveTransaction(t, store, func(tx *sql.Tx) error {
				_, err := store.stampWithin(ctx, tx, "ready")
				return err
			})
		})
	}
}

func TestDurableArchiveCutoverRefusesChangedRecoveryInputs(t *testing.T) {
	for _, mutation := range []string{"source", "backup", "retained", "competing-source", "fence", "paths", "receipt", "staging"} {
		t.Run(mutation, func(t *testing.T) {
			ctx, store, paths := archiveCutoverFixture(t)
			if _, err := prepareArchiveCutover(ctx, store, paths); err != nil {
				t.Fatal(err)
			}
			put := func(directory string) {
				t.Helper()
				legacy, err := openLegacyArchiveFixture(directory, store.Lineage)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := legacy.Put(archiveObject(t, "unexpected", `{}`), "local"); err != nil {
					t.Fatal(err)
				}
			}
			if mutation == "retained" || mutation == "competing-source" || mutation == "fence" {
				if err := os.Rename(paths.Source, paths.Retained); err != nil {
					t.Fatal(err)
				}
			}
			switch mutation {
			case "source":
				put(paths.Source)
			case "retained":
				put(paths.Retained)
			case "competing-source":
				put(paths.Source)
			case "backup":
				if err := os.WriteFile(paths.DatabaseBackup, []byte("damaged backup"), 0600); err != nil {
					t.Fatal(err)
				}
			case "fence":
				if err := os.WriteFile(paths.Source, []byte("unexpected file"), 0600); err != nil {
					t.Fatal(err)
				}
			case "paths":
				paths.DatabaseBackup += ".different"
			case "receipt":
				if _, err := store.DB.Exec(`UPDATE history_archive_state SET migration_json='{}',migration_checksum=? WHERE project_id=?`, Hash("{}"), store.ProjectID); err == nil {
					t.Fatal("immutable recovery receipt changed")
				}
				if _, err := store.DB.Exec(`DROP TRIGGER history_archive_identity_immutable`); err != nil {
					t.Fatal(err)
				}
				if _, err := store.DB.Exec(`UPDATE history_archive_state SET migration_json='{}' WHERE project_id=?`, store.ProjectID); err != nil {
					t.Fatal(err)
				}
			case "staging":
				if _, err := store.DB.Exec(`DROP TRIGGER history_archive_envelope_immutable_update`); err != nil {
					t.Fatal(err)
				}
				if _, err := store.DB.Exec(`UPDATE history_archive_envelopes SET envelope_json=cast('damaged' as blob) WHERE project_id=?`, store.ProjectID); err != nil {
					t.Fatal(err)
				}
			}
			if err := completeArchiveCutover(ctx, store, paths); err == nil {
				t.Fatal("uncertain recovery state activated")
			}
			if mutation == "staging" {
				info, err := os.Stat(paths.Source)
				if err != nil || !info.IsDir() {
					t.Fatalf("damaged staging retired usable source: %v", err)
				}
			}
			var status string
			if err := store.DB.QueryRow(`SELECT status FROM history_archive_state WHERE project_id=?`, store.ProjectID).Scan(&status); err != nil || status != "staging" {
				t.Fatalf("failed cutover became active: %s %v", status, err)
			}
		})
	}
}

func TestDurableArchiveTransactionCleansFailedCommit(t *testing.T) {
	ctx, store := archiveStoreFixture(t)
	store.DB.SetMaxOpenConns(1)
	object := archiveObject(t, "commit-failure", `{}`)
	e := object.Envelope
	err := store.transact(ctx, func(tx *sql.Tx) error {
		// This body has a deferred first-origin FK. SQLite rejects COMMIT,
		// after database/sql has already finalized the Tx handle.
		_, err := tx.ExecContext(ctx, `INSERT INTO history_archive_envelopes(project_id,envelope_hash,record_type,record_id,record_revision,initial_origin_hash,envelope_json) VALUES(?,?,?,?,?,?,?)`, store.ProjectID, e.EnvelopeSHA256, e.RecordType, e.RecordID, e.RecordRevision, Hash("missing-origin"), object.Bytes)
		return err
	})
	if err == nil {
		t.Fatal("deferred FK failure committed")
	}
	if err := store.transact(ctx, func(tx *sql.Tx) error {
		stamp, err := store.stampWithin(ctx, tx, "staging")
		if err != nil {
			return err
		}
		if stamp.Records != 0 || stamp.Changes != 0 {
			t.Fatal("failed COMMIT leaked archive/journal rows")
		}
		_, err = store.putWithin(ctx, tx, "staging", Hash("local"), []portable.EncodedEnvelope{object})
		return err
	}); err != nil {
		t.Fatalf("connection not reusable after failed COMMIT: %v", err)
	}
}

func TestDurableArchiveCutoverRequiresPreparationAndNeverOverwritesBackup(t *testing.T) {
	ctx, store, paths := archiveCutoverFixture(t)
	if err := completeArchiveCutover(ctx, store, paths); err == nil {
		t.Fatal("unprepared source was fenced")
	}
	if err := os.WriteFile(paths.DatabaseBackup, []byte("keep existing backup"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareArchiveCutover(ctx, store, paths); err == nil {
		t.Fatal("existing backup was overwritten")
	}
	if body, err := os.ReadFile(paths.DatabaseBackup); err != nil || string(body) != "keep existing backup" {
		t.Fatalf("backup changed: %q %v", body, err)
	}
	root, err := OpenRoot(paths.Source)
	if err != nil {
		t.Fatalf("failed preparation fenced source: %v", err)
	}
	root.Close()
}
