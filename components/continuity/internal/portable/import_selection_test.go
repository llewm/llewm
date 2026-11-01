package portable

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/decision"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

// Compare each selected operational identity against the independent complete
// reader, including retained wire bytes and actor ancestry. The mixed delta
// fixture invokes this after each arrival, across multiple record families.
func verifyImportSelections(t *testing.T, ctx context.Context, db *sql.DB, project string, objects []EncodedEnvelope) {
	t.Helper()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	identity, err := identityFrom(ctx, tx, project)
	if err != nil {
		t.Fatal(err)
	}
	root, err := projectRootFrom(ctx, tx, project)
	if err != nil {
		t.Fatal(err)
	}
	full, err := LocalEnvelopesForImport(ctx, tx, project, objects)
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range objects {
		measured, metrics := syncwork.WithWorkMetrics(ctx)
		validated := make(snapshotEncodings)
		specs, qualified, err := loadImportSelection(measured, tx, project, root, identity.LineageID, []EncodedEnvelope{object}, validated)
		if err != nil || !qualified || len(specs) != 1 {
			t.Fatalf("selected %s: %d %t %v", recordKey(object.Envelope), len(specs), qualified, err)
		}
		got, err := validated.encode(measured, specs[0])
		want := full[recordKey(object.Envelope)]
		if err != nil || !bytes.Equal(got.Bytes, want.Bytes) {
			t.Fatalf("selected identity differs from full reader: %s %v", recordKey(object.Envelope), err)
		}
		counts := metrics.Counts()
		if counts["operational_full_loads"] != 0 || counts["operational_selected_loads"] != 1 || counts["retained_envelopes_checked"] != 1 {
			t.Fatalf("selection scanned unrelated retained history: %v", counts)
		}
	}
}

func TestImportSelectionPlansMatchCompleteSnapshot(t *testing.T) {
	ctx, db, project := operationalProject(t, "import-selection")
	identity, err := (Service{DB: db}).Initialize(ctx, project, "lineage:abababababababababababababababab")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		if _, err := (decision.Service{DB: db}).Append(ctx, decision.Record{ProjectID: project, ID: fmt.Sprintf("existing-%d", i), Title: "Decision", Rationale: "Observation", Disposition: "accepted", Actor: "owner", ActorType: "human", Authority: "caller_asserted"}); err != nil {
			t.Fatal(err)
		}
	}
	export, err := EncodeMemoryExport(ctx, db, project)
	if err != nil {
		t.Fatal(err)
	}
	verifyImportSelections(t, ctx, db, project, export.Envelopes[:2])
	original, err := DecodeMemoryEnvelope(export.Envelopes[0].Bytes)
	if err != nil {
		t.Fatal(err)
	}
	for _, variant := range []string{"present", "absent", "collision"} {
		t.Run(variant, func(t *testing.T) {
			spec := original
			if variant == "absent" {
				spec.RecordID = "new-identity"
			}
			if variant == "collision" {
				spec.Authority = "user_reported"
			}
			bundle, err := BuildBundle(BundleSpec{ExportID: "export:selection-fixture", LineageID: identity.LineageID, SourceInstanceID: identity.InstanceID, CreatedAt: time.Now().UTC(), SourceDatabaseSchema: export.DatabaseSchema, SelectionProfile: "curated-v1", RedactionProfile: DefaultRedactionProfile(), Omissions: curatedV1Omissions}, []EnvelopeSpec{spec})
			if err != nil {
				t.Fatal(err)
			}
			tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			measured, work := syncwork.WithWorkMetrics(ctx)
			got, err := planValidatedOperationalImport(measured, tx, project, bundle, nil)
			if err != nil {
				t.Fatal(err)
			}
			var snapshot map[string]EncodedEnvelope
			want, err := planValidatedOperationalImport(ctx, tx, project, bundle, &snapshot)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("selected plan differs: %+v/%+v %v", got, want, err)
			}
			counts := work.Counts()
			if len(snapshot) != 100 || counts["operational_full_loads"] != 0 || counts["retained_envelopes_checked"] > 1 || counts["operational_specs_loaded"] > 1 {
				t.Fatalf("unexpected selection work: snapshot=%d %v", len(snapshot), counts)
			}
		})
	}
	// A small comparison does not certify omitted state. Damage to an omitted
	// retained object is still rejected by a caller requesting a full snapshot;
	// selecting that same damaged identity must also fail.
	if _, err := (decision.Service{DB: db}).Append(ctx, decision.Record{ProjectID: project, ID: "damaged", Title: "Decision", Rationale: "Observation", Disposition: "accepted", Actor: "owner", ActorType: "human", Authority: "caller_asserted"}); err != nil {
		t.Fatal(err)
	}
	damagedSpec := original
	damagedSpec.RecordID = "damaged"
	damaged, err := EncodeEnvelope(damagedSpec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO portable_envelopes(project_id,record_type,record_id,record_revision,envelope_json,origin) VALUES(?,?,?,?,'{}','export')`, project, damaged.Envelope.RecordType, damaged.Envelope.RecordID, damaged.Envelope.RecordRevision); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	root, err := projectRootFrom(ctx, tx, project)
	if err != nil {
		t.Fatal(err)
	}
	if _, used, err := loadImportSelection(ctx, tx, project, root, identity.LineageID, export.Envelopes[:1], make(snapshotEncodings)); !used || err != nil {
		t.Fatalf("unrelated damage affected selected comparison: %t %v", used, err)
	}
	if _, used, err := loadImportSelection(ctx, tx, project, root, identity.LineageID, []EncodedEnvelope{damaged}, make(snapshotEncodings)); !used || err == nil {
		t.Fatalf("selected damage was accepted: %t %v", used, err)
	}
	if _, err := LocalEnvelopesForImport(ctx, tx, project, nil); err == nil {
		t.Fatal("complete snapshot accepted omitted damage")
	}
}

func TestImportSelectionUnknownAndLargeSetsUseCompleteReader(t *testing.T) {
	ctx, db, project := operationalProject(t, "import-selection-fallback")
	for _, objects := range [][]EncodedEnvelope{{{Envelope: Envelope{RecordType: "usage_session_summary"}}}, {{Envelope: Envelope{RecordType: "redaction_recovery"}}}, make([]EncodedEnvelope, 129)} {
		if _, used, err := loadImportSelection(ctx, db, project, "", "", objects, nil); used || err != nil {
			t.Fatalf("unqualified selection accepted: used=%t %v", used, err)
		}
	}
	// Even a damaged existing correction graph must reach the complete reader,
	// rather than letting a scoped comparison omit its substitution effects.
	if _, err := db.ExecContext(ctx, `INSERT INTO redaction_recoveries(project_id,id,record_type,record_id,record_revision,original_sha256,derived_sha256,original_content_sha256,derived_content_sha256,envelope_json) VALUES(?,'recovery','decision','x',0,'original','derived','content-original','content-derived','{}')`, project); err != nil {
		t.Fatal(err)
	}
	objects := []EncodedEnvelope{{Envelope: Envelope{RecordType: "decision", RecordID: "x"}}}
	if _, used, err := loadImportSelection(ctx, db, project, "", "", objects, nil); used || err != nil {
		t.Fatalf("existing recovery graph omitted: used=%t %v", used, err)
	}
}
