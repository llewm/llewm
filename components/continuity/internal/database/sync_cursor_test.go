package database

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
)

func TestSyncCursorRecoveryIsolationAndDamage(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cursor.db")
	db, err := Initialize(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { db.Close() }()
	exec := func(q string) {
		t.Helper()
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	read := func(limit int) (SyncCursor, []SyncChange, string) {
		t.Helper()
		c, d, f, e := ReadSyncChanges(ctx, db, "p", "capture", "archive", limit)
		if e != nil {
			t.Fatal(e)
		}
		return c, d, f
	}
	save := func(c SyncCursor, want bool) {
		t.Helper()
		ok, e := RememberSyncCursor(ctx, db, c, "archive")
		if e != nil || ok != want {
			t.Fatalf("save %v %v", ok, e)
		}
	}
	exec(`INSERT INTO projects(id,root,name,created_at) VALUES('p','/p','p','now'),('q','/q','q','now')`)
	c, _, f := read(10)
	if f != "missing_cursor" {
		t.Fatal(f)
	}
	save(c, true)
	exec(`UPDATE projects SET name='other' WHERE id='q'`)
	c, d, f := read(10)
	if f != "" || len(d) != 0 {
		t.Fatalf("other project: %v %s", d, f)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`UPDATE projects SET name='rollback' WHERE id='p'`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	_, d, f = read(10)
	if f != "" || len(d) != 0 {
		t.Fatalf("rollback: %v %s", d, f)
	}
	exec(`UPDATE projects SET name='changed' WHERE id='p'`)
	save(c, false) // A concurrent change cannot be acknowledged by the old snapshot.
	c, d, f = read(10)
	if f != "" || len(d) != 2 || d[0].Operation != "update_old" || d[1].Operation != "update_new" {
		t.Fatalf("delta: %v %s", d, f)
	}
	// No acknowledgement models a crash after external materialization. Reopening
	// must replay the exact ingestion identities, regardless of historical dates.
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	again, delta, f := read(10)
	if again != c || len(delta) != 2 || delta[0] != d[0] || delta[1] != d[1] || f != "" {
		t.Fatalf("restart: %v %v %s", again, delta, f)
	}
	_, _, f = read(1)
	if f != "delta_limit" {
		t.Fatal(f)
	}
	save(c, true)
	_, d, f = read(10)
	if f != "" || len(d) != 0 {
		t.Fatalf("acknowledged: %v %s", d, f)
	}
	if _, _, f, err := ReadSyncChanges(ctx, db, "p", "capture", "damaged archive", 10); err != nil || f != "materialization_changed" {
		t.Fatalf("archive: %s %v", f, err)
	}
	exec(`UPDATE history_sync_cursors SET sequence=sequence+1`)
	c, _, f = read(10)
	if f != "damaged_cursor" {
		t.Fatal(f)
	}
	save(c, true)
	exec(`DELETE FROM history_sync_changes WHERE sequence=(SELECT MIN(sequence) FROM history_sync_changes WHERE project_id='q')`)
	c, _, f = read(10)
	if f != "global_change" {
		t.Fatal(f)
	}
	save(c, true)
	exec(`INSERT INTO history_sync_changes(project_id,table_name,row_key,operation) VALUES('','unknown','[]','insert')`)
	c, _, f = read(10)
	if f != "global_change" {
		t.Fatal(f)
	}
	save(c, true)
	exec(`DELETE FROM history_sync_generation WHERE project_id='p'`)
	if _, _, _, err := ReadSyncChanges(ctx, db, "p", "capture", "archive", 10); err == nil {
		t.Fatal("missing tracking accepted")
	}
}

func BenchmarkSyncCursorFixedDelta(b *testing.B) {
	for _, history := range []int{100, 10000, 100000} {
		b.Run(strconv.Itoa(history), func(b *testing.B) {
			ctx := context.Background()
			db, err := Initialize(ctx, filepath.Join(b.TempDir(), "bench.db"))
			if err != nil {
				b.Fatal(err)
			}
			defer db.Close()
			if _, err = db.Exec(`INSERT INTO projects(id,root,name,created_at) VALUES('p','/p','p','now')`); err != nil {
				b.Fatal(err)
			}
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				b.Fatal(err)
			}
			for i := 0; i < history; i++ {
				if _, err = tx.Exec(`INSERT INTO history_sync_changes(project_id,table_name,row_key,operation) VALUES('p','resources','["old"]','insert')`); err != nil {
					b.Fatal(err)
				}
			}
			if err = tx.Commit(); err != nil {
				b.Fatal(err)
			}
			c, _, _, err := ReadSyncChanges(ctx, db, "p", "bench", "archive", 10)
			if err != nil {
				b.Fatal(err)
			}
			if _, err = RememberSyncCursor(ctx, db, c, "archive"); err != nil {
				b.Fatal(err)
			}
			if _, err = db.Exec(`INSERT INTO history_sync_changes(project_id,table_name,row_key,operation) VALUES('p','resources','["new"]','insert')`); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, d, f, e := ReadSyncChanges(ctx, db, "p", "bench", "archive", 10)
				if e != nil || f != "" || len(d) != 1 {
					b.Fatalf("%v %s %v", d, f, e)
				}
			}
		})
	}
}
