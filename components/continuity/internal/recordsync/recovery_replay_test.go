package recordsync

import (
	"strings"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/portable"
)

func TestRecoveryReplayEligibilityRejectsUnknownSchemas(t *testing.T) {
	object := func(kind string, schema int, hash string) portable.EncodedEnvelope {
		return portable.EncodedEnvelope{Envelope: portable.Envelope{RecordType: kind, RecordSchemaVersion: schema, EnvelopeSHA256: hash}}
	}
	recovery := object("redaction_recovery", 1, strings.Repeat("1", 64))
	retired := object("contract", 1, strings.Repeat("2", 64))
	if key, ok := recoveryReplaySet([]portable.EncodedEnvelope{recovery, retired}); !ok || !strings.HasPrefix(key, "recovery:v4:") {
		t.Fatalf("retired observation prevented proof candidacy: %q %v", key, ok)
	}
	unknown := object("work_narrative", 999, strings.Repeat("3", 64))
	if _, ok := recoveryReplaySet([]portable.EncodedEnvelope{recovery, unknown}); ok {
		t.Fatal("unsupported schema accepted for replay")
	}
}

func TestRecoveryFingerprintKeepsReferencedInvocations(t *testing.T) {
	actor := func(id string, refs ...portable.Reference) portable.EncodedEnvelope {
		return portable.EncodedEnvelope{Envelope: portable.Envelope{RecordType: "actor_invocation", RecordID: id, Refs: refs}}
	}
	ref := func(id string) portable.Reference { return portable.Reference{Kind: "actor_invocation", ID: id} }
	local := map[string]portable.EncodedEnvelope{
		"unrelated":      actor("unrelated"),
		"local-ref":      actor("local-ref", ref("parent")),
		"parent":         actor("parent"),
		"incoming":       actor("incoming"),
		"incoming-ref":   actor("incoming-ref"),
		"normalized-ref": actor("normalized-ref"),
		"narrative":      {Envelope: portable.Envelope{RecordType: "work_narrative", Refs: []portable.Reference{ref("local-ref")}}},
	}
	input := []portable.EncodedEnvelope{
		actor("incoming"),
		{Envelope: portable.Envelope{RecordType: "checkpoint", EnvelopeSHA256: "alias", Refs: []portable.Reference{ref("incoming-ref")}}},
	}
	recovered := map[string]portable.EncodedEnvelope{"alias": {Envelope: portable.Envelope{RecordType: "checkpoint", Refs: []portable.Reference{ref("normalized-ref")}}}}
	excluded := recoveryInvocationExclusions(local, input, recovered)
	if len(excluded) != 1 || !excluded["unrelated"] {
		t.Fatalf("excluded the wrong invocation identities: %#v", excluded)
	}
	before := localStateHash(local, excluded)
	changed := local["unrelated"]
	changed.Bytes = []byte("unrelated change")
	local["unrelated"] = changed
	if localStateHash(local, recoveryInvocationExclusions(local, input, recovered)) != before {
		t.Fatal("unreferenced activity invalidated the proof")
	}
	changed = local["parent"]
	changed.Bytes = []byte("referenced parent change")
	local["parent"] = changed
	if localStateHash(local, recoveryInvocationExclusions(local, input, recovered)) == before {
		t.Fatal("referenced invocation change did not invalidate the proof")
	}
}
