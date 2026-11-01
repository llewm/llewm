package database

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestHistoryStorageTrackingMatchesEmbeddedDefinitionsAndRejectsChangedGuards(t *testing.T) {
	ctx := context.Background()
	db, err := Initialize(ctx, filepath.Join(t.TempDir(), "tracking.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	check := func(want bool) {
		t.Helper()
		tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		got, err := HistoryStorageTracking(ctx, tx)
		tx.Rollback()
		if err != nil || got != want {
			t.Fatalf("tracking=%v want=%v: %v", got, want, err)
		}
	}
	check(true)
	if _, err = db.Exec(`CREATE TRIGGER unrelated_selector_hook AFTER INSERT ON history_index_dependency_records BEGIN SELECT 1; END`); err != nil {
		t.Fatal(err)
	}
	check(false)
	if _, err = db.Exec(`DROP TRIGGER unrelated_selector_hook`); err != nil {
		t.Fatal(err)
	}
	check(true)
	if _, err = db.Exec(`DROP TRIGGER history_inventory_history_index_blocks_insert`); err != nil {
		t.Fatal(err)
	}
	check(false)
	if _, err = db.Exec(`CREATE TRIGGER history_inventory_history_index_blocks_insert AFTER INSERT ON history_index_blocks BEGIN SELECT 1; END`); err != nil {
		t.Fatal(err)
	}
	check(false)
}

func TestInventoryGenerationIsProjectScopedTransactionalAndIgnoresCanonicalChanges(t *testing.T) {
	ctx := context.Background()
	db, err := Initialize(ctx, filepath.Join(t.TempDir(), "generation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, id := range []string{"one", "two"} {
		if _, err = db.Exec(`INSERT INTO projects(id,root,name,created_at) VALUES(?,?,?,'2026-01-01T00:00:00Z')`, id, "/"+id, id); err != nil {
			t.Fatal(err)
		}
	}
	token := func(id string) string {
		t.Helper()
		var value string
		if err := db.QueryRow(`SELECT token FROM history_index_inventory_generation WHERE project_id=?`, id).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	one, two := token("one"), token("two")
	if _, err = db.Exec(`UPDATE projects SET name='canonical change' WHERE id='one'`); err != nil {
		t.Fatal(err)
	}
	if token("one") != one {
		t.Fatal("canonical change invalidated inventory")
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`INSERT INTO history_index_blocks VALUES('one','rollback',6)`); err != nil {
		t.Fatal(err)
	}
	tx.Rollback()
	if token("one") != one {
		t.Fatal("rollback leaked generation")
	}
	if _, err = db.Exec(`INSERT INTO history_index_blocks VALUES('one','block',6)`); err != nil {
		t.Fatal(err)
	}
	next := token("one")
	if next == one || token("two") != two {
		t.Fatal("insert scope wrong")
	}
	if _, err = db.Exec(`UPDATE history_index_blocks SET index_version=index_version WHERE project_id='one'`); err != nil {
		t.Fatal(err)
	}
	if token("one") != next {
		t.Fatal("no-op invalidated inventory")
	}
	if _, err = db.Exec(`UPDATE history_index_blocks SET project_id='two' WHERE project_id='one'`); err != nil {
		t.Fatal(err)
	}
	if token("one") == next || token("two") == two {
		t.Fatal("moved row did not invalidate both projects")
	}
}

func TestDependencyGenerationTracksCacheChangesWithoutInvalidatingTransport(t *testing.T) {
	ctx := context.Background()
	db, err := Initialize(ctx, filepath.Join(t.TempDir(), "dependency-generation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, project := range []string{"one", "two"} {
		if _, err := db.Exec(`INSERT INTO projects(id,root,name,created_at) VALUES(?,?,?,'now')`, project, "/"+project, project); err != nil {
			t.Fatal(err)
		}
	}
	token := func(project string) string {
		t.Helper()
		var value string
		if err := db.QueryRow(`SELECT token FROM history_index_dependency_generation WHERE project_id=?`, project).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	canonical, err := SyncGeneration(ctx, db, "one")
	if err != nil {
		t.Fatal(err)
	}
	var inventory string
	if err := db.QueryRow(`SELECT token FROM history_index_inventory_generation WHERE project_id='one'`).Scan(&inventory); err != nil {
		t.Fatal(err)
	}
	one, two := token("one"), token("two")
	if _, err := db.Exec(`INSERT INTO history_index_dependency_records VALUES('one','source',6,1,'digest')`); err != nil {
		t.Fatal(err)
	}
	if token("one") == one || token("two") != two {
		t.Fatal("selector record changed the wrong project generation")
	}
	one = token("one")
	if _, err := db.Exec(`UPDATE history_index_dependency_records SET supported=supported`); err != nil {
		t.Fatal(err)
	}
	if token("one") != one {
		t.Fatal("no-op cache update invalidated selectors")
	}
	if _, err := db.Exec(`UPDATE history_index_dependency_records SET project_id='two' WHERE project_id='one'`); err != nil {
		t.Fatal(err)
	}
	if token("one") == one || token("two") == two {
		t.Fatal("moving selector ownership did not invalidate both projects")
	}
	two = token("two")
	if _, err := db.Exec(`INSERT INTO history_index_dependencies VALUES('two','source','decision','target','exact',0)`); err != nil {
		t.Fatal(err)
	}
	if token("two") == two {
		t.Fatal("adding an edge did not invalidate selectors")
	}
	two = token("two")
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE history_index_dependencies SET target_id='uncommitted'`); err != nil {
		t.Fatal(err)
	}
	var staged string
	if err := tx.QueryRow(`SELECT token FROM history_index_dependency_generation WHERE project_id='two'`).Scan(&staged); err != nil || staged == two {
		t.Fatalf("edge update did not invalidate selectors: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if token("two") != two {
		t.Fatal("rollback leaked a selector generation")
	}
	if _, err := db.Exec(`DELETE FROM history_index_dependencies`); err != nil {
		t.Fatal(err)
	}
	if token("two") == two {
		t.Fatal("removing an edge did not invalidate selectors")
	}
	two = token("two")
	if _, err := db.Exec(`DELETE FROM history_index_dependency_records`); err != nil {
		t.Fatal(err)
	}
	if token("two") == two {
		t.Fatal("cache removal did not invalidate selectors")
	}
	afterCanonical, err := SyncGeneration(ctx, db, "one")
	var afterInventory string
	if err := db.QueryRow(`SELECT token FROM history_index_inventory_generation WHERE project_id='one'`).Scan(&afterInventory); err != nil {
		t.Fatal(err)
	}
	if err != nil || afterCanonical != canonical || afterInventory != inventory {
		t.Fatalf("cache refresh invalidated transport inputs: %v", err)
	}
	if _, err := db.Exec(`DROP TRIGGER history_dependency_edges_insert`); err != nil {
		t.Fatal(err)
	}
	guard, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Rollback()
	if known, err := HistoryStorageTracking(ctx, guard); err != nil || known {
		t.Fatalf("missing selector tracking guard accepted: %t %v", known, err)
	}
}
