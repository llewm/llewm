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
		})
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
