package portable

import (
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/attribution"
	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
)

func TestMemoryExportIncludesInvocationAncestorsOnly(t *testing.T) {
	ctx, source, sourceID := operationalProject(t, "invocation-source")
	_, target, targetID := operationalProject(t, "invocation-target")
	lineage := "lineage:abababababababababababababababab"
	for _, p := range []struct {
		s  Service
		id string
	}{{Service{DB: source}, sourceID}, {Service{DB: target}, targetID}} {
		if _, err := p.s.Initialize(ctx, p.id, lineage); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Now()
	for _, item := range []struct{ id, parent string }{{"root", ""}, {"child", "root"}, {"leaf", "child"}, {"unrelated", ""}} {
		if _, err := (attribution.Service{DB: source}).AppendInvocation(ctx, attribution.Invocation{ID: item.id, ProjectID: sourceID, ParentInvocationID: item.parent, ActorType: "agent", IdentitySource: "caller_asserted", OccurredAt: at, ReceivedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := (worknarrative.Service{DB: source}).Append(ctx, worknarrative.Record{ID: "work", ProjectID: sourceID, Title: "Work", Trigger: "manual", Creator: "test", CreatorInvocationID: "leaf", Claims: []worknarrative.Claim{{Kind: "outcome", Text: "Observation", Authority: "caller_asserted"}}}); err != nil {
		t.Fatal(err)
	}
	export, err := EncodeMemoryExport(ctx, source, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	actors := map[string]bool{}
	var specs []EnvelopeSpec
	for _, object := range export.Envelopes {
		if object.Envelope.RecordType == "actor_invocation" {
			actors[object.Envelope.RecordID] = true
		}
		spec, err := DecodeMemoryEnvelope(object.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		specs = append(specs, spec)
	}
	if len(actors) != 3 || !actors["root"] || !actors["child"] || !actors["leaf"] || actors["unrelated"] {
		t.Fatalf("wrong attribution closure: %v", actors)
	}
	plan, receipt, err := ImportMemoryRecords(ctx, target, targetID, specs, time.Now())
	if err != nil || !plan.ApplyAllowed || receipt == nil {
		t.Fatalf("fresh import lacks parent dependencies: %+v %v", plan, err)
	}
}
