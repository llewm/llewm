package portable

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/trackplan"
)

func TestTrackPlanTextValidationMatchesUncachedPredicate(t *testing.T) {
	values := []string{"", "text", " text", "text ", "\x00", "\xff", "line\ntext", "😀", "[PROJECT_ROOT]", "token=not-for-persistence", strings.Repeat("x", trackPlanTextMaxBytes), strings.Repeat("x", trackPlanTextMaxBytes+1)}
	cache := newTrackPlanTextValidation()
	check := func(value string) {
		t.Helper()
		for _, required := range []bool{false, true, false, true} {
			if got, want := cache.valid(value, required), validTrackPlanText(value, required); got != want {
				t.Fatalf("required=%t value length%d: got%t want%t", required, len(value), got, want)
			}
		}
	}
	for _, value := range values {
		check(value)
	}
	for i := 0; i < trackPlanValidationMaxEntries+10; i++ {
		check(fmt.Sprintf("entry-%d", i))
	}
	if len(cache.values) != trackPlanValidationMaxEntries || cache.bytes > trackPlanValidationMaxBytes {
		t.Fatalf("entry bounds violated: entries%d bytes%d", len(cache.values), cache.bytes)
	}
	for _, value := range values {
		check(value)
	}
	cache = newTrackPlanTextValidation()
	for i := 0; i < 100; i++ {
		check(fmt.Sprintf("%03d", i) + strings.Repeat("x", trackPlanTextMaxBytes-3))
	}
	if cache.bytes > trackPlanValidationMaxBytes || len(cache.values) != 64 {
		t.Fatalf("byte bound violated: entries%d bytes%d", len(cache.values), cache.bytes)
	}
	check("")
}

func TestTrackPlanCachedValidationPreservesPayloadErrors(t *testing.T) {
	value := trackplan.Plan{ID: "plan", Revision: 1, RoadmapNodeID: "track", Title: "Plan", Summary: "Observation", Status: "active", Creator: "owner", Authority: "caller_asserted", RecordedAt: time.Unix(1, 0), SchemaVersion: 1, Items: []trackplan.Item{{ID: "first", Position: 1, Title: "Item", Status: "active"}, {ID: "second", Position: 2, Title: "Item", Status: "active"}}}
	spec, err := trackPlanSpec("lineage:55555555555555555555555555555555", "project-test", "/project", value)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeEnvelope(spec)
	if err != nil {
		t.Fatal(err)
	}
	cache := newTrackPlanTextValidation()
	for index, mutate := range []func(*Envelope, *trackPlanPayload){
		func(*Envelope, *trackPlanPayload) {},
		func(_ *Envelope, p *trackPlanPayload) { p.Title = "" },
		func(_ *Envelope, p *trackPlanPayload) { p.Summary = " whitespace" },
		func(_ *Envelope, p *trackPlanPayload) { p.Items[0].Detail = "token=not-for-persistence" },
		func(_ *Envelope, p *trackPlanPayload) { p.Items[0].ParentID = "first" },
		func(_ *Envelope, p *trackPlanPayload) {
			p.Items[0].DependsOn = []string{"second"}
			p.Items[1].DependsOn = []string{"first"}
		},
		func(_ *Envelope, p *trackPlanPayload) { p.Items[1].Position = 1 },
		func(e *Envelope, _ *trackPlanPayload) { e.Refs = nil },
		func(e *Envelope, _ *trackPlanPayload) { e.Index = json.RawMessage(`{}`) },
		func(e *Envelope, _ *trackPlanPayload) { e.Authority = "machine_observed" },
	} {
		e := encoded.Envelope
		var payload trackPlanPayload
		if err := json.Unmarshal(spec.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		mutate(&e, &payload)
		want, wantErr := decodeTrackPlan(e, payload, validTrackPlanText)
		if (index == 0) != (wantErr == nil) {
			t.Fatalf("fixture%d error=%v", index, wantErr)
		}
		got, gotErr := decodeTrackPlan(e, payload, cache.valid)
		if fmt.Sprint(gotErr) != fmt.Sprint(wantErr) || !reflect.DeepEqual(got, want) {
			t.Fatalf("cached validation differs: %v / %v", gotErr, wantErr)
		}
	}
}

func FuzzTrackPlanTextValidation(f *testing.F) {
	for _, value := range []string{"", "title", "\xff", "\x00", "token=not-for-persistence", " text", "😀"} {
		f.Add(value, true)
	}
	f.Fuzz(func(t *testing.T, value string, required bool) {
		cache := newTrackPlanTextValidation()
		for _, r := range []bool{!required, required, !required} {
			if got, want := cache.valid(value, r), validTrackPlanText(value, r); got != want {
				t.Fatal("cached predicate differs")
			}
		}
	})
}
