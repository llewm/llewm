package fork

import "testing"

func TestEquivalentRoadmapNodeEnvelopesIgnoresOnlyDefaultV2Fields(t *testing.T) {
	v1 := []byte(`{"record_type":"roadmap_node","record_schema_version":1,"payload":{"title":"Node","status":"active","priority":10,"source_path":"docs/roadmap.md","operational_schema_version":1}}`)
	v2 := []byte(`{"record_type":"roadmap_node","record_schema_version":2,"payload":{"title":"Node","status":"active","priority":10,"kind":"work_item","evidence_refs":[],"source_path":"docs/roadmap.md","operational_schema_version":1}}`)
	if !EquivalentRoadmapNodeEnvelopes([][]byte{v1, v2}) {
		t.Fatal("default v2 fields should not create a semantic fork")
	}
	milestone := []byte(`{"record_type":"roadmap_node","record_schema_version":2,"payload":{"title":"Node","status":"active","priority":10,"kind":"milestone","evidence_refs":[],"source_path":"docs/roadmap.md","operational_schema_version":1}}`)
	if EquivalentRoadmapNodeEnvelopes([][]byte{v1, milestone}) {
		t.Fatal("milestone kind must remain a semantic difference")
	}
}
