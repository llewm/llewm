package portable

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
	"github.com/llewm/llewm/components/continuity/internal/procedures"
	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
)

func TestUnchangedExportDoesNotAcquireWriterLock(t *testing.T) {
	ctx, db, id := operationalProject(t, "read-only-export")
	if _, err := (Service{DB: db}).Initialize(ctx, id, "lineage:abababababababababababababababab"); err != nil {
		t.Fatal(err)
	}
	if _, err := (worknarrative.Service{DB: db}).Append(ctx, worknarrative.Record{ID: "retained", ProjectID: id, Title: "Retained", Trigger: "manual", Creator: "test", Claims: []worknarrative.Claim{{Kind: "outcome", Text: "Observation", Authority: "caller_asserted"}}}); err != nil {
		t.Fatal(err)
	}
	first, err := EncodeMemoryExport(ctx, db, id)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback()
	if _, err := writer.ExecContext(ctx, `UPDATE projects SET name=name WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	readCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	second, err := EncodeMemoryExport(readCtx, db, id)
	if err != nil {
		t.Fatalf("unchanged export blocked by writer: %v", err)
	}
	if len(first.Envelopes) != len(second.Envelopes) {
		t.Fatal("record count changed")
	}
	for i := range first.Envelopes {
		if !bytes.Equal(first.Envelopes[i].Bytes, second.Envelopes[i].Bytes) {
			t.Fatal("export bytes changed")
		}
	}
}

// Source prose deliberately mentions the destination's root and ID. Those are
// ordinary foreign text on the source, but used to be substituted on re-export.
func TestImportedEnvelopesSurviveDestinationRedactionContext(t *testing.T) {
	ctx, a, aid := operationalProject(t, "retained-source")
	_, b, bid := operationalProject(t, "retained-destination")
	_, c, cid := operationalProject(t, "retained-third")
	lineage := "lineage:abababababababababababababababab"
	for _, p := range []struct {
		dbID   string
		target int
	}{{aid, 0}, {bid, 1}, {cid, 2}} {
		db := a
		if p.target == 1 {
			db = b
		}
		if p.target == 2 {
			db = c
		}
		if _, err := (Service{DB: db}).Initialize(ctx, p.dbID, lineage); err != nil {
			t.Fatal(err)
		}
	}
	foreign := filepath.Dir(sourceDBPath(t, b)) + " " + bid
	if _, err := (checkpoint.Service{DB: a}).CreateWithActions(ctx, checkpoint.Checkpoint{ProjectID: aid, TaskID: "retained", Task: "Retain envelope", Summary: foreign, NextAction: "Check " + foreign, Status: "in_progress"}); err != nil {
		t.Fatal(err)
	}
	if _, err := (worknarrative.Service{DB: a}).Append(ctx, worknarrative.Record{ID: "retained-narrative", ProjectID: aid, Title: "Retain envelope", Trigger: "manual", Creator: "codex", Claims: []worknarrative.Claim{{Kind: "outcome", Text: foreign, Authority: "caller_asserted"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := (procedures.Service{DB: a}).Append(ctx, procedures.Procedure{ProjectID: aid, ID: "retained-procedure", Title: "Exact install", Purpose: "Install", Kind: "runbook", Status: "accepted", Platforms: []string{"linux"}, DeclaredEffects: []string{"install"}, Steps: []procedures.Step{{ID: "install", Description: foreign, ArgvTemplate: []string{"copy", foreign}}}, Source: "test"}); err != nil {
		t.Fatal(err)
	}
	original, err := EncodeMemoryExport(ctx, a, aid)
	if err != nil {
		t.Fatal(err)
	}
	var specs []EnvelopeSpec
	for _, e := range original.Envelopes {
		s, err := DecodeMemoryEnvelope(e.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		specs = append(specs, s)
	}
	plan, receipt, err := ImportMemoryRecords(ctx, b, bid, specs, time.Now())
	if err != nil || !plan.ApplyAllowed || receipt == nil {
		t.Fatalf("import: %#v %v", plan, err)
	}
	imported, err := EncodeMemoryExport(ctx, b, bid)
	if err != nil {
		t.Fatal(err)
	}
	if len(original.Envelopes) != len(imported.Envelopes) {
		t.Fatal("lost records")
	}
	for i, e := range original.Envelopes {
		if !bytes.Equal(e.Bytes, imported.Envelopes[i].Bytes) {
			t.Fatalf("changed %s", e.Envelope.RecordType)
		}
	}
	replay, _, err := ImportMemoryRecords(ctx, b, bid, specs, time.Now())
	if err != nil || !replay.ApplyAllowed || replay.Counts[PlanCollision] != 0 {
		t.Fatalf("replay: %#v %v", replay, err)
	}
	if _, receipt, err := ImportMemoryRecords(ctx, c, cid, specs, time.Now()); err != nil || receipt == nil {
		t.Fatalf("third host: %v", err)
	}
	got, err := (procedures.Service{DB: b}).Get(ctx, bid, "retained-procedure", 1)
	if err != nil || got.Steps[0].ArgvTemplate[1] != foreign {
		t.Fatalf("procedure semantics changed: %#v %v", got, err)
	}
	if _, err := b.Exec(`UPDATE portable_envelopes SET origin='export'`); err == nil {
		t.Fatal("retained envelopes allowed mutation")
	}
	if _, err := b.Exec(`DELETE FROM portable_envelopes`); err == nil {
		t.Fatal("retained envelopes allowed deletion")
	}
}

func TestMemoryExportRemainsStableAfterRootChange(t *testing.T) {
	ctx, db, id := operationalProject(t, "before-move")
	if _, err := (Service{DB: db}).Initialize(ctx, id, "lineage:cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd"); err != nil {
		t.Fatal(err)
	}
	future := filepath.Join(t.TempDir(), "after-move")
	if _, err := (worknarrative.Service{DB: db}).Append(ctx, worknarrative.Record{ID: "move", ProjectID: id, Title: "Move", Trigger: "manual", Creator: "codex", Claims: []worknarrative.Claim{{Kind: "outcome", Text: "Future root " + future + " token=not-for-export", Authority: "caller_asserted"}}}); err != nil {
		t.Fatal(err)
	}
	first, err := EncodeMemoryExport(ctx, db, id)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(first.Envelopes[0].Bytes), "not-for-export") {
		t.Fatal("secret persisted in transport")
	}
	if _, err := db.Exec(`UPDATE projects SET root=? WHERE id=?`, future, id); err != nil {
		t.Fatal(err)
	}
	second, err := EncodeMemoryExport(ctx, db, id)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Envelopes[0].Bytes, second.Envelopes[0].Bytes) {
		t.Fatal("project move changed immutable export")
	}
}

func TestRetainedNarrativeExportAndNewSupersession(t *testing.T) {
	ctx, db, id := operationalProject(t, "narrative-reuse")
	if _, err := (Service{DB: db}).Initialize(ctx, id, "lineage:abababababababababababababababab"); err != nil {
		t.Fatal(err)
	}
	appendNarrative := func(recordID, supersedes string) {
		t.Helper()
		_, err := (worknarrative.Service{DB: db}).Append(ctx, worknarrative.Record{
			ID: recordID, ProjectID: id, Title: recordID, Trigger: "manual", Creator: "test", SupersedesID: supersedes,
			Claims: []worknarrative.Claim{{Kind: "outcome", Text: "An observation", Authority: "caller_asserted"}},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	appendNarrative("first", "")
	first, err := EncodeMemoryExport(ctx, db, id)
	if err != nil {
		t.Fatal(err)
	}
	appendNarrative("second", "first")
	second, err := EncodeMemoryExport(ctx, db, id)
	if err != nil {
		t.Fatal(err)
	}
	third, err := EncodeMemoryExport(ctx, db, id)
	if err != nil {
		t.Fatal(err)
	}
	index := func(export MemoryExport) map[string][]byte {
		result := make(map[string][]byte)
		for _, e := range export.Envelopes {
			result[RecordKey(e.Envelope.RecordType, e.Envelope.RecordID, e.Envelope.RecordRevision)] = e.Bytes
		}
		return result
	}
	a, b, c := index(first), index(second), index(third)
	if !bytes.Equal(a[RecordKey("work_narrative", "first", 0)], b[RecordKey("work_narrative", "first", 0)]) {
		t.Fatal("retained narrative changed")
	}
	for _, kind := range []string{"work_narrative", "work_narrative_supersession"} {
		key := RecordKey(kind, "second", 0)
		if len(b[key]) == 0 || !bytes.Equal(b[key], c[key]) {
			t.Fatalf("new %s did not survive retained export", kind)
		}
	}
}

// Retaining an envelope must not change the operational records used by
// import planning. This compares the reconstruction before retention with the
// path taken on subsequent exports, including a supersession edge.
func TestRetainedNarrativeMatchesReconstructedRecords(t *testing.T) {
	ctx, db, id := operationalProject(t, "narrative-equivalence")
	if _, err := (Service{DB: db}).Initialize(ctx, id, "lineage:abababababababababababababababab"); err != nil {
		t.Fatal(err)
	}
	service := worknarrative.Service{DB: db}
	for _, record := range []worknarrative.Record{
		{ID: "first", ProjectID: id, Title: "First", Trigger: "manual", Creator: "test", Claims: []worknarrative.Claim{{Kind: "outcome", Text: "First observation", Authority: "caller_asserted"}}},
		{ID: "second", ProjectID: id, Title: "Correction", Trigger: "manual", Creator: "test", SupersedesID: "first", Claims: []worknarrative.Claim{{Kind: "outcome", Text: "Corrected observation", Authority: "caller_asserted"}}},
	} {
		if _, err := service.Append(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	before, err := LoadOperationalRecords(ctx, db, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := EncodeMemoryExport(ctx, db, id); err != nil {
		t.Fatal(err)
	}
	after, err := LoadOperationalRecords(ctx, db, id)
	if err != nil {
		t.Fatal(err)
	}
	encode := func(records []EnvelopeSpec) map[string][]byte {
		t.Helper()
		result := make(map[string][]byte, len(records))
		for _, spec := range records {
			e, err := EncodeEnvelope(spec)
			if err != nil {
				t.Fatal(err)
			}
			result[RecordKey(spec.RecordType, spec.RecordID, spec.RecordRevision)] = e.Bytes
		}
		return result
	}
	a, b := encode(before.Records), encode(after.Records)
	if len(a) != len(b) {
		t.Fatalf("record count changed after retention: %d to %d", len(a), len(b))
	}
	for key, want := range a {
		if !bytes.Equal(want, b[key]) {
			t.Fatalf("record %q changed after retention", key)
		}
	}
}

// A snapshot reuse hit must still prove canonical bytes, both hashes, supported
// fields and the SQLite identity. Retention alone is never a validation proof.
func TestSnapshotEncodingReuseRejectsInvalidRetention(t *testing.T) {
	for _, mutation := range []string{"envelope hash", "content hash", "payload", "noncanonical", "duplicate key", "unknown field", "lineage", "identity"} {
		t.Run(mutation, func(t *testing.T) {
			ctx, db, id := operationalProject(t, "invalid-retained")
			lineage := "lineage:abababababababababababababababab"
			if _, err := (Service{DB: db}).Initialize(ctx, id, lineage); err != nil {
				t.Fatal(err)
			}
			if _, err := (worknarrative.Service{DB: db}).Append(ctx, worknarrative.Record{ID: "record", ProjectID: id, Title: "Record", Trigger: "manual", Creator: "test", Claims: []worknarrative.Claim{{Kind: "outcome", Text: "Observation", Authority: "caller_asserted"}}}); err != nil {
				t.Fatal(err)
			}
			loaded, err := LoadOperationalRecords(ctx, db, id)
			if err != nil || len(loaded.Records) != 1 {
				t.Fatalf("load: %v, %d records", err, len(loaded.Records))
			}
			spec := loaded.Records[0]
			encoded, err := EncodeEnvelope(spec)
			if err != nil {
				t.Fatal(err)
			}
			body := string(encoded.Bytes)
			columnID := spec.RecordID
			switch mutation {
			case "envelope hash":
				body = strings.Replace(body, encoded.Envelope.EnvelopeSHA256, strings.Repeat("0", 64), 1)
			case "content hash":
				body = strings.Replace(body, encoded.Envelope.ContentSHA256, strings.Repeat("0", 64), 1)
			case "payload":
				body = strings.Replace(body, "Observation", "Altered", 1)
			case "noncanonical":
				body = " " + body
			case "duplicate key":
				body = strings.Replace(body, "{", `{"authority":"mixed",`, 1)
			case "unknown field":
				body = strings.Replace(body, "{", `{"unexpected":true,`, 1)
			case "lineage":
				spec.LineageID = "lineage:cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd"
				other, err := EncodeEnvelope(spec)
				if err != nil {
					t.Fatal(err)
				}
				body = string(other.Bytes)
			case "identity":
				columnID = "different"
			}
			if _, err := db.ExecContext(ctx, `INSERT INTO portable_envelopes(project_id,record_type,record_id,record_revision,envelope_json,origin) VALUES(?,?,?,?,?,'export')`, id, spec.RecordType, columnID, spec.RecordRevision, body); err != nil {
				t.Fatal(err)
			}
			if _, err := EncodeMemoryExport(ctx, db, id); err == nil {
				t.Fatal("export accepted invalid retained envelope")
			}
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err := LocalEnvelopesForImport(ctx, tx, id); err == nil {
				t.Fatal("import comparison accepted invalid retained envelope")
			}
		})
	}
}
