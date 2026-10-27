package recordsync

import (
	"database/sql"
	"fmt"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
)

func TestMixedAdditionsMatchFullImportAndGuardDependencies(t *testing.T) {
	ctx, source, sourceID := indexedTestProject(t, "mixed-add-source")
	_, indexed, indexedID := indexedTestProject(t, "mixed-add-indexed")
	_, full, fullID := indexedTestProject(t, "mixed-add-full")
	appendRecord := func(db *sql.DB, project, id, parent string) {
		t.Helper()
		_, err := (worknarrative.Service{DB: db}).Append(ctx, worknarrative.Record{ID: id, ProjectID: project, Title: id, Trigger: "manual", Creator: "test", SupersedesID: parent, Claims: []worknarrative.Claim{{Kind: "outcome", Text: "Observation", Authority: "caller_asserted"}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	appendRecord(source, sourceID, "first", "")
	appendRecord(source, sourceID, "second", "first")
	initial, err := portable.EncodeMemoryExport(ctx, source, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	block, err := EncodeBlock(lineage, initial.Envelopes)
	if err != nil {
		t.Fatal(err)
	}
	index := Index{DB: indexed, ProjectID: indexedID, Lineage: lineage}
	for i := 0; i < 2; i++ {
		want, wantErr := (Importer{DB: full, ProjectID: fullID}).Import(ctx, block.Objects)
		got, gotErr := index.ImportBlocks(ctx, []Block{block})
		if fmt.Sprint(wantErr) != fmt.Sprint(gotErr) || want != got {
			t.Fatalf("initial %d: full=%+v %v indexed=%+v %v", i, want, wantErr, got, gotErr)
		}
	}
	appendRecord(source, sourceID, "third", "")
	export, err := portable.EncodeMemoryExport(ctx, source, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	setHash, eligible := mixedReplaySet(export.Envelopes)
	if !eligible {
		t.Fatal("fixture is not eligible")
	}
	want, wantErr := (Importer{DB: full, ProjectID: fullID}).Import(ctx, export.Envelopes)
	got, used, gotErr := index.tryMixedAdditions(ctx, setHash, export.Envelopes)
	if !used || fmt.Sprint(wantErr) != fmt.Sprint(gotErr) || want != got {
		t.Fatalf("full=%+v %v incremental=%+v used=%t %v", want, wantErr, got, used, gotErr)
	}
	if _, proof, err := index.mixedReplaySnapshot(ctx, setHash, export.Envelopes, ""); err != nil || !proof {
		t.Fatalf("new mixed proof missing: %v", err)
	}
	// A local change outside the added input invalidates the previous proof.
	appendRecord(indexed, indexedID, "unrelated-local", "")
	appendRecord(source, sourceID, "fifth", "")
	changed, err := portable.EncodeMemoryExport(ctx, source, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	changedHash, _ := mixedReplaySet(changed.Envelopes)
	if _, used, err := index.tryMixedAdditions(ctx, changedHash, changed.Envelopes); err != nil || used {
		t.Fatalf("unrelated local change incorrectly used shortcut: used=%t %v", used, err)
	}
	var count int
	if err := indexed.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_narratives WHERE project_id=? AND id='fifth'`, indexedID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rejected mixed shortcut left partial records: %d %v", count, err)
	}
	// A new dependent successor is intentionally sent to the full importer.
	appendRecord(source, sourceID, "fourth", "third")
	dependent, err := portable.EncodeMemoryExport(ctx, source, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	dependentHash, _ := mixedReplaySet(dependent.Envelopes)
	if _, used, err := index.tryMixedAdditions(ctx, dependentHash, dependent.Envelopes); err != nil || used {
		t.Fatalf("dependency incorrectly used shortcut: used=%t %v", used, err)
	}
}
