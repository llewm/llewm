package recordsync

import (
	"bytes"
	"database/sql"
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/decision"
	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/resource"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

func TestSemanticJournalRevisitsAffectedResourceNotesAndMatchesReference(t *testing.T) {
	f := newReplayFixture(t)
	ctx, reference, referenceProject := indexedTestProject(t, "replay-reference")
	for i := 0; i < 100; i++ {
		if _, err := (decision.Service{DB: f.source}).Append(ctx, decision.Record{ProjectID: f.sourceProject, ID: fmt.Sprintf("unrelated-%d", i), Title: "Independent", Rationale: "Observation", Disposition: "accepted", Actor: "owner", ActorType: "human", Authority: "caller_asserted"}); err != nil {
			t.Fatal(err)
		}
	}
	resources := resource.Service{DB: f.source}
	if _, err := resources.Record(ctx, resource.Resource{ProjectID: f.sourceProject, ID: "host", Kind: "host", Title: "Host", Creator: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := resources.AddNote(ctx, resource.Note{ProjectID: f.sourceProject, ResourceID: "host", Text: "Depends on the resource identity", Authority: "machine_observed", Creator: "test"}); err != nil {
		t.Fatal(err)
	}
	baseline := f.captureSource(t)
	f.stage(t)
	if result, err := f.replay(t); err != nil || result.ImportedRecords != len(baseline) {
		t.Fatalf("baseline: %+v %v", result, err)
	}
	full := Importer{DB: reference, ProjectID: referenceProject}
	if _, err := full.Import(ctx, baseline); err != nil {
		t.Fatal(err)
	}
	if _, err := resources.Record(ctx, resource.Resource{ProjectID: f.sourceProject, ID: "host", Kind: "host", Title: "Host", Summary: "New revision", Creator: "test"}); err != nil {
		t.Fatal(err)
	}
	current := f.captureSource(t)
	f.stage(t)
	measured, metrics := syncwork.WithWorkMetrics(ctx)
	f.ctx = measured
	got, err := f.replay(t)
	if err != nil {
		t.Fatal(err)
	}
	want, err := full.Import(ctx, current)
	if err != nil || got != want || got.ImportedRecords != 1 {
		t.Fatalf("affected replay differs from reference: %+v/%+v %v", got, want, err)
	}
	counts := metrics.Counts()
	if counts["semantic_journal_delta_cycles"] != 1 || counts["fallback_semantic_journal_full"] != 0 || counts["dependency_selected_records"] != 3 || counts["dependency_membership_records"] != 3 || counts["archive_envelopes_read"] != 3 || counts["operational_full_loads"] != 0 || counts["local_state_records_visited"] != 0 {
		t.Fatalf("resource arrival visited unrelated history: %v", counts)
	}
	actual, err := portable.EncodeMemoryExport(ctx, f.store.DB, f.store.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := portable.EncodeMemoryExport(ctx, reference, referenceProject)
	if err != nil || len(actual.Envelopes) != len(expected.Envelopes) {
		t.Fatalf("export sizes differ: %d/%d %v", len(actual.Envelopes), len(expected.Envelopes), err)
	}
	byHash := map[string][]byte{}
	for _, object := range expected.Envelopes {
		byHash[object.Envelope.EnvelopeSHA256] = object.Bytes
	}
	for _, object := range actual.Envelopes {
		if !bytes.Equal(object.Bytes, byHash[object.Envelope.EnvelopeSHA256]) {
			t.Fatal("affected replay changed canonical wire bytes")
		}
	}
}

func TestReplayPointTraversalMatchesIndependentTypedGraph(t *testing.T) {
	f := journalFixture(t)
	rng := rand.New(rand.NewSource(813))
	kinds := []string{"decision", "resource", "track_plan"}
	for trial := 0; trial < 20; trial++ {
		var objects []portable.EncodedEnvelope
		values := map[string]indexedDependencies{}
		for i := 0; i < 40; i++ {
			kind := kinds[rng.Intn(len(kinds))]
			object, err := portable.EncodeEnvelope(portable.EnvelopeSpec{LineageID: lineage, RecordType: kind, RecordSchemaVersion: 1, RecordID: fmt.Sprintf("trial-%d-id-%d", trial, rng.Intn(4)), RecordRevision: 1 + rng.Intn(8), RecordedAt: time.Unix(int64(trial*100+i), 0), Authority: "caller_asserted", Payload: []byte(fmt.Sprintf(`{"fixture":%d}`, i))})
			if err != nil {
				t.Fatal(err)
			}
			objects = append(objects, object)
			unique := map[portable.ImportDependency]bool{}
			for j := rng.Intn(3); j > 0; j-- {
				unique[portable.ImportDependency{RecordType: kinds[rng.Intn(len(kinds))], RecordID: fmt.Sprintf("trial-%d-id-%d", trial, rng.Intn(5)), Mode: []string{portable.DependencyExact, portable.DependencyAny, portable.DependencyPrior}[rng.Intn(3)], Revision: 1 + rng.Intn(10)}] = true
			}
			var selectors []portable.ImportDependency
			for selector := range unique {
				selectors = append(selectors, selector)
			}
			sort.Slice(selectors, func(i, j int) bool {
				a, b := selectors[i], selectors[j]
				if a.RecordType != b.RecordType {
					return a.RecordType < b.RecordType
				}
				if a.RecordID != b.RecordID {
					return a.RecordID < b.RecordID
				}
				if a.Mode != b.Mode {
					return a.Mode < b.Mode
				}
				return a.Revision < b.Revision
			})
			values[object.Envelope.EnvelopeSHA256] = indexedDependencies{Supported: true, Selectors: selectors}
		}
		// Synthetic, preverified selectors isolate graph discovery from payload
		// decoding. This is not a semantic import receipt or baseline seed.
		if err := f.archive.CaptureLocal(objects); err != nil {
			t.Fatal(err)
		}
		block, err := EncodeBlock(lineage, objects)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.index.AddBlock(f.ctx, block); err != nil {
			t.Fatal(err)
		}
		if err := f.index.saveDependencySelectors(f.ctx, values); err != nil {
			t.Fatal(err)
		}
		seed := objects[rng.Intn(len(objects))]
		selected := map[string]bool{seed.Envelope.EnvelopeSHA256: true}
		matches := func(selector portable.ImportDependency, e portable.Envelope) bool {
			return selector.RecordType == e.RecordType && selector.RecordID == e.RecordID && (selector.Mode == portable.DependencyAny || selector.Mode == portable.DependencyExact && selector.Revision == e.RecordRevision || selector.Mode == portable.DependencyPrior && e.RecordRevision < selector.Revision)
		}
		for {
			before := len(selected)
			for _, a := range objects {
				for _, b := range objects {
					linked := a.Envelope.RecordType == b.Envelope.RecordType && a.Envelope.RecordID == b.Envelope.RecordID && a.Envelope.RecordRevision == b.Envelope.RecordRevision
					for _, selector := range values[a.Envelope.EnvelopeSHA256].Selectors {
						linked = linked || matches(selector, b.Envelope)
					}
					if linked && (selected[a.Envelope.EnvelopeSHA256] || selected[b.Envelope.EnvelopeSHA256]) {
						selected[a.Envelope.EnvelopeSHA256], selected[b.Envelope.EnvelopeSHA256] = true, true
					}
				}
			}
			if before == len(selected) {
				break
			}
		}
		var got []portable.EncodedEnvelope
		err = f.store.transact(f.ctx, func(tx *sql.Tx) error {
			points := &replayObjectSet{archive: f.archive, tx: tx, objects: map[string]portable.EncodedEnvelope{}}
			var err error
			got, err = f.index.replayDependencyClosureWithin(f.ctx, tx, points, []string{seed.Envelope.EnvelopeSHA256}, nil)
			return err
		})
		var wantHashes, gotHashes []string
		for hash := range selected {
			wantHashes = append(wantHashes, hash)
		}
		sort.Strings(wantHashes)
		for _, object := range got {
			gotHashes = append(gotHashes, object.Envelope.EnvelopeSHA256)
		}
		if err != nil || !reflect.DeepEqual(gotHashes, wantHashes) {
			t.Fatalf("point trial%d differs: %v/%v %v", trial, gotHashes, wantHashes, err)
		}
	}
}
