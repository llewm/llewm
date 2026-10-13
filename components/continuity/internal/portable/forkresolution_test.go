package portable

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/fork"
)

func TestRevisionForkResolutionsTravelAsTheirOwnType(t *testing.T) {
	payload := forkResolutionPayload{Family: fork.FamilyRevision, SubjectID: "topic_revision:sync@2", AcceptedHead: strings.Repeat("a", 64), RejectedHeads: []string{strings.Repeat("b", 64)}, ContinuationID: "topic_revision:sync@3", Resolver: "owner", OperationalSchemaVersion: fork.RevisionSchemaVersion}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	index, err := json.Marshal(forkResolutionIndex(payload))
	if err != nil {
		t.Fatal(err)
	}
	encode := func(recordType string) Envelope {
		encoded, err := EncodeEnvelope(EnvelopeSpec{LineageID: "lineage:5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a", RecordType: recordType, RecordSchemaVersion: 1, RecordID: "resolution-1", RecordedAt: time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC), Authority: "user_reported", Index: index, Refs: forkResolutionReferences(payload), Payload: body})
		if err != nil {
			t.Fatal(err)
		}
		return encoded.Envelope
	}
	records := portableForkResolutions{}
	if handled, err := decodeForkResolutionEnvelope(records, encode("revision_fork_resolution")); !handled || err != nil {
		t.Fatalf("revision_fork_resolution: handled %v, %v", handled, err)
	}
	if got := records[identityKey("revision_fork_resolution", "resolution-1", 0)]; got.Subject != "topic_revision:sync@2" || got.Continuation != "topic_revision:sync@3" {
		t.Fatalf("decoded %#v", got)
	}
	// Builds that know only checkpoint forks read fork_resolution; a revision
	// resolution must never travel there, where they would refuse it.
	if _, err := decodeForkResolutionEnvelope(portableForkResolutions{}, encode("fork_resolution")); err == nil {
		t.Fatal("a revision resolution was accepted as fork_resolution")
	}
}

func TestSupersessionForkResolutionsTravelAsTheirOwnType(t *testing.T) {
	payload := forkResolutionPayload{Family: fork.FamilySupersession, SubjectID: "decision:base", AcceptedHead: "from-a", RejectedHeads: []string{"from-b"}, Resolver: "owner", OperationalSchemaVersion: fork.SchemaVersion}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	index, err := json.Marshal(forkResolutionIndex(payload))
	if err != nil {
		t.Fatal(err)
	}
	encode := func(recordType string) Envelope {
		encoded, err := EncodeEnvelope(EnvelopeSpec{LineageID: "lineage:5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a", RecordType: recordType, RecordSchemaVersion: 1, RecordID: "resolution-1", RecordedAt: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC), Authority: "user_reported", Index: index, Refs: forkResolutionReferences(payload), Payload: body})
		if err != nil {
			t.Fatal(err)
		}
		return encoded.Envelope
	}
	records := portableForkResolutions{}
	envelope := encode("supersession_fork_resolution")
	if handled, err := decodeForkResolutionEnvelope(records, envelope); !handled || err != nil {
		t.Fatalf("supersession_fork_resolution: handled %v, %v", handled, err)
	}
	if len(envelope.Refs) != 3 || envelope.Refs[0] != (Reference{Kind: "decision", ID: "base"}) {
		t.Fatalf("refs %#v", envelope.Refs)
	}
	if got := records[identityKey("supersession_fork_resolution", "resolution-1", 0)]; got.Subject != "decision:base" || got.AcceptedHead != "from-a" {
		t.Fatalf("decoded %#v", got)
	}
	// Older builds read fork_resolution and revision_fork_resolution; a
	// supersession resolution travels in neither.
	for _, other := range []string{"fork_resolution", "revision_fork_resolution"} {
		if _, err := decodeForkResolutionEnvelope(portableForkResolutions{}, encode(other)); err == nil {
			t.Fatalf("a supersession resolution was accepted as %s", other)
		}
	}
}
