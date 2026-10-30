package recordsync

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/decision"
	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/release"
	"github.com/llewm/llewm/components/continuity/internal/resource"
	"github.com/llewm/llewm/components/continuity/internal/roadmap"
	"github.com/llewm/llewm/components/continuity/internal/trackplan"
	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
)

func TestDependencyClosureTypedRevisionsAndLateTargets(t *testing.T) {
	ctx, db, project := indexedTestProject(t, "dependency-closure")
	for revision := 1; revision <= 2; revision++ {
		if _, err := (resource.Service{DB: db}).Record(ctx, resource.Resource{ProjectID: project, ID: "shared", Kind: "host", Title: fmt.Sprint(revision), Creator: "test"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := (resource.Service{DB: db}).AddNote(ctx, resource.Note{ProjectID: project, ResourceID: "shared", Text: "Observation", Authority: "machine_observed", Creator: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := (decision.Service{DB: db}).Append(ctx, decision.Record{ProjectID: project, ID: "shared", Title: "Independent", Rationale: "Observation", Disposition: "accepted", Actor: "test", ActorType: "human", Authority: "caller_asserted"}); err != nil {
		t.Fatal(err)
	}
	for revision := 1; revision <= 3; revision++ {
		if _, err := (trackplan.Service{DB: db}).AppendPlan(ctx, trackplan.Plan{ProjectID: project, ID: "plan", RoadmapNodeID: "node", TaskID: "task", Title: "Qualification", Summary: fmt.Sprint(revision), Status: "active", Creator: "test", Items: []trackplan.Item{{ID: "item", Position: 1, Title: "Qualify", Status: "active"}}}); err != nil {
			t.Fatal(err)
		}
	}
	exported, err := portable.EncodeMemoryExport(ctx, db, project)
	if err != nil {
		t.Fatal(err)
	}
	index := Index{DB: db, ProjectID: project, Lineage: lineage}
	var latestResource, latestPlan, note portable.EncodedEnvelope
	for _, object := range exported.Envelopes {
		switch object.Envelope.RecordType {
		case "resource":
			if object.Envelope.RecordRevision == 2 {
				latestResource = object
			}
		case "track_plan":
			if object.Envelope.RecordRevision == 3 {
				latestPlan = object
			}
		case "resource_note":
			note = object
		}
	}
	for _, tc := range []struct {
		name  string
		seed  portable.EncodedEnvelope
		kinds map[string]int
	}{
		{"any revision and typed identity", latestResource, map[string]int{"resource": 2, "resource_note": 1}},
		{"transitive plan predecessors", latestPlan, map[string]int{"track_plan": 3}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selected, ok := dependencyClosure(ctx, exported.Envelopes, []portable.EncodedEnvelope{tc.seed})
			if !ok {
				t.Fatal("closure declined valid fixture")
			}
			got := map[string]int{}
			for _, object := range selected {
				got[object.Envelope.RecordType]++
			}
			if !reflect.DeepEqual(got, tc.kinds) {
				t.Fatalf("selected %v, want %v", got, tc.kinds)
			}
			persisted, complete := index.indexedDependencyClosure(ctx, exported.Envelopes, []portable.EncodedEnvelope{tc.seed})
			if !complete || !reflect.DeepEqual(persisted, selected) {
				t.Fatal("persistent selectors differ from canonical graph")
			}
		})
	}
	// A cache hit must work without writing. This distinguishes reuse from a
	// silent re-decode/rebuild on every invocation.
	if _, err := db.ExecContext(ctx, `CREATE TRIGGER reject_dependency_cache_write BEFORE INSERT ON history_index_dependency_records BEGIN SELECT RAISE(ABORT,'unexpected dependency rewrite'); END`); err != nil {
		t.Fatal(err)
	}
	if _, ok := index.indexedDependencyClosure(ctx, exported.Envelopes, []portable.EncodedEnvelope{latestResource}); !ok {
		t.Fatal("complete cache was not reusable")
	}
	if _, err := db.ExecContext(ctx, `DROP TRIGGER reject_dependency_cache_write`); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []string{
		`DELETE FROM history_index_dependencies WHERE target_type='resource'`,
		`UPDATE history_index_dependencies SET target_revision=999 WHERE target_type='resource'`,
		`UPDATE history_index_dependency_records SET supported=0 WHERE supported=1`,
		`UPDATE history_index_dependency_records SET selectors_sha256='damaged'`,
		`UPDATE history_index_dependency_records SET index_version=0`,
		`DELETE FROM history_index_dependency_records`,
	} {
		if _, err := db.ExecContext(ctx, mutation); err != nil {
			t.Fatal(err)
		}
		selected, ok := index.indexedDependencyClosure(ctx, exported.Envelopes, []portable.EncodedEnvelope{latestResource})
		if !ok || len(selected) != 3 {
			t.Fatalf("damaged cache was not repaired: %s (%v %d)", mutation, ok, len(selected))
		}
	}
	// Preserve unresolved selectors so late target arrivals find earlier owners.
	selected, ok := dependencyClosure(ctx, []portable.EncodedEnvelope{note}, []portable.EncodedEnvelope{note})
	if !ok || len(selected) != 1 {
		t.Fatalf("unresolved selector lost: %v %d", ok, len(selected))
	}
	selected, ok = dependencyClosure(ctx, []portable.EncodedEnvelope{note, latestResource}, []portable.EncodedEnvelope{latestResource})
	if !ok || len(selected) != 2 {
		t.Fatalf("late target missed dependent: %v %d", ok, len(selected))
	}
	selected, ok = index.indexedDependencyClosure(ctx, []portable.EncodedEnvelope{note}, []portable.EncodedEnvelope{note})
	if !ok || len(selected) != 1 {
		t.Fatal("cache pulled a target outside the supplied snapshot")
	}
	selected, ok = index.indexedDependencyClosure(ctx, []portable.EncodedEnvelope{note, latestResource}, []portable.EncodedEnvelope{latestResource})
	if !ok || len(selected) != 2 {
		t.Fatal("persistent unresolved selector missed arriving target")
	}
	// A failed rebuild may not leave a partial batch that looks complete.
	if _, err := db.ExecContext(ctx, `DELETE FROM history_index_dependency_records`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TRIGGER reject_dependency_edge BEFORE INSERT ON history_index_dependencies BEGIN SELECT RAISE(ABORT,'fixture failure'); END`); err != nil {
		t.Fatal(err)
	}
	if selected, ok := index.indexedDependencyClosure(ctx, exported.Envelopes, []portable.EncodedEnvelope{latestResource}); ok || selected != nil {
		t.Fatal("failed cache write produced closure")
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM history_index_dependency_records`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial dependency batch: %d %v", count, err)
	}
	if _, err := db.ExecContext(ctx, `DROP TRIGGER reject_dependency_edge`); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if selected, ok := dependencyClosure(canceled, exported.Envelopes, []portable.EncodedEnvelope{note}); ok || selected != nil {
		t.Fatal("cancellation produced partial closure")
	}
	malformed := note
	malformed.Envelope.Payload = []byte(`{}`)
	if selected, ok := dependencyClosure(ctx, []portable.EncodedEnvelope{malformed}, []portable.EncodedEnvelope{malformed}); ok || selected != nil {
		t.Fatal("malformed record produced closure")
	}
}

func TestDependencyClosureExactRevision(t *testing.T) {
	ctx, db, project := indexedTestProject(t, "exact-dependency")
	for revision := 1; revision <= 2; revision++ {
		_, err := db.ExecContext(ctx, `INSERT INTO roadmap_nodes(project_id,node_id,revision,title,status,priority,source_path,authority,recorded_at,schema_version,kind,evidence_refs_json) VALUES(?, 'node',?,'Node','completed',10,'docs/roadmap.md','caller_asserted','2026-01-01T00:00:00Z',1,'work_item','[]')`, project, revision)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := (roadmap.Service{DB: db}).AppendBinding(ctx, roadmap.Binding{ProjectID: project, NodeID: "node", NodeRevision: 1, Path: "example.go", PathKind: "file", Relationship: "implementation"}); err != nil {
		t.Fatal(err)
	}
	exported, err := portable.EncodeMemoryExport(ctx, db, project)
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range exported.Envelopes {
		if object.Envelope.RecordType != "roadmap_node" {
			continue
		}
		selected, ok := dependencyClosure(ctx, exported.Envelopes, []portable.EncodedEnvelope{object})
		want := 1
		if object.Envelope.RecordRevision == 1 {
			want = 2
		}
		if !ok || len(selected) != want {
			t.Fatalf("revision %d selected %d, want %d (complete %v)", object.Envelope.RecordRevision, len(selected), want, ok)
		}
	}
}

func TestDependencyClosurePublishedReleasePriorHead(t *testing.T) {
	ctx, db, project := indexedTestProject(t, "release-dependency")
	if _, err := db.ExecContext(ctx, `INSERT INTO roadmap_nodes(project_id,node_id,revision,title,status,priority,source_path,authority,recorded_at,schema_version,kind,evidence_refs_json) VALUES(?, 'node',1,'Node','completed',10,'docs/roadmap.md','caller_asserted','2026-01-01T00:00:00Z',1,'work_item','[]')`, project); err != nil {
		t.Fatal(err)
	}
	if _, err := (worknarrative.Service{DB: db}).Append(ctx, worknarrative.Record{ID: "narrative", ProjectID: project, Title: "Observation", Trigger: "manual", Creator: "test", Claims: []worknarrative.Claim{{Kind: "outcome", Text: "Completed", Authority: "caller_asserted"}}}); err != nil {
		t.Fatal(err)
	}
	entry := release.Entry{Category: "feature", Wording: "Feature", NodeID: "node", NodeRevision: 1, NarrativeIDs: []string{"narrative"}}
	for _, status := range []string{"draft", "reviewed", "published"} {
		record := release.Record{ProjectID: project, ID: "release", Version: "1.0", Title: "Release", Status: status, Reviewer: "test", Entries: []release.Entry{entry}}
		if status == "published" {
			now := time.Now().UTC()
			record.PublishedAt = &now
		}
		if _, err := (release.Service{DB: db}).Append(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	exported, err := portable.EncodeMemoryExport(ctx, db, project)
	if err != nil {
		t.Fatal(err)
	}
	var published, prior portable.EncodedEnvelope
	var withoutPrior []portable.EncodedEnvelope
	for _, object := range exported.Envelopes {
		if object.Envelope.RecordType == "roadmap_release" && object.Envelope.RecordRevision == 2 {
			prior = object
			continue
		}
		if object.Envelope.RecordType == "roadmap_release" && object.Envelope.RecordRevision == 3 {
			published = object
		}
		withoutPrior = append(withoutPrior, object)
	}
	selectors, ok := portable.ImportDependencies(published.Envelope)
	if !ok {
		t.Fatal("published release unsupported")
	}
	found := false
	for _, selector := range selectors {
		if selector.RecordType == "roadmap_release" && selector.Mode == portable.DependencyPrior && selector.Revision == 3 {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing prior-head selector: %+v", selectors)
	}
	// The missing middle revision must remain covered by the range selector.
	withoutPrior = append(withoutPrior, prior)
	selected, ok := dependencyClosure(ctx, withoutPrior, []portable.EncodedEnvelope{prior})
	if !ok || len(selected) != len(exported.Envelopes) {
		t.Fatalf("late prior head missed closure: %v %d/%d", ok, len(selected), len(exported.Envelopes))
	}
}
