package portable

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRedactionRecoveryRefusesUnrelatedOrReversedChanges(t *testing.T) {
	bundle, _, at := operationalRecoveryBundle(t)
	var original EncodedEnvelope
	for _, e := range bundle.Records {
		if e.Envelope.RecordType == "work_narrative" {
			original = e
			break
		}
	}
	spec, err := DecodeMemoryEnvelope(original.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	var payload workNarrativePayload
	if err := json.Unmarshal(spec.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	payload.Claims[0].Text = "Read /home/peer/LLMem project-0123456789abcdef"
	spec.Payload, _ = json.Marshal(payload)
	original, err = EncodeEnvelope(spec)
	if err != nil {
		t.Fatal(err)
	}
	spec.Payload = []byte(strings.ReplaceAll(strings.ReplaceAll(string(spec.Payload), "/home/peer/LLMem", "[PROJECT_ROOT]"), "project-0123456789abcdef", "[PROJECT_ID]"))
	derived, err := EncodeEnvelope(spec)
	if err != nil {
		t.Fatal(err)
	}
	good := RedactionRecovery{Original: original.Bytes, Derived: derived.Bytes, ProjectRoot: "/home/peer/LLMem", ProjectID: "project-0123456789abcdef", Creator: "codex", Reason: "Historical re-export"}
	recovery, err := RedactionRecoverySpec(good, at)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeEnvelope(recovery)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeRedactionRecovery(encoded); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*RedactionRecovery){
		"reversed":   func(v *RedactionRecovery) { v.Original, v.Derived = v.Derived, v.Original },
		"wrong root": func(v *RedactionRecovery) { v.ProjectRoot = "/home/other" },
		"wrong ID":   func(v *RedactionRecovery) { v.ProjectID = "project-fedcba9876543210" },
		"no change":  func(v *RedactionRecovery) { v.Derived = v.Original },
		"root slash": func(v *RedactionRecovery) { v.ProjectRoot = "/" },
		"secret":     func(v *RedactionRecovery) { v.Reason = "token=not-for-persistence" },
		"unrelated content": func(v *RedactionRecovery) {
			s, _ := DecodeMemoryEnvelope(v.Derived)
			s.Payload = []byte(strings.ReplaceAll(string(s.Payload), "Read ", "Delete "))
			e, _ := EncodeEnvelope(s)
			v.Derived = e.Bytes
		},
		"different identity": func(v *RedactionRecovery) {
			s, _ := DecodeMemoryEnvelope(v.Derived)
			s.RecordID = "other"
			e, _ := EncodeEnvelope(s)
			v.Derived = e.Bytes
		},
		"different time": func(v *RedactionRecovery) {
			s, _ := DecodeMemoryEnvelope(v.Derived)
			s.RecordedAt = s.RecordedAt.Add(time.Second)
			e, _ := EncodeEnvelope(s)
			v.Derived = e.Bytes
		},
	} {
		t.Run(name, func(t *testing.T) {
			bad := good
			mutate(&bad)
			if _, err := RedactionRecoverySpec(bad, at); err == nil {
				t.Fatal("accepted invalid recovery")
			}
		})
	}
	encoded.Envelope.Authority = "machine_observed"
	encoded.Bytes, _ = json.Marshal(encoded.Envelope)
	if _, err := decodeRedactionRecovery(encoded); err == nil {
		t.Fatal("accepted manufactured recovery authority")
	}
}

func TestRedactionRecoveryChainRollsBack(t *testing.T) {
	bundle, lineage, at := operationalRecoveryBundle(t)
	var spec EnvelopeSpec
	for _, e := range bundle.Records {
		if e.Envelope.RecordType == "work_narrative" {
			spec, _ = DecodeMemoryEnvelope(e.Bytes)
			break
		}
	}
	var payload workNarrativePayload
	if err := json.Unmarshal(spec.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	payload.Claims[0].Text = "Roots /home/first/LLMem and /home/second/LLMem"
	spec.Payload, _ = json.Marshal(payload)
	original, err := EncodeEnvelope(spec)
	if err != nil {
		t.Fatal(err)
	}
	spec.Payload = []byte(strings.ReplaceAll(string(spec.Payload), "/home/first/LLMem", "[PROJECT_ROOT]"))
	middle, err := EncodeEnvelope(spec)
	if err != nil {
		t.Fatal(err)
	}
	spec.Payload = []byte(strings.ReplaceAll(string(spec.Payload), "/home/second/LLMem", "[PROJECT_ROOT]"))
	derived, err := EncodeEnvelope(spec)
	if err != nil {
		t.Fatal(err)
	}
	first, err := RedactionRecoverySpec(RedactionRecovery{Original: original.Bytes, Derived: middle.Bytes, ProjectRoot: "/home/first/LLMem", Creator: "codex", Reason: "First re-export"}, at)
	if err != nil {
		t.Fatal(err)
	}
	second, err := RedactionRecoverySpec(RedactionRecovery{Original: middle.Bytes, Derived: derived.Bytes, ProjectRoot: "/home/second/LLMem", Creator: "codex", Reason: "Second re-export"}, at)
	if err != nil {
		t.Fatal(err)
	}
	ctx, db, id := operationalProject(t, "chain")
	if _, err := (Service{DB: db}).Initialize(ctx, id, lineage); err != nil {
		t.Fatal(err)
	}
	if _, receipt, err := ImportMemoryRecords(ctx, db, id, []EnvelopeSpec{first}, at); err != nil || receipt == nil {
		t.Fatalf("first: %v", err)
	}
	if _, _, err := ImportMemoryRecords(ctx, db, id, []EnvelopeSpec{second}, at); err == nil {
		t.Fatal("chain accepted")
	}
	if got := tableCount(t, db, "redaction_recoveries"); got != 1 {
		t.Fatalf("chain persisted: %d", got)
	}
	if got := tableCount(t, db, "portable_import_receipts"); got != 1 {
		t.Fatalf("chain receipt persisted: %d", got)
	}
	if _, err := RecoveredEnvelopes(ctx, db, id); err != nil {
		t.Fatalf("failed recovery stranded the store: %v", err)
	}
}
