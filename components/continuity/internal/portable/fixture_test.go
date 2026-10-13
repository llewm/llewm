package portable

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func fixtureBundle(t *testing.T, lineage, instance, body string, recorded time.Time) Bundle {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"body": body, "title": "Shared ID"})
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := BuildBundle(BundleSpec{
		ExportID: "export:" + lineage[len("lineage:"):len("lineage:")+8], LineageID: lineage,
		SourceInstanceID: instance, CreatedAt: recorded, SourceDatabaseSchema: "014_portable_identity_effort.sql",
		SelectionProfile: "curated-v1", RedactionProfile: RedactionProfile{ID: "default", Version: 1, SHA256: strings.Repeat("c", 64)}, Omissions: []Omission{},
	}, []EnvelopeSpec{{LineageID: lineage, RecordType: "work_narrative", RecordSchemaVersion: 1, RecordID: "same-local-id", RecordedAt: recorded, Authority: "caller_asserted", Index: json.RawMessage(`{"title":"Shared ID","task_id":"portable"}`), Payload: payload}})
	if err != nil {
		t.Fatal(err)
	}
	return bundle
}
