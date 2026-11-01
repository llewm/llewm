package portable

import (
	"encoding/json"
	"github.com/llewm/llewm/components/continuity/internal/fork"
	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
	"strings"
	"testing"
	"time"
)

func recoveryInvalidationFixture(t *testing.T) (EncodedEnvelope, EncodedEnvelope, EncodedEnvelope) {
	t.Helper()
	bundle, _, at := operationalRecoveryBundle(t)
	var spec EnvelopeSpec
	for _, object := range bundle.Records {
		if object.Envelope.RecordType == "work_narrative" {
			var err error
			spec, err = DecodeMemoryEnvelope(object.Bytes)
			if err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	var payload workNarrativePayload
	if err := json.Unmarshal(spec.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	payload.Claims[0].Text = "Read /home/peer/LLMem project-0123456789abcdef"
	payload.Claims[0].Citations = []worknarrative.Citation{{Source: "narrative:project-0123456789abcdef", Relation: "supports"}}
	spec.Payload, _ = json.Marshal(payload)
	spec.Refs = workNarrativeReferences(payload.Anchors, payload.CreatorInvocationID, payload.Claims)
	original, err := EncodeEnvelope(spec)
	if err != nil {
		t.Fatal(err)
	}
	replace := func(s string) string {
		return strings.ReplaceAll(strings.ReplaceAll(s, "/home/peer/LLMem", "[PROJECT_ROOT]"), "project-0123456789abcdef", "[PROJECT_ID]")
	}
	spec.Payload = []byte(replace(string(spec.Payload)))
	for i := range spec.Refs {
		spec.Refs[i].ID = replace(spec.Refs[i].ID)
	}
	derived, err := EncodeEnvelope(spec)
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := RedactionRecoverySpec(RedactionRecovery{Original: original.Bytes, Derived: derived.Bytes, ProjectRoot: "/home/peer/LLMem", ProjectID: "project-0123456789abcdef", Creator: "test", Reason: "Historical re-export"}, at)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeEnvelope(recovery)
	if err != nil {
		t.Fatal(err)
	}
	return original, derived, encoded
}

func TestRecoveryInvalidationGuardsIncludeBothPayloadInterpretations(t *testing.T) {
	original, _, recovery := recoveryInvalidationFixture(t)
	guards, known := ImportInvalidationGuards(recovery)
	if !known {
		t.Fatal("valid correction has unknown guards")
	}
	wanted := map[ImportDependency]bool{
		{RecordType: "work_narrative", RecordID: original.Envelope.RecordID, Mode: DependencyExact}: true,
		{RecordType: "work_narrative", RecordID: "project-0123456789abcdef", Mode: DependencyExact}: true,
		{RecordType: "work_narrative", RecordID: "[PROJECT_ID]", Mode: DependencyExact}:             true,
	}
	for _, guard := range guards {
		delete(wanted, guard)
	}
	if len(wanted) != 0 {
		t.Fatalf("correction omitted dependency interpretations: %v", wanted)
	}
	if _, supported := ImportDependencies(recovery.Envelope); supported {
		t.Fatal("invalidation guard authorized partial recovery application")
	}
	recovery.Bytes = []byte(`{}`)
	if _, known := ImportInvalidationGuards(recovery); known {
		t.Fatal("changed correction bytes accepted")
	}
	if guards, known := ImportInvalidationGuards(EncodedEnvelope{Envelope: Envelope{RecordType: "contract"}}); !known || len(guards) != 0 {
		t.Fatal("retired observations should have no semantic effect")
	}
	if _, known := ImportInvalidationGuards(EncodedEnvelope{Envelope: Envelope{RecordType: "future"}}); known {
		t.Fatal("unknown input has invalidation rules")
	}
}

func TestResolutionInvalidationGuardsNameCanonicalSubjects(t *testing.T) {
	for _, test := range []struct {
		family, subject, kind, id string
		revision                  int
	}{
		{fork.FamilyRevision, "topic_revision:subject@2", "topic_revision", "subject", 2},
		{fork.FamilySupersession, "narrative:subject", "work_narrative", "subject", 0},
		{fork.FamilyCheckpoint, "task", "checkpoint", "accepted", 0},
	} {
		t.Run(test.family, func(t *testing.T) {
			accepted, rejected := "accepted", "rejected"
			if test.family == fork.FamilyRevision {
				accepted, rejected = strings.Repeat("a", 64), strings.Repeat("b", 64)
			}
			payload := forkResolutionPayload{Family: test.family, SubjectID: test.subject, AcceptedHead: accepted, RejectedHeads: []string{rejected}, Resolver: "owner", OperationalSchemaVersion: fork.SchemaVersion}
			body, _ := json.Marshal(payload)
			index, _ := json.Marshal(forkResolutionIndex(payload))
			object, err := EncodeEnvelope(EnvelopeSpec{LineageID: "lineage:66666666666666666666666666666666", RecordType: forkResolutionRecordType(test.family), RecordSchemaVersion: 1, RecordID: "guard-" + test.family, RecordedAt: time.Unix(1, 0), Authority: "user_reported", Index: index, Refs: forkResolutionReferences(payload), Payload: body})
			if err != nil {
				t.Fatal(err)
			}
			guards, known := ImportInvalidationGuards(object)
			if !known {
				t.Fatal("resolution guards unknown")
			}
			wanted := ImportDependency{RecordType: test.kind, RecordID: test.id, Revision: test.revision, Mode: DependencyExact}
			for _, guard := range guards {
				if guard == wanted {
					return
				}
			}
			t.Fatalf("missing resolution subject: %v", guards)
		})
	}
}
