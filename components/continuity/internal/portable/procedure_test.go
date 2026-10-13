package portable

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/procedures"
)

func TestProceduresRoundTrip(t *testing.T) {
	ctx, db, projectID := operationalProject(t, "procedures")
	lineage := "lineage:2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f"
	identity, err := (Service{DB: db}).Initialize(ctx, projectID, lineage)
	if err != nil {
		t.Fatal(err)
	}
	recorded := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	service := procedures.Service{DB: db, Now: func() time.Time { recorded = recorded.Add(time.Minute); return recorded }}
	runbook := procedures.Procedure{ProjectID: projectID, ID: "box-install", Kind: "runbook", Title: "Install on the box", Purpose: "Copy a build to " + projectID,
		Status: "proposed", Platforms: []string{"linux/amd64"}, Parameters: []procedures.Parameter{{Name: "host", Description: "SSH destination", Required: true}},
		DeclaredEffects: []string{"replaces the remote binary"}, Validations: []string{"remote version matches"},
		Steps:  []procedures.Step{{ID: "copy", Description: "Copy it", ArgvTemplate: []string{"scp", "{{out}}", "{{host}}:bin"}}},
		Source: "docs/resources-and-runbooks.md"}
	if _, err := service.Append(ctx, runbook); err != nil {
		t.Fatal(err)
	}
	runbook.Status = "accepted"
	if _, err := service.Append(ctx, runbook); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Append(ctx, procedures.Procedure{ProjectID: projectID, ID: "go-checks", Title: "Go checks", Purpose: "Verify", Status: "accepted", Platforms: []string{"linux"},
		Steps: []procedures.Step{{ID: "test", Description: "Run tests", StageKind: "source_check", StageID: "go-test"}}, Source: "docs/procedure-monitoring.md"}); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadOperationalRecords(ctx, db, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if counts := countSpecs(loaded.Records); counts["procedure"] != 3 {
		t.Fatalf("procedure count %d; all=%#v", counts["procedure"], counts)
	}
	bundle, err := BuildBundle(BundleSpec{ExportID: "export:procedures", LineageID: lineage, SourceInstanceID: identity.InstanceID, CreatedAt: recorded.Add(time.Hour), SourceDatabaseSchema: loaded.DatabaseSchema, SelectionProfile: "curated-v1", RedactionProfile: DefaultRedactionProfile(), Omissions: CuratedV1Omissions()}, loaded.Records)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bundle.RecordsBytes), projectID) {
		t.Fatal("procedure export leaked the source project ID")
	}

	targetCtx, targetDB, targetProject := operationalProject(t, "procedures-target")
	targetIdentity, err := (Service{DB: targetDB}).Initialize(targetCtx, targetProject, lineage)
	if err != nil {
		t.Fatal(err)
	}
	applied, err := ApplyOperationalImport(targetCtx, targetDB, targetProject, bundle.ManifestBytes, bundle.RecordsBytes, recorded.Add(2*time.Hour))
	if err != nil || applied.Receipt == nil || applied.Receipt.AppendedRecords != len(bundle.Records) {
		t.Fatalf("apply: %#v %v", applied, err)
	}
	imported, err := (procedures.Service{DB: targetDB}).Get(targetCtx, targetProject, "box-install", 0)
	if err != nil || imported == nil || imported.Kind != "runbook" || imported.Revision != 2 || imported.Steps[0].ArgvTemplate[0] != "scp" {
		t.Fatalf("imported runbook: %#v %v", imported, err)
	}
	reloaded, err := LoadOperationalRecords(targetCtx, targetDB, targetProject)
	if err != nil {
		t.Fatal(err)
	}
	reexport, err := BuildBundle(BundleSpec{ExportID: "export:procedures-clone", LineageID: lineage, SourceInstanceID: targetIdentity.InstanceID, CreatedAt: recorded.Add(3 * time.Hour), SourceDatabaseSchema: reloaded.DatabaseSchema, SelectionProfile: "curated-v1", RedactionProfile: DefaultRedactionProfile(), Omissions: CuratedV1Omissions()}, reloaded.Records)
	if err != nil {
		t.Fatal(err)
	}
	if string(bundle.RecordsBytes) != string(reexport.RecordsBytes) {
		t.Fatal("procedure clone re-export changed semantic records")
	}
	replay, err := ApplyOperationalImport(targetCtx, targetDB, targetProject, bundle.ManifestBytes, bundle.RecordsBytes, recorded.Add(4*time.Hour))
	if err != nil || replay.Receipt == nil || replay.Receipt.AlreadyPresentRecords != len(bundle.Records) || replay.Receipt.AppendedRecords != 0 {
		t.Fatalf("replay: %#v %v", replay, err)
	}
}

func TestProcedureImportRefusesNonCanonicalPayloads(t *testing.T) {
	good := procedurePayload{Title: "T", Purpose: "P", Kind: "runbook", Status: "proposed", Platforms: []string{"linux"}, WorkingDirectory: ".", DeclaredEffects: []string{"e"},
		Steps: []procedures.Step{{ID: "s", Description: "d"}}, Source: "src", OperationalSchemaVersion: 1}
	for name, mutate := range map[string]func(*procedurePayload){
		"old kind name": func(p *procedurePayload) {
			p.Kind = "completion_check"
			p.Steps[0].StageKind, p.Steps[0].StageID = "source_check", "x"
		},
		"runbook without effects": func(p *procedurePayload) { p.DeclaredEffects = nil },
		"unredacted secret":       func(p *procedurePayload) { p.Purpose = "token=abc123secret" },
		"padded title":            func(p *procedurePayload) { p.Title = " T" },
	} {
		payload := good
		payload.Steps = []procedures.Step{good.Steps[0]}
		mutate(&payload)
		body, _ := jsonMarshal(payload)
		envelope := Envelope{RecordType: "procedure", RecordID: "p", RecordRevision: 1, RecordedAt: "2026-09-29T12:00:00Z", Authority: "caller_asserted", Refs: []Reference{}, Index: mustIndex(t, payload.Title, payload.Status), Payload: body}
		if _, err := decodeProcedureEnvelope(portableProcedures{}, envelope); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func jsonMarshal(value any) ([]byte, error) { return json.Marshal(value) }

func mustIndex(t *testing.T, title, status string) json.RawMessage {
	t.Helper()
	body, err := json.Marshal(envelopeIndex{Title: title, Status: status})
	if err != nil {
		t.Fatal(err)
	}
	return body
}
