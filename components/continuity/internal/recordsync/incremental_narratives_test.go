package recordsync

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
)

func TestNarrativeAdditionsMatchFullImport(t *testing.T) {
	ctx, source, sourceID := indexedTestProject(t, "add-source")
	_, indexed, indexedID := indexedTestProject(t, "add-indexed")
	_, full, fullID := indexedTestProject(t, "add-full")
	appendRecord := func(db *sql.DB, project, id string) {
		t.Helper()
		_, err := (worknarrative.Service{DB: db}).Append(ctx, worknarrative.Record{ID: id, ProjectID: project, Title: id, Trigger: "manual", Creator: "test", Claims: []worknarrative.Claim{{Kind: "outcome", Text: "Observation", Authority: "caller_asserted"}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	appendRecord(source, sourceID, "first")
	initial, err := portable.EncodeMemoryExport(ctx, source, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	first, err := EncodeBlock(lineage, initial.Envelopes)
	if err != nil {
		t.Fatal(err)
	}
	index := Index{DB: indexed, ProjectID: indexedID, Lineage: lineage}
	if _, err := index.ImportBlocks(ctx, []Block{first}); err != nil {
		t.Fatal(err)
	}
	if _, err := (Importer{DB: full, ProjectID: fullID}).Import(ctx, first.Objects); err != nil {
		t.Fatal(err)
	}
	appendRecord(source, sourceID, "second")
	export, err := portable.EncodeMemoryExport(ctx, source, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	setHash, ok := independentNarrativeSet(export.Envelopes)
	if !ok {
		t.Fatal("fixture is not eligible")
	}
	want, wantErr := (Importer{DB: full, ProjectID: fullID}).Import(ctx, export.Envelopes)
	got, used, gotErr := index.tryNarrativeAdditions(ctx, setHash, export.Envelopes)
	if !used || fmt.Sprint(wantErr) != fmt.Sprint(gotErr) || want != got {
		t.Fatalf("full=%+v %v incremental=%+v used=%t %v", want, wantErr, got, used, gotErr)
	}
	if present, err := index.validatedNarrativesPresent(ctx, setHash, export.Envelopes); err != nil || !present {
		t.Fatalf("additive proof missing: %v", err)
	}
}

func TestNarrativeAdditionRollsBackWhenPriorStateChanges(t *testing.T) {
	ctx, source, sourceID := indexedTestProject(t, "rollback-source")
	_, indexed, indexedID := indexedTestProject(t, "rollback-indexed")
	appendRecord := func(id string) {
		t.Helper()
		_, err := (worknarrative.Service{DB: source}).Append(ctx, worknarrative.Record{ID: id, ProjectID: sourceID, Title: id, Trigger: "manual", Creator: "test", Claims: []worknarrative.Claim{{Kind: "outcome", Text: "Observation", Authority: "caller_asserted"}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	appendRecord("first")
	initial, err := portable.EncodeMemoryExport(ctx, source, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	block, err := EncodeBlock(lineage, initial.Envelopes)
	if err != nil {
		t.Fatal(err)
	}
	index := Index{DB: indexed, ProjectID: indexedID, Lineage: lineage}
	if _, err := index.ImportBlocks(ctx, []Block{block}); err != nil {
		t.Fatal(err)
	}
	appendRecord("second")
	export, err := portable.EncodeMemoryExport(ctx, source, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := indexed.ExecContext(ctx, `CREATE TRIGGER alter_prior BEFORE INSERT ON work_narratives WHEN NEW.id='second' BEGIN UPDATE work_narratives SET payload_json='{}' WHERE id='first'; END`); err != nil {
		t.Fatal(err)
	}
	setHash, _ := independentNarrativeSet(export.Envelopes)
	if _, used, err := index.tryNarrativeAdditions(ctx, setHash, export.Envelopes); err != nil || used {
		t.Fatalf("changed prior state accepted: used=%t %v", used, err)
	}
	var count int
	if err := indexed.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_narratives WHERE project_id=? AND id='second'`, indexedID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial import survived: %d %v", count, err)
	}
	if _, err := indexed.ExecContext(ctx, `DROP TRIGGER alter_prior`); err != nil {
		t.Fatal(err)
	}
	if _, err := (Importer{DB: indexed, ProjectID: indexedID}).Import(context.Background(), export.Envelopes); err != nil {
		t.Fatalf("rollback damaged prior state: %v", err)
	}
}
