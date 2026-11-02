package recordsync

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/decision"
	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

func TestFreshCaptureExportObservationsMatchTransportDecode(t *testing.T) {
	a := Archive{Lineage: lineage}
	for _, text := range []string{"ordinary", "😀<>&\x00", "invalid\xff"} {
		for _, field := range []string{"record", "authority", "ref_kind", "ref_id"} {
			t.Run(fmt.Sprintf("%s-%q", field, text), func(t *testing.T) {
				spec := portable.EnvelopeSpec{LineageID: lineage, RecordType: "decision", RecordSchemaVersion: 1, RecordID: "record", Authority: "observed", RecordedAt: time.Unix(1234567890, 0), Payload: []byte(`{}`), Refs: []portable.Reference{{Kind: "task", ID: "task"}}}
				switch field {
				case "record":
					spec.RecordID = text
				case "authority":
					spec.Authority = text
				case "ref_kind":
					spec.Refs[0].Kind = text
				case "ref_id":
					spec.Refs[0].ID = text
				}
				object, err := portable.EncodeEnvelope(spec)
				if err != nil {
					t.Fatal(err)
				}
				owned := a.freshCaptureExportObservations(portable.MemoryExport{Envelopes: []portable.EncodedEnvelope{object}})
				want, wantErr := portable.DecodeTransportEnvelope(object.Bytes)
				ctx, work := syncwork.WithWorkMetrics(context.Background())
				got, gotErr := captureObservation(ctx, object, owned)
				if (wantErr == nil) != (gotErr == nil) || !reflect.DeepEqual(got, want) {
					t.Fatalf("different transport result: %v vs %v", gotErr, wantErr)
				}
				wantReuse := int64(1)
				if strings.Contains(text, "\xff") {
					wantReuse = 0 // JSON encoding replaces invalid metadata UTF-8.
				}
				if work.Counts()["capture_decode_reuses"] != wantReuse {
					t.Fatalf("unexpected reuse: %v", work.Counts())
				}
			})
		}
	}
	// Export supports larger metadata than the transport profile; the shortcut
	// must not turn an export-valid oversized object into an accepted capture.
	large, err := portable.EncodeEnvelope(portable.EnvelopeSpec{LineageID: lineage, RecordType: "decision", RecordSchemaVersion: 1, RecordID: "large", Authority: strings.Repeat("x", MaxObjectBytes), RecordedAt: time.Unix(1234567890, 0), Payload: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	owned := a.freshCaptureExportObservations(portable.MemoryExport{Envelopes: []portable.EncodedEnvelope{large}})
	if len(owned) != 0 {
		t.Fatal("oversized transport object certified")
	}
	if _, err := captureObservation(context.Background(), large, owned); err == nil {
		t.Fatal("oversized transport object accepted")
	}
}

func TestFreshCaptureExportObservationsBoundAndExactBytes(t *testing.T) {
	a := Archive{Lineage: lineage}
	objects := make([]portable.EncodedEnvelope, 8193)
	for i := range objects {
		objects[i] = archiveObject(t, fmt.Sprintf("bound-%d", i), `{}`)
	}
	owned := a.freshCaptureExportObservations(portable.MemoryExport{Envelopes: objects})
	if len(owned) != 8192 {
		t.Fatalf("unexpected metadata bound: %d", len(owned))
	}
	object := objects[0]
	object.Bytes = append(bytes.Clone(object.Bytes), ' ')
	if _, err := captureObservation(context.Background(), object, owned); err == nil {
		t.Fatal("altered wire bytes reused a prior parse")
	}
	object = objects[0]
	object.Envelope.RecordID = "forged parsed metadata"
	got, err := captureObservation(context.Background(), object, owned)
	if err != nil || got.Envelope.RecordID != objects[0].Envelope.RecordID {
		t.Fatal("caller metadata replaced the private observation")
	}
}

func TestCaptureProjectReusesFreshExportButRetainsArchiveAudit(t *testing.T) {
	ctx, db, project := indexedTestProject(t, "fresh-capture")
	a, err := openTestArchive(t, db, project)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (decision.Service{DB: db}).Append(ctx, decision.Record{ProjectID: project, ID: "decision", Title: "Decision", Rationale: "Observation", Disposition: "accepted", Actor: "test", ActorType: "human", Authority: "caller_asserted"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.CaptureProject(ctx, db, project); err != nil {
		t.Fatal(err)
	}
	// A missing cursor forces the complete export and complete archive audit.
	if _, err := db.Exec(`DELETE FROM history_sync_cursors WHERE project_id=?`, project); err != nil {
		t.Fatal(err)
	}
	ctx, work := syncwork.WithWorkMetrics(ctx)
	if _, err := a.CaptureProject(ctx, db, project); err != nil {
		t.Fatal(err)
	}
	counts := work.Counts()
	if counts["capture_full_exports"] != 1 || counts["archive_audit_envelopes"] != 1 || counts["archive_audit_verified_bytes_reused"] != 1 || counts["capture_decode_reuses"] == 0 {
		t.Fatalf("fresh export reuse skipped or failed to audit: %v", counts)
	}
}

func FuzzFreshCaptureExportMatchesTransport(f *testing.F) {
	for _, text := range []string{"ordinary", "😀<>&\x00", "invalid\xff"} {
		for _, year := range []int32{-1, 0, 2026, 9999, 10000} {
			f.Add(text, year, true)
		}
	}
	f.Fuzz(func(t *testing.T, text string, year int32, optional bool) {
		if len(text) > 2048 {
			t.Skip()
		}
		spec := portable.EnvelopeSpec{LineageID: lineage, RecordType: "decision", RecordSchemaVersion: 1, RecordID: "id:" + text, Authority: "authority:" + text, RecordedAt: time.Date(int(year), 7, 2, 3, 4, 5, 123, time.UTC), Payload: []byte(`{}`)}
		if optional {
			spec.Refs = []portable.Reference{{Kind: "kind:" + text, ID: "id:" + text}}
		}
		object, err := portable.EncodeEnvelope(spec)
		if err != nil {
			return
		}
		a := Archive{Lineage: lineage}
		owned := a.freshCaptureExportObservations(portable.MemoryExport{Envelopes: []portable.EncodedEnvelope{object}})
		got, gotErr := captureObservation(context.Background(), object, owned)
		want, wantErr := portable.DecodeTransportEnvelope(object.Bytes)
		if (gotErr == nil) != (wantErr == nil) || !reflect.DeepEqual(got, want) {
			t.Fatalf("fresh export differs from transport: %v vs %v", gotErr, wantErr)
		}
	})
}
