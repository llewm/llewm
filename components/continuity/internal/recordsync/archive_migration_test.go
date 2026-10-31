package recordsync

import (
	"bytes"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/portable"
)

func TestDurableArchiveLegacyMigrationPreservesForksAndOrigins(t *testing.T) {
	ctx, store := archiveStoreFixture(t)
	legacy, err := OpenArchive(filepath.Join(t.TempDir(), "archive"), lineage)
	if err != nil {
		t.Fatal(err)
	}
	a := archiveObject(t, "fork", `{"a":1}`)
	b := archiveObject(t, "fork", `{"b":2}`)
	for _, pair := range []struct {
		object portable.EncodedEnvelope
		origin string
	}{{a, "local"}, {a, "git:foreign"}, {b, "folder:peer"}} {
		if _, err := legacy.Put(pair.object, pair.origin); err != nil {
			t.Fatal(err)
		}
	}
	if err := legacy.SaveStatus("git:foreign", map[string]string{"disposable": "ignored"}); err != nil {
		t.Fatal(err)
	}
	source, err := collectArchiveMigration(ctx, legacy.Directory, lineage)
	if err != nil {
		t.Fatal(err)
	}
	expected := archiveSourceInventory([]portable.EncodedEnvelope{a, b}, map[string][]string{
		a.Envelope.EnvelopeSHA256: {Hash("local"), Hash("git:foreign")},
		b.Envelope.EnvelopeSHA256: {Hash("folder:peer")},
	})
	if source.inventory != expected {
		t.Fatalf("source inventory differs: %+v != %+v", source.inventory, expected)
	}
	for i := 0; i < 2; i++ {
		archiveTransaction(t, store, func(tx *sql.Tx) error {
			return source.stageWithin(ctx, tx, store)
		})
	}
	archiveTransaction(t, store, func(tx *sql.Tx) error {
		if _, err := store.stampWithin(ctx, tx, "ready"); !errors.Is(err, errArchiveNotReady) {
			t.Fatalf("staging activated before cutover: %v", err)
		}
		return store.activateWithin(ctx, tx, expected)
	})
	archiveTransaction(t, store, func(tx *sql.Tx) error {
		for _, object := range []portable.EncodedEnvelope{a, b} {
			got, err := store.readObjectWithin(ctx, tx, object.Envelope.EnvelopeSHA256)
			if err != nil || !bytes.Equal(got.Bytes, object.Bytes) {
				t.Fatalf("fork lost: %v", err)
			}
		}
		allowed, err := store.authorizedWithin(ctx, tx, b.Envelope.EnvelopeSHA256, Hash("git:foreign"), false)
		if err != nil || allowed {
			t.Fatalf("foreign observation gained local provenance: %v %v", allowed, err)
		}
		return nil
	})
	// The supported migration leaves its complete source available for backup
	// and explicit cutover recovery; no destructive filesystem action occurs.
	again, err := collectArchiveMigration(ctx, legacy.Directory, lineage)
	if err != nil || again.inventory != source.inventory {
		t.Fatalf("source changed: %v", err)
	}
}

func TestDurableArchiveLegacyMigrationRejectsUnaccountedData(t *testing.T) {
	for _, name := range []string{"missing-source", "missing-lineage", "wrong-lineage", "missing-origins", "bad-marker", "orphan-origin", "stage-file", "extra-newline", "symlink", "unknown-lineage"} {
		t.Run(name, func(t *testing.T) {
			ctx, _ := archiveStoreFixture(t)
			directory := filepath.Join(t.TempDir(), "archive")
			if name == "missing-source" {
				if _, err := collectArchiveMigration(ctx, directory, lineage); err == nil {
					t.Fatal("missing archive silently created")
				}
				if _, err := os.Lstat(directory); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("source was created: %v", err)
				}
				return
			}
			legacy, err := OpenArchive(directory, lineage)
			if err != nil {
				t.Fatal(err)
			}
			object := archiveObject(t, "one", `{"value":1}`)
			if _, err := legacy.Put(object, "local"); err != nil {
				t.Fatal(err)
			}
			origin := filepath.Join(directory, "origins", object.Envelope.EnvelopeSHA256, Hash("local"))
			record := filepath.Join(directory, portable.MemoryRecordPath(object.Envelope))
			write := func(path string, body []byte) {
				t.Helper()
				if err := os.WriteFile(path, body, 0600); err != nil {
					t.Fatal(err)
				}
			}
			remove := func(path string) {
				t.Helper()
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			switch name {
			case "missing-lineage":
				remove(filepath.Join(directory, "lineage"))
			case "wrong-lineage":
				write(filepath.Join(directory, "lineage"), []byte("lineage:00000000000000000000000000000000\n"))
			case "missing-origins":
				remove(origin)
			case "bad-marker":
				write(origin, []byte("forged\n"))
			case "orphan-origin":
				path := filepath.Join(directory, "origins", Hash("missing-object"))
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
				write(filepath.Join(path, Hash("local")), []byte("observed\n"))
			case "stage-file":
				write(filepath.Join(filepath.Dir(record), ".llewm-stage-interrupted"), []byte("partial"))
			case "extra-newline":
				write(record, append(portable.MemoryFileBytes(object), '\n'))
			case "symlink":
				remove(origin)
				if err := os.Symlink(record, origin); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			case "unknown-lineage":
				if err := os.Mkdir(filepath.Join(directory, "unaccounted"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := collectArchiveMigration(ctx, directory, lineage); err == nil {
				t.Fatal("ambiguous source accepted")
			}
		})
	}
}

func TestDurableArchiveLegacyMigrationEmptyAndContaminatedStaging(t *testing.T) {
	ctx, store := archiveStoreFixture(t)
	legacy, err := OpenArchive(filepath.Join(t.TempDir(), "empty"), lineage)
	if err != nil {
		t.Fatal(err)
	}
	source, err := collectArchiveMigration(ctx, legacy.Directory, lineage)
	if err != nil || source.inventory != archiveSourceInventory(nil, nil) {
		t.Fatalf("empty verified source: %+v %v", source.inventory, err)
	}
	archiveTransaction(t, store, func(tx *sql.Tx) error { return source.stageWithin(ctx, tx, store) })
	archiveTransaction(t, store, func(tx *sql.Tx) error {
		_, err := store.putWithin(ctx, tx, "staging", Hash("local"), []portable.EncodedEnvelope{archiveObject(t, "unexpected", `{}`)})
		return err
	})
	tx, err := store.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := source.stageWithin(ctx, tx, store); err == nil {
		t.Fatal("unexpected durable observations were discarded or accepted")
	}
}
