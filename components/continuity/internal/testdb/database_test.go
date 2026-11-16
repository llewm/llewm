package testdb

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/database"
)

func schema(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT type,name,coalesce(sql,'') FROM sqlite_schema ORDER BY type,name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var kind, name, sql string
		if err := rows.Scan(&kind, &name, &sql); err != nil {
			t.Fatal(err)
		}
		result = append(result, kind+"\n"+name+"\n"+sql)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestCopyMatchesMigratedSchemaAndReopens(t *testing.T) {
	ctx := context.Background()
	fresh, err := database.Initialize(ctx, filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	path := filepath.Join(t.TempDir(), "copy.db")
	copy, err := Create(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	if !reflect.DeepEqual(schema(t, fresh), schema(t, copy)) {
		t.Fatal("copied schema differs from production migration result")
	}
	for query, want := range map[string]string{"PRAGMA integrity_check": "ok", "PRAGMA journal_mode": "wal", "PRAGMA foreign_keys": "1"} {
		var got string
		if err := copy.QueryRow(query).Scan(&got); err != nil || got != want {
			t.Fatalf("%s: %q %v", query, got, err)
		}
	}
	if _, err := copy.Exec(`CREATE TABLE fixture_value(value TEXT); INSERT INTO fixture_value VALUES('retained')`); err != nil {
		t.Fatal(err)
	}
	if err := copy.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := database.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var value string
	if err := reopened.QueryRow(`SELECT value FROM fixture_value`).Scan(&value); err != nil || value != "retained" {
		t.Fatalf("reopen: %q %v", value, err)
	}
}

func TestParallelCopiesHaveIndependentDataAndIdentity(t *testing.T) {
	var tokens sync.Map
	for i := 0; i < 8; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			db, err := Create(context.Background(), filepath.Join(t.TempDir(), "copy.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var token string
			if err := db.QueryRow(`SELECT token FROM history_sync_generation WHERE project_id=''`).Scan(&token); err != nil {
				t.Fatal(err)
			}
			if _, exists := tokens.LoadOrStore(token, true); exists {
				t.Fatal("database identity was copied")
			}
			// Every copy must be able to create the same table without seeing a
			// sibling's table or contaminating the immutable schema image.
			if _, err := db.Exec(`CREATE TABLE isolation(value TEXT); INSERT INTO isolation VALUES('private')`); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOnlyKnownSeedTablesArePopulated(t *testing.T) {
	db, err := Create(context.Background(), filepath.Join(t.TempDir(), "copy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT name FROM pragma_table_list WHERE schema='main' AND type IN ('table','virtual') AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM "` + strings.ReplaceAll(name, `"`, `""`) + `"`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		// Writer-floor upgrades leave deterministic journal entries, with no
		// project identity or user data. Preserve those migration effects.
		if count != 0 && name != "schema_migrations" && name != "writer_floor" && name != "history_sync_generation" && name != "history_sync_changes" {
			t.Errorf("new seed table %s has %d rows: review whether copies need fresh identity", name, count)
		}
	}
	var unexpected int
	if err := db.QueryRow(`SELECT count(*) FROM history_sync_changes WHERE project_id<>'' OR table_name<>'writer_floor' OR row_key<>'[1]'`).Scan(&unexpected); err != nil || unexpected != 0 {
		t.Fatalf("unexpected migration journal data: %d %v", unexpected, err)
	}
}

func TestCreateRefusesExistingPathAndCanceledContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing")
	if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if db, err := Create(context.Background(), path); err == nil {
		db.Close()
		t.Fatal("overwrote existing path")
	}
	if body, err := os.ReadFile(path); err != nil || string(body) != "keep" {
		t.Fatalf("existing data changed: %q %v", body, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	newPath := filepath.Join(t.TempDir(), "canceled.db")
	if db, err := Create(ctx, newPath); err == nil {
		db.Close()
		t.Fatal("ignored cancellation")
	}
	if _, err := os.Stat(newPath); !os.IsNotExist(err) {
		t.Fatalf("canceled create wrote a file: %v", err)
	}
}

func BenchmarkFixtureDatabase(b *testing.B) {
	// Warm the process-local schema once; measure per-fixture costs after that.
	db, err := Create(context.Background(), filepath.Join(b.TempDir(), "warm.db"))
	if err != nil {
		b.Fatal(err)
	}
	db.Close()
	for _, strategy := range []struct {
		name   string
		create func(context.Context, string) (*sql.DB, error)
	}{
		{"migrate", database.Initialize}, {"copy", Create},
	} {
		b.Run(strategy.name, func(b *testing.B) {
			dir := b.TempDir()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				db, err := strategy.create(context.Background(), filepath.Join(dir, fmt.Sprintf("%d.db", i)))
				if err != nil {
					b.Fatal(err)
				}
				if err := db.Close(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
