package recordsync

import (
	"bytes"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/topic"
	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
)

func TestMixedFirstTopicRevisionMatchesReference(t *testing.T) {
	ctx, source, sourceID := indexedTestProject(t, "topic-add-source")
	_, indexed, indexedID := indexedTestProject(t, "topic-add-indexed")
	_, full, fullID := indexedTestProject(t, "topic-add-full")
	for _, id := range []string{"root", "successor"} {
		parent := ""
		if id == "successor" {
			parent = "root"
		}
		if _, err := (worknarrative.Service{DB: source}).Append(ctx, worknarrative.Record{ID: id, ProjectID: sourceID, Title: id, Trigger: "manual", Creator: "test", SupersedesID: parent, Claims: []worknarrative.Claim{{Kind: "outcome", Text: "Observation", Authority: "caller_asserted"}}}); err != nil {
			t.Fatal(err)
		}
	}
	index := Index{DB: indexed, ProjectID: indexedID, Lineage: lineage}
	initial, err := portable.EncodeMemoryExport(ctx, source, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	block, err := EncodeBlock(lineage, initial.Envelopes)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := index.ImportBlocks(ctx, []Block{block}); err != nil {
			t.Fatal(err)
		}
		if _, err := (Importer{DB: full, ProjectID: fullID}).Import(ctx, initial.Envelopes); err != nil {
			t.Fatal(err)
		}
	}
	for revision := 1; revision <= 2; revision++ {
		if _, err := (topic.Service{DB: source}).Record(ctx, topic.Topic{ID: "new-topic", ProjectID: sourceID, Title: fmt.Sprintf("Topic revision %d", revision), Creator: "test"}); err != nil {
			t.Fatal(err)
		}
		export, err := portable.EncodeMemoryExport(ctx, source, sourceID)
		if err != nil {
			t.Fatal(err)
		}
		hash, eligible := mixedReplaySet(export.Envelopes)
		if !eligible {
			t.Fatal("ineligible fixture")
		}
		want, wantErr := (Importer{DB: full, ProjectID: fullID}).Import(ctx, export.Envelopes)
		got, used, gotErr := index.tryMixedAdditions(ctx, hash, export.Envelopes)
		if !used {
			t.Fatalf("revision %d missed typed closure: %v", revision, gotErr)
		}
		if fmt.Sprint(wantErr) != fmt.Sprint(gotErr) || want != got {
			t.Fatalf("revision %d: reference=%+v %v indexed=%+v %v", revision, want, wantErr, got, gotErr)
		}
		var replayed int
		if err := indexed.QueryRowContext(ctx, `SELECT already_present_records FROM portable_import_receipts WHERE project_id=? ORDER BY applied_at DESC,id DESC LIMIT 1`, indexedID).Scan(&replayed); err != nil {
			t.Fatal(err)
		}
		if replayed != 0 {
			t.Fatalf("independent topic arrival replayed %d unrelated records", replayed)
		}
		wantRecords, err := portable.LocalEnvelopes(ctx, full, fullID)
		if err != nil {
			t.Fatal(err)
		}
		gotRecords, err := portable.LocalEnvelopes(ctx, indexed, indexedID)
		if err != nil {
			t.Fatal(err)
		}
		if len(wantRecords) != len(gotRecords) {
			t.Fatal("semantic counts differ")
		}
		for key, value := range wantRecords {
			if !bytes.Equal(value.Bytes, gotRecords[key].Bytes) {
				t.Fatalf("semantic record %s differs", key)
			}
		}
	}
}

