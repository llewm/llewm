package recordsync

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/fork"
	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/topic"
	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
)

func indexedTestProject(t testing.TB, name string) (context.Context, *sql.DB, string) {
	t.Helper()
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), name)
	db, err := database.Initialize(ctx, filepath.Join(root, "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	id, err := (activity.Store{DB: db}).EnsureProject(ctx, root, name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (portable.Service{DB: db}).Initialize(ctx, id, lineage); err != nil {
		t.Fatal(err)
	}
	return ctx, db, id
}

func TestBlockImportMatchesFullScanForFork(t *testing.T) {
	ctx, a, aid := indexedTestProject(t, "fork-source-a")
	_, b, bid := indexedTestProject(t, "fork-source-b")
	_, full, fullID := indexedTestProject(t, "fork-full")
	_, indexed, indexedID := indexedTestProject(t, "fork-indexed")
	record := func(db *sql.DB, project, title string) {
		t.Helper()
		if _, err := (topic.Service{DB: db}).Record(ctx, topic.Topic{ProjectID: project, ID: "shared", Title: title, Creator: "test"}); err != nil {
			t.Fatal(err)
		}
	}
	record(a, aid, "Base")
	base, err := portable.EncodeMemoryExport(ctx, a, aid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (Importer{DB: b, ProjectID: bid}).Import(ctx, base.Envelopes); err != nil {
		t.Fatal(err)
	}
	record(a, aid, "Version A")
	record(b, bid, "Version B")
	var blocks []Block
	seen := map[string]bool{}
	for _, source := range []struct {
		db *sql.DB
		id string
	}{{a, aid}, {b, bid}} {
		export, err := portable.EncodeMemoryExport(ctx, source.db, source.id)
		if err != nil {
			t.Fatal(err)
		}
		for _, object := range export.Envelopes {
			if seen[object.Envelope.EnvelopeSHA256] {
				continue
			}
			seen[object.Envelope.EnvelopeSHA256] = true
			block, err := EncodeBlock(lineage, []portable.EncodedEnvelope{object})
			if err != nil {
				t.Fatal(err)
			}
			blocks = append(blocks, block)
		}
	}
	index := Index{DB: indexed, ProjectID: indexedID, Lineage: lineage}
	root, err := OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	var scanned []Block
	for _, block := range blocks {
		if _, err := PublishBlock(root, lineage, block); err != nil {
			t.Fatal(err)
		}
		path, err := blockPath(lineage, block.Hash)
		if err != nil {
			t.Fatal(err)
		}
		read, err := index.ReadBlock(ctx, root, path)
		if err != nil {
			t.Fatal(err)
		}
		scanned = append(scanned, read)
	}
	for round := 0; round < 2; round++ {
		var objects []portable.EncodedEnvelope
		for _, block := range blocks {
			objects = append(objects, block.Objects...)
		}
		for _, object := range objects {
			if object.Envelope.RecordType != "topic_revision" || object.Envelope.RecordRevision != 2 {
				continue
			}
			selected, complete := dependencyClosure(ctx, objects, []portable.EncodedEnvelope{object})
			if !complete || len(selected) != 2 {
				t.Fatalf("closure separated competing revisions: complete=%v count=%d", complete, len(selected))
			}
		}
		want, wantErr := (Importer{DB: full, ProjectID: fullID}).Import(ctx, objects)
		input := blocks
		if round == 1 {
			input = scanned
		}
		got, gotErr := index.ImportBlocks(ctx, input)
		if fmt.Sprint(wantErr) != fmt.Sprint(gotErr) || want != got {
			t.Fatalf("round %d: full=%+v %v indexed=%+v %v", round, want, wantErr, got, gotErr)
		}
		fullForks, err := fork.RevisionForks(ctx, full, fullID, "")
		if err != nil {
			t.Fatal(err)
		}
		indexedForks, err := fork.RevisionForks(ctx, indexed, indexedID, "")
		if err != nil {
			t.Fatal(err)
		}
		if len(fullForks) != 1 || len(indexedForks) != 1 || fullForks[0].Ref() != indexedForks[0].Ref() || len(fullForks[0].Heads) != 2 || len(indexedForks[0].Heads) != 2 {
			t.Fatalf("round %d: forks differ: %+v %+v", round, fullForks, indexedForks)
		}
		headHashes := func(value fork.RevisionFork) []string {
			var result []string
			for _, head := range value.Heads {
				result = append(result, head.ContentSHA256)
			}
			sort.Strings(result)
			return result
		}
		if fmt.Sprint(headHashes(fullForks[0])) != fmt.Sprint(headHashes(indexedForks[0])) {
			t.Fatalf("round %d: fork heads differ", round)
		}
	}
}

func TestBlockImportMatchesFullScan(t *testing.T) {
	ctx, source, sourceID := indexedTestProject(t, "source")
	_, full, fullID := indexedTestProject(t, "full")
	_, indexed, indexedID := indexedTestProject(t, "indexed")
	service := worknarrative.Service{DB: source}
	for _, record := range []worknarrative.Record{
		{ID: "first", ProjectID: sourceID, Title: "First", Trigger: "manual", Creator: "test", Claims: []worknarrative.Claim{{Kind: "outcome", Text: "A first observation", Authority: "caller_asserted"}}},
		{ID: "second", ProjectID: sourceID, Title: "Second", Trigger: "manual", Creator: "test", SupersedesID: "first", Claims: []worknarrative.Claim{{Kind: "outcome", Text: "A correction", Authority: "caller_asserted"}}},
	} {
		if _, err := service.Append(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	export, err := portable.EncodeMemoryExport(ctx, source, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	var blocks []Block
	for _, object := range export.Envelopes {
		block, err := EncodeBlock(lineage, []portable.EncodedEnvelope{object})
		if err != nil {
			t.Fatal(err)
		}
		blocks = append(blocks, block)
	}
	index := Index{DB: indexed, ProjectID: indexedID, Lineage: lineage}
	bad := blocks[0]
	bad.Bytes = append([]byte(nil), bad.Bytes...)
	bad.Bytes[len(bad.Bytes)-2] ^= 1
	if _, err := index.ImportBlocks(ctx, []Block{bad}); err == nil {
		t.Fatal("accepted block bytes that do not match the hash")
	}
	var edgeOnly []Block
	for _, block := range blocks {
		if block.Objects[0].Envelope.RecordType == "work_narrative_supersession" {
			edgeOnly = append(edgeOnly, block)
		}
	}
	if len(edgeOnly) != 1 {
		t.Fatalf("expected one supersession edge, got %d", len(edgeOnly))
	}
	for round := 0; round < 5; round++ {
		selected := blocks
		if round == 0 {
			selected = edgeOnly // Its dependencies arrive in the next round.
		}
		if round == 3 {
			if _, err := indexed.ExecContext(ctx, `DELETE FROM history_index_records WHERE project_id=? AND block_hash=?`, indexedID, blocks[0].Hash); err != nil {
				t.Fatal(err)
			}
		}
		if round == 4 {
			if _, err := indexed.ExecContext(ctx, `UPDATE history_index_blocks SET index_version=0 WHERE project_id=?`, indexedID); err != nil {
				t.Fatal(err)
			}
		}
		var objects []portable.EncodedEnvelope
		for _, block := range selected {
			objects = append(objects, block.Objects...)
		}
		want, wantErr := (Importer{DB: full, ProjectID: fullID}).Import(ctx, objects)
		got, gotErr := index.ImportBlocks(ctx, selected)
		if fmt.Sprint(wantErr) != fmt.Sprint(gotErr) || want != got {
			t.Fatalf("round %d: full=%+v %v indexed=%+v %v", round, want, wantErr, got, gotErr)
		}
		if round == 1 && (wantErr != nil || want.ImportedRecords != len(export.Envelopes)) {
			t.Fatalf("late dependencies did not import the complete history: %+v %v", want, wantErr)
		}
		if round > 1 && (wantErr != nil || want.ImportedRecords != 0) {
			t.Fatalf("replay imported records again: %+v %v", want, wantErr)
		}
		fullExport, err := portable.EncodeMemoryExport(ctx, full, fullID)
		if err != nil {
			t.Fatal(err)
		}
		indexedExport, err := portable.EncodeMemoryExport(ctx, indexed, indexedID)
		if err != nil {
			t.Fatal(err)
		}
		canonical := func(es []portable.EncodedEnvelope) [][]byte {
			result := make([][]byte, len(es))
			for i, e := range es {
				result[i] = e.Bytes
			}
			sort.Slice(result, func(i, j int) bool { return bytes.Compare(result[i], result[j]) < 0 })
			return result
		}
		a, b := canonical(fullExport.Envelopes), canonical(indexedExport.Envelopes)
		if len(a) != len(b) {
			t.Fatalf("round %d: output counts differ: %d %d", round, len(a), len(b))
		}
		for i := range a {
			if !bytes.Equal(a[i], b[i]) {
				t.Fatalf("round %d: output %d differs", round, i)
			}
		}
	}
}

func TestMixedReplayProofInvalidatesOnLocalChange(t *testing.T) {
	ctx, source, sourceID := indexedTestProject(t, "mixed-source")
	_, indexed, indexedID := indexedTestProject(t, "mixed-indexed")
	_, full, fullID := indexedTestProject(t, "mixed-full")
	appendRecord := func(db *sql.DB, project, id, parent string) {
		t.Helper()
		_, err := (worknarrative.Service{DB: db}).Append(ctx, worknarrative.Record{ID: id, ProjectID: project, Title: id, Trigger: "manual", Creator: "test", SupersedesID: parent, Claims: []worknarrative.Claim{{Kind: "outcome", Text: "An observation", Authority: "caller_asserted"}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	appendRecord(source, sourceID, "first", "")
	appendRecord(source, sourceID, "second", "first")
	export, err := portable.EncodeMemoryExport(ctx, source, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	block, err := EncodeBlock(lineage, export.Envelopes)
	if err != nil {
		t.Fatal(err)
	}
	index := Index{DB: indexed, ProjectID: indexedID, Lineage: lineage}
	for i := 0; i < 3; i++ {
		want, wantErr := (Importer{DB: full, ProjectID: fullID}).Import(ctx, block.Objects)
		got, gotErr := index.ImportBlocks(ctx, []Block{block})
		if fmt.Sprint(wantErr) != fmt.Sprint(gotErr) || want != got {
			t.Fatalf("round %d: full=%+v %v indexed=%+v %v", i, want, wantErr, got, gotErr)
		}
	}
	setHash, ok := mixedReplaySet(block.Objects)
	if !ok {
		t.Fatal("mixed dependency fixture was ineligible")
	}
	if _, proof, err := index.mixedReplaySnapshot(ctx, setHash, block.Objects, ""); err != nil || !proof {
		t.Fatalf("stable replay proof missing: %v", err)
	}
	appendRecord(indexed, indexedID, "local-change", "")
	appendRecord(full, fullID, "local-change", "")
	if _, proof, err := index.mixedReplaySnapshot(ctx, setHash, block.Objects, ""); err != nil || proof {
		t.Fatalf("local change did not invalidate proof: %v", err)
	}
	want, wantErr := (Importer{DB: full, ProjectID: fullID}).Import(ctx, block.Objects)
	got, gotErr := index.ImportBlocks(ctx, []Block{block})
	if fmt.Sprint(wantErr) != fmt.Sprint(gotErr) || want != got {
		t.Fatalf("replay after invalidation: full=%+v %v indexed=%+v %v", want, wantErr, got, gotErr)
	}
}

func TestMixedReplayRefusesForksAndRecoveries(t *testing.T) {
	makeObject := func(kind, id, payload string) portable.EncodedEnvelope {
		t.Helper()
		object, err := portable.EncodeEnvelope(portable.EnvelopeSpec{LineageID: lineage, RecordType: kind, RecordSchemaVersion: 1, RecordID: id, RecordRevision: 1, RecordedAt: time.Now().UTC(), Authority: "caller_asserted", Payload: []byte(payload)})
		if err != nil {
			t.Fatal(err)
		}
		return object
	}
	first := makeObject("decision", "fork", `{"head":1}`)
	second := makeObject("decision", "fork", `{"head":2}`)
	if _, eligible := mixedReplaySet([]portable.EncodedEnvelope{first, second}); eligible {
		t.Fatal("competing identity accepted for fast replay")
	}
	recovery := makeObject("redaction_recovery", "recovery", `{}`)
	if _, eligible := mixedReplaySet([]portable.EncodedEnvelope{first, recovery}); eligible {
		t.Fatal("recovery accepted for fast replay")
	}
}

func TestIndependentNarrativeReplayMatchesFullScan(t *testing.T) {
	ctx, source, sourceID := indexedTestProject(t, "independent-source")
	_, full, fullID := indexedTestProject(t, "independent-full")
	_, indexed, indexedID := indexedTestProject(t, "independent-indexed")
	// The fast path must not ask the pool for another connection while it
	// holds the transaction used for its local-state comparison.
	indexed.SetMaxOpenConns(1)
	service := worknarrative.Service{DB: source}
	appendRecord := func(id string) {
		t.Helper()
		_, err := service.Append(ctx, worknarrative.Record{ID: id, ProjectID: sourceID, Title: id, Trigger: "manual", Creator: "test", Claims: []worknarrative.Claim{{Kind: "outcome", Text: "Observation", Authority: "caller_asserted"}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	appendRecord("first")
	index := Index{DB: indexed, ProjectID: indexedID, Lineage: lineage}
	for round := 0; round < 5; round++ {
		if round == 2 {
			appendRecord("second")
		}
		export, err := portable.EncodeMemoryExport(ctx, source, sourceID)
		if err != nil {
			t.Fatal(err)
		}
		block, err := EncodeBlock(lineage, export.Envelopes)
		if err != nil {
			t.Fatal(err)
		}
		setHash, eligible := independentNarrativeSet(block.Objects)
		if !eligible {
			t.Fatal("independent narrative fixture was not eligible")
		}
		if round == 4 {
			if _, err := indexed.ExecContext(ctx, `UPDATE history_index_valid_sets SET index_version=0 WHERE project_id=?`, indexedID); err != nil {
				t.Fatal(err)
			}
		}
		ready, err := index.validatedNarrativesPresent(ctx, setHash, block.Objects)
		if err != nil {
			t.Fatal(err)
		}
		if ready != (round == 1 || round == 3) {
			t.Fatalf("round %d: unexpected validation proof: %v", round, ready)
		}
		want, wantErr := (Importer{DB: full, ProjectID: fullID}).Import(ctx, block.Objects)
		got, gotErr := index.ImportBlocks(ctx, []Block{block})
		if fmt.Sprint(wantErr) != fmt.Sprint(gotErr) || want != got {
			t.Fatalf("round %d: full=%+v %v indexed=%+v %v", round, want, wantErr, got, gotErr)
		}
		expectedImports := 0
		if round == 0 || round == 2 {
			expectedImports = 1
		}
		if wantErr != nil || want.ImportedRecords != expectedImports {
			t.Fatalf("round %d: unexpected full scan outcome: %+v %v", round, want, wantErr)
		}
	}
	// A previously validated set must not hide local corruption discovered by
	// the full import planner.
	for _, target := range []struct {
		db *sql.DB
		id string
	}{{full, fullID}, {indexed, indexedID}} {
		if _, err := target.db.ExecContext(ctx, `UPDATE work_narratives SET payload_json='{}' WHERE project_id=? AND id='first'`, target.id); err != nil {
			t.Fatal(err)
		}
	}
	export, err := portable.EncodeMemoryExport(ctx, source, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	block, err := EncodeBlock(lineage, export.Envelopes)
	if err != nil {
		t.Fatal(err)
	}
	want, wantErr := (Importer{DB: full, ProjectID: fullID}).Import(ctx, block.Objects)
	got, gotErr := index.ImportBlocks(ctx, []Block{block})
	if wantErr == nil || fmt.Sprint(wantErr) != fmt.Sprint(gotErr) || want != got {
		t.Fatalf("local corruption: full=%+v %v indexed=%+v %v", want, wantErr, got, gotErr)
	}
}

// Both cases replay the same history. The block-backed path can skip semantic
// reprocessing only for previously validated independent narrative sets.
func BenchmarkBlockImportUnchanged(b *testing.B) {
	for _, size := range []int{500, 5669} {
		b.Run(fmt.Sprintf("records_%d", size), func(b *testing.B) {
			ctx, source, sourceID := indexedTestProject(b, "benchmark-source")
			service := worknarrative.Service{DB: source}
			for i := 0; i < size; i++ {
				_, err := service.Append(ctx, worknarrative.Record{ID: fmt.Sprintf("narrative-%06d", i), ProjectID: sourceID, Title: "Benchmark record", Trigger: "manual", Creator: "benchmark", Claims: []worknarrative.Claim{{Kind: "outcome", Text: "A representative observation for history sync.", Authority: "caller_asserted"}}})
				if err != nil {
					b.Fatal(err)
				}
			}
			export, err := portable.EncodeMemoryExport(ctx, source, sourceID)
			if err != nil {
				b.Fatal(err)
			}
			var blocks []Block
			for start := 0; start < len(export.Envelopes); start += 100 {
				end := min(start+100, len(export.Envelopes))
				block, err := EncodeBlock(lineage, export.Envelopes[start:end])
				if err != nil {
					b.Fatal(err)
				}
				blocks = append(blocks, block)
			}
			var objects []portable.EncodedEnvelope
			for _, block := range blocks {
				objects = append(objects, block.Objects...)
			}
			b.Run("full_scan", func(b *testing.B) {
				_, db, id := indexedTestProject(b, "benchmark-full")
				importer := Importer{DB: db, ProjectID: id}
				if result, err := importer.Import(ctx, objects); err != nil || result.ImportedRecords != len(objects) {
					b.Fatalf("initial import: %+v %v", result, err)
				}
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if _, err := importer.Import(ctx, objects); err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run("blocks_indexed", func(b *testing.B) {
				_, db, id := indexedTestProject(b, "benchmark-blocks")
				index := Index{DB: db, ProjectID: id, Lineage: lineage}
				if result, err := index.ImportBlocks(ctx, blocks); err != nil || result.ImportedRecords != len(objects) {
					b.Fatalf("initial import: %+v %v", result, err)
				}
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if _, err := index.ImportBlocks(ctx, blocks); err != nil {
						b.Fatal(err)
					}
				}
			})
			// Prepare a corpus with one record withheld, then time its arrival.
			// Setup and initial import are outside the timer; the measured work
			// includes planning and applying the new record.
			oldObjects := objects[:len(objects)-1]
			var oldBlocks []Block
			for start := 0; start < len(oldObjects); start += 100 {
				end := min(start+100, len(oldObjects))
				block, err := EncodeBlock(lineage, oldObjects[start:end])
				if err != nil {
					b.Fatal(err)
				}
				oldBlocks = append(oldBlocks, block)
			}
			newBlock, err := EncodeBlock(lineage, objects[len(objects)-1:])
			if err != nil {
				b.Fatal(err)
			}
			changedBlocks := append(append([]Block(nil), oldBlocks...), newBlock)
			b.Run("full_scan_one_new", func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					b.StopTimer()
					_, db, id := indexedTestProject(b, "benchmark-full-change")
					importer := Importer{DB: db, ProjectID: id}
					if result, err := importer.Import(ctx, oldObjects); err != nil || result.ImportedRecords != len(oldObjects) {
						b.Fatalf("initial import: %+v %v", result, err)
					}
					b.StartTimer()
					if result, err := importer.Import(ctx, objects); err != nil || result.ImportedRecords != 1 {
						b.Fatalf("changed import: %+v %v", result, err)
					}
				}
			})
			b.Run("blocks_indexed_one_new", func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					b.StopTimer()
					_, db, id := indexedTestProject(b, "benchmark-block-change")
					index := Index{DB: db, ProjectID: id, Lineage: lineage}
					if result, err := index.ImportBlocks(ctx, oldBlocks); err != nil || result.ImportedRecords != len(oldObjects) {
						b.Fatalf("initial import: %+v %v", result, err)
					}
					b.StartTimer()
					if result, err := index.ImportBlocks(ctx, changedBlocks); err != nil || result.ImportedRecords != 1 {
						b.Fatalf("changed import: %+v %v", result, err)
					}
				}
			})
		})
	}
}
