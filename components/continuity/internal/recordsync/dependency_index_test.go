package recordsync

import (
	"fmt"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/portable"
)

func TestDependencySelectorBatchesAndProjectScope(t *testing.T) {
	ctx, db, project := indexedTestProject(t, "dependency-batches")
	var objects []portable.EncodedEnvelope
	for i := 0; i < 131; i++ {
		object, err := portable.EncodeEnvelope(portable.EnvelopeSpec{LineageID: lineage, RecordType: "decision", RecordSchemaVersion: 1, RecordID: fmt.Sprintf("decision-%03d", i), RecordedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Authority: "caller_asserted", Index: []byte(`{"title":"Decision","status":"accepted"}`), Payload: []byte(`{"title":"Decision","rationale":"Observation","disposition":"accepted","actor":"test","actor_type":"human","review_source":"","links":[],"operational_schema_version":1}`)})
		if err != nil {
			t.Fatal(err)
		}
		objects = append(objects, object)
	}
	index := Index{DB: db, ProjectID: project, Lineage: lineage}
	values, err := index.dependencySelectors(ctx, objects)
	if err != nil || len(values) != len(objects) {
		t.Fatalf("initial batches: %d %v", len(values), err)
	}
	for _, value := range values {
		if !value.Supported || len(value.Selectors) != 0 {
			t.Fatalf("independent fixture: %+v", value)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO projects(id,root,name,created_at) SELECT 'other',root||'-other','Other',created_at FROM projects WHERE id=?`, project); err != nil {
		t.Fatal(err)
	}
	other := Index{DB: db, ProjectID: "other", Lineage: lineage}
	if _, err := other.dependencySelectors(ctx, objects); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE history_index_dependency_records SET selectors_sha256='damaged' WHERE project_id='other'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TRIGGER reject_selector_rewrite BEFORE INSERT ON history_index_dependency_records BEGIN SELECT RAISE(ABORT,'unexpected rewrite'); END`); err != nil {
		t.Fatal(err)
	}
	// A new Index value reuses persistent rows across batch boundaries. Damage
	// in another project must not cause repair or invalidate this graph.
	reloaded := Index{DB: db, ProjectID: project, Lineage: lineage}
	if got, err := reloaded.dependencySelectors(ctx, objects); err != nil || len(got) != 131 {
		t.Fatalf("persistent batch reuse: %d %v", len(got), err)
	}
	if _, err := db.ExecContext(ctx, `DROP TRIGGER reject_selector_rewrite`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM history_index_dependency_records WHERE project_id=? AND envelope_hash=?`, project, objects[130].Envelope.EnvelopeSHA256); err != nil {
		t.Fatal(err)
	}
	if got, err := reloaded.dependencySelectors(ctx, objects); err != nil || len(got) != 131 {
		t.Fatalf("last batch repair: %d %v", len(got), err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM history_index_dependency_records WHERE project_id=?`, project).Scan(&count); err != nil || count != 131 {
		t.Fatalf("repaired membership: %d %v", count, err)
	}
}