// Exercise the public coordinator across changing block boundaries, local
// invalidation, and dependent additions. Shortcut-only tests cannot establish
// that fallback and proof renewal compose correctly over several sync cycles.
func TestMixedImportCyclesMatchReference(t *testing.T) {
	ctx, source, sourceID := indexedTestProject(t, "cycles-source")
	_, indexed, indexedID := indexedTestProject(t, "cycles-indexed")
	_, full, fullID := indexedTestProject(t, "cycles-full")
	appendRecord := func(db *sql.DB, project, id, parent string) {
		t.Helper()
		_, err := (worknarrative.Service{DB: db}).Append(ctx, worknarrative.Record{ID: id, ProjectID: project, Title: id, Trigger: "manual", Creator: "test", SupersedesID: parent, Claims: []worknarrative.Claim{{Kind: "outcome", Text: "Observation", Authority: "caller_asserted"}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	appendRecord(source, sourceID, "root", "")
	appendRecord(source, sourceID, "head", "root")
	index := Index{DB: indexed, ProjectID: indexedID, Lineage: lineage}
	head := "head"
	for cycle := 0; cycle < 6; cycle++ {
		if cycle == 2 {
			appendRecord(indexed, indexedID, "local", "")
			local, err := portable.EncodeMemoryExport(ctx, indexed, indexedID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := (Importer{DB: full, ProjectID: fullID}).Import(ctx, local.Envelopes); err != nil {
				t.Fatal(err)
			}
		}
		if cycle > 0 {
			parent := ""
			if cycle%2 == 0 {
				parent = head
			}
			appendRecord(source, sourceID, fmt.Sprintf("addition-%d", cycle), parent)
			if parent != "" {
				head = fmt.Sprintf("addition-%d", cycle)
			}
		}
		exported, err := portable.EncodeMemoryExport(ctx, source, sourceID)
		if err != nil {
			t.Fatal(err)
		}
		var blocks []Block
		// Alternate a single block and one block per object, in reverse order.
		if cycle%2 == 0 {
			block, err := EncodeBlock(lineage, exported.Envelopes)
			if err != nil {
				t.Fatal(err)
			}
			blocks = append(blocks, block)
		} else {
			for i := len(exported.Envelopes) - 1; i >= 0; i-- {
				block, err := EncodeBlock(lineage, exported.Envelopes[i:i+1])
				if err != nil {
					t.Fatal(err)
				}
				blocks = append(blocks, block)
			}
		}
		for replay := 0; replay < 3; replay++ {
			want, wantErr := (Importer{DB: full, ProjectID: fullID}).Import(ctx, exported.Envelopes)
			got, gotErr := index.ImportBlocks(ctx, blocks)
			if fmt.Sprint(wantErr) != fmt.Sprint(gotErr) || want != got {
				t.Fatalf("cycle %d replay %d: full=%+v %v indexed=%+v %v", cycle, replay, want, wantErr, got, gotErr)
			}
			wantRecords, err := portable.LocalEnvelopes(ctx, full, fullID)
			if err != nil {
				t.Fatal(err)
			}
			gotRecords, err := portable.LocalEnvelopes(ctx, indexed, indexedID)
			if err != nil {
				t.Fatal(err)
			}
			if len(wantRecords) != len(gotRecords) {
				t.Fatal("semantic record counts differ")
			}
			for key, object := range wantRecords {
				if !bytes.Equal(object.Bytes, gotRecords[key].Bytes) {
					t.Fatalf("cycle %d replay %d: semantic record %s differs", cycle, replay, key)
				}
			}
		}
	}
}

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
	// Common task and commit anchors are metadata, and should not force a
	// full replay of an otherwise independent narrative.
	_, err = (worknarrative.Service{DB: source}).Append(ctx, worknarrative.Record{ID: "anchored", ProjectID: sourceID, Title: "anchored", Trigger: "manual", Creator: "test", Claims: []worknarrative.Claim{{Kind: "outcome", Text: "Observation", Authority: "caller_asserted"}}, Anchors: []worknarrative.Anchor{{Kind: "task", Target: "task-a", Authority: "caller_asserted"}, {Kind: "commit", Target: strings.Repeat("a", 40), Authority: "caller_asserted"}}})
	if err != nil {
		t.Fatal(err)
	}
	anchored, err := portable.EncodeMemoryExport(ctx, source, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	anchoredHash, ok := mixedReplaySet(anchored.Envelopes)
	if !ok {
		t.Fatal("anchored fixture ineligible")
	}
	want, wantErr = (Importer{DB: full, ProjectID: fullID}).Import(ctx, anchored.Envelopes)
	got, used, gotErr = index.tryMixedAdditions(ctx, anchoredHash, anchored.Envelopes)
	if !used || fmt.Sprint(wantErr) != fmt.Sprint(gotErr) || want != got {
		t.Fatalf("anchored: full=%+v %v incremental=%+v used=%t %v", want, wantErr, got, used, gotErr)
	}
	if _, proof, err := index.mixedReplaySnapshot(ctx, anchoredHash, anchored.Envelopes, ""); err != nil || !proof {
		t.Fatalf("anchored proof missing: %v", err)
	}
	_, err = (worknarrative.Service{DB: source}).Append(ctx, worknarrative.Record{ID: "other-anchor", ProjectID: sourceID, Title: "other anchor", Trigger: "manual", Creator: "test", Claims: []worknarrative.Claim{{Kind: "outcome", Text: "Observation", Authority: "caller_asserted"}}, Anchors: []worknarrative.Anchor{{Kind: "roadmap_node", Target: "node-a", Authority: "caller_asserted"}}})
	if err != nil {
		t.Fatal(err)
	}
	other, err := portable.EncodeMemoryExport(ctx, source, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	otherHash, _ := mixedReplaySet(other.Envelopes)
	want, wantErr = (Importer{DB: full, ProjectID: fullID}).Import(ctx, other.Envelopes)
	got, used, gotErr = index.tryMixedAdditions(ctx, otherHash, other.Envelopes)
	if !used || fmt.Sprint(wantErr) != fmt.Sprint(gotErr) || want != got {
		t.Fatalf("informational anchor: full=%+v %v closure=%+v used=%t %v", want, wantErr, got, used, gotErr)
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
	// Local drift must also reject a later dependent successor atomically.
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

func TestMixedSupersessionAdditionsMatchFullImport(t *testing.T) {
	ctx, source, sourceID := indexedTestProject(t, "mixed-edge-source")
	_, indexed, indexedID := indexedTestProject(t, "mixed-edge-indexed")
	_, full, fullID := indexedTestProject(t, "mixed-edge-full")
	appendRecord := func(id, parent string) {
		t.Helper()
		_, err := (worknarrative.Service{DB: source}).Append(ctx, worknarrative.Record{ID: id, ProjectID: sourceID, Title: id, Trigger: "manual", Creator: "test", SupersedesID: parent, Claims: []worknarrative.Claim{{Kind: "outcome", Text: "Observation", Authority: "caller_asserted"}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	appendRecord("root", "")
	appendRecord("initial", "root")
	initial, err := portable.EncodeMemoryExport(ctx, source, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	block, err := EncodeBlock(lineage, initial.Envelopes)
	if err != nil {
		t.Fatal(err)
	}
	_, branch, branchID := indexedTestProject(t, "mixed-edge-branch")
	if _, err := (Importer{DB: branch, ProjectID: branchID}).Import(ctx, block.Objects); err != nil {
		t.Fatal(err)
	}
	if _, err := (worknarrative.Service{DB: branch}).Append(ctx, worknarrative.Record{ID: "sibling", ProjectID: branchID, Title: "sibling", Trigger: "manual", Creator: "test", SupersedesID: "initial", Claims: []worknarrative.Claim{{Kind: "outcome", Text: "Observation", Authority: "caller_asserted"}}}); err != nil {
		t.Fatal(err)
	}
	branchExport, err := portable.EncodeMemoryExport(ctx, branch, branchID)
	if err != nil {
		t.Fatal(err)
	}
	index := Index{DB: indexed, ProjectID: indexedID, Lineage: lineage}
	for i := 0; i < 2; i++ {
		if _, err := (Importer{DB: full, ProjectID: fullID}).Import(ctx, block.Objects); err != nil {
			t.Fatal(err)
		}
		if _, err := index.ImportBlocks(ctx, []Block{block}); err != nil {
			t.Fatal(err)
		}
	}
	// Two successive additions extend the proven chain.
	for _, item := range []struct{ id, parent string }{{"next", "initial"}, {"last", "next"}} {
		appendRecord(item.id, item.parent)
		export, err := portable.EncodeMemoryExport(ctx, source, sourceID)
		if err != nil {
			t.Fatal(err)
		}
		hash, ok := mixedReplaySet(export.Envelopes)
		if !ok {
			t.Fatal("ineligible fixture")
		}
		want, wantErr := (Importer{DB: full, ProjectID: fullID}).Import(ctx, export.Envelopes)
		got, used, gotErr := index.tryMixedAdditions(ctx, hash, export.Envelopes)
		if !used || fmt.Sprint(wantErr) != fmt.Sprint(gotErr) || want != got {
			t.Fatalf("%s: full=%+v %v incremental=%+v used=%t %v", item.id, want, wantErr, got, used, gotErr)
		}
		if _, proof, err := index.mixedReplaySnapshot(ctx, hash, export.Envelopes, ""); err != nil || !proof {
			t.Fatalf("%s: missing proof: %v", item.id, err)
		}
	}
	// A single import can extend the proven chain by multiple linked records.
	appendRecord("batch-first", "last")
	appendRecord("batch-second", "batch-first")
	batch, err := portable.EncodeMemoryExport(ctx, source, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	batchHash, ok := mixedReplaySet(batch.Envelopes)
	if !ok {
		t.Fatal("ineligible batch fixture")
	}
	wantBatch, wantBatchErr := (Importer{DB: full, ProjectID: fullID}).Import(ctx, batch.Envelopes)
	gotBatch, used, gotBatchErr := index.tryMixedAdditions(ctx, batchHash, batch.Envelopes)
	if !used || fmt.Sprint(wantBatchErr) != fmt.Sprint(gotBatchErr) || wantBatch != gotBatch {
		t.Fatalf("batch: full=%+v %v incremental=%+v used=%t %v", wantBatch, wantBatchErr, gotBatch, used, gotBatchErr)
	}
	if _, proof, err := index.mixedReplaySnapshot(ctx, batchHash, batch.Envelopes, ""); err != nil || !proof {
		t.Fatalf("batch: missing proof: %v", err)
	}
	// Merge a sibling from a second origin to produce a supersession fork.
	export, err := portable.EncodeMemoryExport(ctx, source, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range branchExport.Envelopes {
		if object.Envelope.RecordID == "sibling" {
			export.Envelopes = append(export.Envelopes, object)
		}
	}
	hash, ok := mixedReplaySet(export.Envelopes)
	if !ok {
		t.Fatal("ineligible fork fixture")
	}
	want, wantErr := (Importer{DB: full, ProjectID: fullID}).Import(ctx, export.Envelopes)
	got, used, gotErr := index.tryMixedAdditions(ctx, hash, export.Envelopes)
	if !used || fmt.Sprint(wantErr) != fmt.Sprint(gotErr) || want != got || got.SupersessionForks == 0 {
		t.Fatalf("fork: full=%+v %v incremental=%+v used=%t %v", want, wantErr, got, used, gotErr)
	}
}
