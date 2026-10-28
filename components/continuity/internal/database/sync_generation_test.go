package database

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// New tables must explicitly join change tracking or be reviewed as excluded.
// Raw usage and event logs are excluded; their exported summaries are tracked.
func TestSyncGenerationCoversSchema(t *testing.T) {
	db, err := Initialize(context.Background(), filepath.Join(t.TempDir(), "generation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	excluded := map[string]bool{}
	// Coordination uses separate lease refs, not curated envelope import.
	// lease_stamps and stale_write_resolutions are exported and stay tracked.
	for _, name := range []string{"lease_grants", "lease_events", "lease_remote_events"} {
		excluded[name] = true
	}
	// Replay proofs are rechecked against canonical state on use. Transport
	// inventory and publication membership remain tracked for damage/delivery.
	for _, name := range []string{"history_index_valid_sets", "history_index_narrative_tip", "history_index_narrative_members", "history_index_mixed_tip", "history_index_mixed_members"} {
		excluded[name] = true
	}
	for _, name := range strings.Fields(`bootstrap_deliveries contract_index_entries events head_observations commit_authors presence_events sessions sync_attempts usage_requests usage_sources usage_tool_calls usage_tool_results roadmap_sync_files roadmap_sync_runs portable_import_receipts history_sync_generation history_sync_proofs schema_migrations`) {
		excluded[name] = true
	}
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	for _, name := range tables {
		if excluded[name] || strings.HasPrefix(name, "history_chunks") {
			continue
		}
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND tbl_name=? AND name IN (?,?,?)`, name, "history_sync_"+name+"_insert", "history_sync_"+name+"_update", "history_sync_"+name+"_delete").Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 3 {
			t.Errorf("table %s needs explicit sync invalidation classification", name)
		}
		var updateSQL string
		if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name=?`, "history_sync_"+name+"_update").Scan(&updateSQL); err != nil {
			t.Fatal(err)
		}
		columns, err := db.Query(`PRAGMA table_info("` + name + `")`)
		if err != nil {
			t.Fatal(err)
		}
		for columns.Next() {
			var position, notNull, primaryKey int
			var column, kind string
			var defaultValue any
			if err := columns.Scan(&position, &column, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(updateSQL, `old."`+column+`" IS NOT new."`+column+`"`) {
				t.Errorf("%s.%s missing update invalidation", name, column)
			}
		}
		if err := columns.Err(); err != nil {
			t.Fatal(err)
		}
		columns.Close()
	}
}

func TestSyncGenerationIsTransactionalAndIgnoresNoopUpdates(t *testing.T) {
	ctx := context.Background()
	db, err := Initialize(ctx, filepath.Join(t.TempDir(), "generation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	token := func() string {
		t.Helper()
		var value string
		if err := db.QueryRow(`SELECT token FROM history_sync_generation WHERE id=1`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := token()
	if _, err := db.Exec(`INSERT INTO projects(id,root,name,created_at) VALUES('p','/p','p','2026-10-28T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	after := token()
	if before == after {
		t.Fatal("canonical insert did not invalidate")
	}
	if _, err := db.Exec(`UPDATE projects SET name=name WHERE id='p'`); err != nil {
		t.Fatal(err)
	}
	if token() != after {
		t.Fatal("no-op update invalidated")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE projects SET name='changed' WHERE id='p'`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if token() != after {
		t.Fatal("rollback leaked invalidation")
	}
}
