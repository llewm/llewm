package portable

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/decision"
	"github.com/llewm/llewm/components/continuity/internal/topic"
)

func TestLedgerDecisionsAndTopicsRoundTrip(t *testing.T) {
	ctx, db, projectID := operationalProject(t, "ledger")
	lineage := "lineage:1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e"
	identity, err := (Service{DB: db}).Initialize(ctx, projectID, lineage)
	if err != nil {
		t.Fatal(err)
	}
	recorded := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { recorded = recorded.Add(time.Minute); return recorded }
	decisions := decision.Service{DB: db, Now: clock}
	first, err := decisions.Append(ctx, decision.Record{ProjectID: projectID, ID: "keep-cards", Title: "Keep cards", Rationale: "Cards stay reminders", Disposition: "accepted", Actor: "owner", ActorType: "human", Authority: "caller_asserted",
		Links: []decision.Link{{Kind: "subject", TargetKind: "roadmap_node", TargetID: "cards"}, {Kind: "resulted_in", TargetKind: "commit", TargetID: "0123456789abcdef0123456789abcdef01234567"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decisions.Append(ctx, decision.Record{ProjectID: projectID, ID: "keep-cards-v2", Title: "Keep cards, add topics", Rationale: "Topics group cards", Disposition: "accepted", Actor: "owner", ActorType: "human", Authority: "user_reported", ReviewSource: "review session", SupersedesID: first.ID}); err != nil {
		t.Fatal(err)
	}
	topics := topic.Service{DB: db, Now: clock}
	if _, err := topics.Record(ctx, topic.Topic{ProjectID: projectID, ID: "cards", Title: "Cards", Summary: "Reference cards", Status: "proposed", Creator: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err := topics.Record(ctx, topic.Topic{ProjectID: projectID, ID: "cards", Title: "Reference cards", Summary: "Accepted scope", Status: "accepted", Creator: "owner"}); err != nil {
		t.Fatal(err)
	}
	if err := topics.Alias(ctx, projectID, "cards", "memory-jogs", "owner"); err != nil {
		t.Fatal(err)
	}
	proposal, err := topics.AddLink(ctx, topic.Link{ProjectID: projectID, TopicID: "cards", TargetKind: "decision", TargetID: "keep-cards-v2", Creator: "agent", Model: "model-x"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := topics.AcceptLink(ctx, projectID, proposal.ID, "owner"); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadOperationalRecords(ctx, db, projectID)
	if err != nil {
		t.Fatal(err)
	}
	counts := countSpecs(loaded.Records)
	for recordType, want := range map[string]int{"decision": 2, "topic_revision": 2, "topic_alias": 1, "topic_link": 2} {
		if counts[recordType] != want {
			t.Fatalf("%s count=%d want=%d; all=%#v", recordType, counts[recordType], want, counts)
		}
	}
	bundle, err := BuildBundle(BundleSpec{ExportID: "export:ledger", LineageID: lineage, SourceInstanceID: identity.InstanceID, CreatedAt: recorded.Add(time.Hour), SourceDatabaseSchema: loaded.DatabaseSchema, SelectionProfile: "curated-v1", RedactionProfile: DefaultRedactionProfile(), Omissions: CuratedV1Omissions()}, loaded.Records)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bundle.RecordsBytes), projectID) {
		t.Fatal("ledger export leaked the source project ID")
	}
	// Retention may avoid reconstruction, but must preserve every family and
	// its references, including topic revisions and decision supersessions.
	retained, err := EncodeMemoryExport(ctx, db, projectID)
	if err != nil {
		t.Fatal(err)
	}
	stable, err := EncodeMemoryExport(ctx, db, projectID)
	if err != nil {
		t.Fatal(err)
	}
	for i, spec := range loaded.Records {
		want, err := EncodeEnvelope(spec)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(want.Bytes, retained.Envelopes[i].Bytes) || !bytes.Equal(want.Bytes, stable.Envelopes[i].Bytes) {
			t.Fatalf("retention changed %s %s", spec.RecordType, spec.RecordID)
		}
	}

	targetCtx, targetDB, targetProject := operationalProject(t, "ledger-target")
	targetIdentity, err := (Service{DB: targetDB}).Initialize(targetCtx, targetProject, lineage)
	if err != nil {
		t.Fatal(err)
	}
	applied, err := ApplyOperationalImport(targetCtx, targetDB, targetProject, bundle.ManifestBytes, bundle.RecordsBytes, recorded.Add(2*time.Hour))
	if err != nil || applied.Receipt == nil || applied.Receipt.AppendedRecords != len(bundle.Records) {
		t.Fatalf("apply: %#v %v", applied, err)
	}
	for table, want := range map[string]int{"decision_records": 2, "decision_links": 2, "topic_revisions": 2, "topic_aliases": 1, "topic_links": 2} {
		if got := tableCount(t, targetDB, table); got != want {
			t.Fatalf("%s count=%d want=%d", table, got, want)
		}
	}
	// record_sha256 is recomputed for the destination project and still verifies.
	shown, err := (decision.Service{DB: targetDB}).Show(targetCtx, targetProject, "keep-cards", 0)
	if err != nil || len(shown.Links) != 2 || shown.SupersededBy != "keep-cards-v2" {
		t.Fatalf("imported decision: %#v %v", shown, err)
	}
	shown.ProjectID = targetProject
	if hash, _ := decision.SemanticHash(shown); hash != shown.RecordSHA256 || shown.RecordSHA256 == first.RecordSHA256 {
		t.Fatalf("destination hash %s, stored %s, source %s", hash, shown.RecordSHA256, first.RecordSHA256)
	}
	if detail, err := (topic.Service{DB: targetDB}).Show(targetCtx, targetProject, "memory-jogs"); err != nil || detail.Topic.Title != "Reference cards" || len(detail.Links) != 1 {
		t.Fatalf("imported topic: %#v %v", detail, err)
	}

	reloaded, err := LoadOperationalRecords(targetCtx, targetDB, targetProject)
	if err != nil {
		t.Fatal(err)
	}
	reexport, err := BuildBundle(BundleSpec{ExportID: "export:ledger-clone", LineageID: lineage, SourceInstanceID: targetIdentity.InstanceID, CreatedAt: recorded.Add(3 * time.Hour), SourceDatabaseSchema: reloaded.DatabaseSchema, SelectionProfile: "curated-v1", RedactionProfile: DefaultRedactionProfile(), Omissions: CuratedV1Omissions()}, reloaded.Records)
	if err != nil {
		t.Fatal(err)
	}
	if string(bundle.RecordsBytes) != string(reexport.RecordsBytes) {
		t.Fatal("ledger clone re-export changed semantic records")
	}
	replay, err := ApplyOperationalImport(targetCtx, targetDB, targetProject, bundle.ManifestBytes, bundle.RecordsBytes, recorded.Add(4*time.Hour))
	if err != nil || replay.Receipt == nil || replay.Receipt.AlreadyPresentRecords != len(bundle.Records) || replay.Receipt.AppendedRecords != 0 {
		t.Fatalf("replay: %#v %v", replay, err)
	}
	// New operational identities must still be discovered after older ledger
	// records have retained envelopes.
	if _, err := decisions.Append(ctx, decision.Record{ProjectID: projectID, ID: "third", Title: "Third decision", Rationale: "Follow up", Disposition: "accepted", Actor: "owner", ActorType: "human", Authority: "caller_asserted", SupersedesID: "keep-cards-v2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := topics.Record(ctx, topic.Topic{ProjectID: projectID, ID: "cards", Title: "Cards revised", Status: "accepted", Creator: "owner"}); err != nil {
		t.Fatal(err)
	}
	mixed, err := EncodeMemoryExport(ctx, db, projectID)
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string][]byte{}
	for _, e := range mixed.Envelopes {
		byKey[RecordKey(e.Envelope.RecordType, e.Envelope.RecordID, e.Envelope.RecordRevision)] = e.Bytes
	}
	if len(mixed.Envelopes) != len(stable.Envelopes)+2 || len(byKey[RecordKey("decision", "third", 0)]) == 0 || len(byKey[RecordKey("topic_revision", "cards", 3)]) == 0 {
		t.Fatal("mixed export lost new ledger identities")
	}
	for _, e := range stable.Envelopes {
		if !bytes.Equal(e.Bytes, byKey[RecordKey(e.Envelope.RecordType, e.Envelope.RecordID, e.Envelope.RecordRevision)]) {
			t.Fatal("mixed export changed retained ledger bytes")
		}
	}
}

func TestLedgerImportRefusesCollisionsAndMissingDependencies(t *testing.T) {
	ctx, db, projectID := operationalProject(t, "ledger-source")
	lineage := "lineage:2e2e2e2e2e2e2e2e2e2e2e2e2e2e2e2e"
	identity, err := (Service{DB: db}).Initialize(ctx, projectID, lineage)
	if err != nil {
		t.Fatal(err)
	}
	decisions := decision.Service{DB: db}
	if _, err := decisions.Append(ctx, decision.Record{ProjectID: projectID, ID: "shared", Title: "Source title", Rationale: "Source", Disposition: "accepted", Actor: "owner", ActorType: "human", Authority: "caller_asserted"}); err != nil {
		t.Fatal(err)
	}
	if _, err := decisions.Append(ctx, decision.Record{ProjectID: projectID, ID: "follow-up", Title: "Follow up", Rationale: "Builds on shared", Disposition: "accepted", Actor: "owner", ActorType: "human", Authority: "caller_asserted", SupersedesID: "shared"}); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadOperationalRecords(ctx, db, projectID)
	if err != nil {
		t.Fatal(err)
	}
	build := func(records []EnvelopeSpec) Bundle {
		t.Helper()
		bundle, err := BuildBundle(BundleSpec{ExportID: "export:ledger-boundaries", LineageID: lineage, SourceInstanceID: identity.InstanceID, CreatedAt: time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC), SourceDatabaseSchema: loaded.DatabaseSchema, SelectionProfile: "curated-v1", RedactionProfile: DefaultRedactionProfile(), Omissions: CuratedV1Omissions()}, records)
		if err != nil {
			t.Fatal(err)
		}
		return bundle
	}

	// Same decision identity with different content is a visible collision.
	targetCtx, targetDB, targetProject := operationalProject(t, "ledger-collision")
	if _, err := (Service{DB: targetDB}).Initialize(targetCtx, targetProject, lineage); err != nil {
		t.Fatal(err)
	}
	if _, err := (decision.Service{DB: targetDB}).Append(targetCtx, decision.Record{ProjectID: targetProject, ID: "shared", Title: "Different title", Rationale: "Target", Disposition: "accepted", Actor: "owner", ActorType: "human", Authority: "caller_asserted"}); err != nil {
		t.Fatal(err)
	}
	full := build(loaded.Records)
	plan, err := PlanOperationalImport(targetCtx, targetDB, targetProject, full.ManifestBytes, full.RecordsBytes)
	if err != nil || plan.ApplyAllowed || plan.Counts[PlanCollision] != 1 {
		t.Fatalf("collision plan: %#v %v", plan, err)
	}

	// A superseding decision whose predecessor is absent everywhere is refused.
	var partial []EnvelopeSpec
	for _, record := range loaded.Records {
		if record.RecordType != "decision" || record.RecordID == "follow-up" {
			partial = append(partial, record)
		}
	}
	emptyCtx, emptyDB, emptyProject := operationalProject(t, "ledger-empty")
	if _, err := (Service{DB: emptyDB}).Initialize(emptyCtx, emptyProject, lineage); err != nil {
		t.Fatal(err)
	}
	orphan := build(partial)
	if _, err := ApplyOperationalImport(emptyCtx, emptyDB, emptyProject, orphan.ManifestBytes, orphan.RecordsBytes, time.Time{}); err == nil || !strings.Contains(err.Error(), "supersedes a missing decision") {
		t.Fatalf("orphan supersession: %v", err)
	}
	if got := tableCount(t, emptyDB, "decision_records"); got != 0 {
		t.Fatalf("refused import wrote %d decisions", got)
	}
}
