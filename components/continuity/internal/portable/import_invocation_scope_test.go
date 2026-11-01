package portable

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/attribution"
	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/decision"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
)

const scopeLineage = "lineage:abababababababababababababababab"

func invocationScopeProject(t *testing.T) (context.Context, *sql.DB, string) {
	t.Helper()
	ctx, db, project := operationalProject(t, "invocation-scope")
	if _, err := (Service{DB: db}).Initialize(ctx, project, scopeLineage); err != nil {
		t.Fatal(err)
	}
	return ctx, db, project
}
func scopeActor(t *testing.T, ctx context.Context, db *sql.DB, project, id, parent string) EnvelopeSpec {
	t.Helper()
	at := time.Unix(1234567890, 123456789).UTC()
	value, err := (attribution.Service{DB: db}).AppendInvocation(ctx, attribution.Invocation{ID: id, ProjectID: project, ParentInvocationID: parent, ActorType: "agent", IdentitySource: "caller_asserted", OccurredAt: at, ReceivedAt: at})
	if err != nil {
		t.Fatal(err)
	}
	spec, err := invocationSpec(scopeLineage, cachedPortableText(""), value)
	if err != nil {
		t.Fatal(err)
	}
	return spec
}
func scopeSnapshot(t *testing.T, ctx context.Context, db *sql.DB, project string, objects []EncodedEnvelope) map[string]EncodedEnvelope {
	t.Helper()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	local, err := LocalEnvelopesForImport(ctx, tx, project, objects)
	if err != nil {
		t.Fatal(err)
	}
	return local
}

// This is the former whole-row reader, independent of the new selector. It is
// only a test reference; production no longer validates unrelated attribution.
func fullInvocationReference(t *testing.T, ctx context.Context, db *sql.DB, project string) map[string]EncodedEnvelope {
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
	validated := make(snapshotEncodings)
	specs, _, err := loadOperationalSpecs(ctx, tx, project, root, identity.LineageID, true, validated, nil)
	if err != nil {
		t.Fatal(err)
	}
	local := make(map[string]EncodedEnvelope)
	for _, spec := range specs {
		e, err := validated.encode(ctx, spec)
		if err != nil {
			t.Fatal(err)
		}
		local[recordKey(e.Envelope)] = e
	}
	return local
}

func TestScopedInvocationPlanningKeepsRelevantValidation(t *testing.T) {
	ctx, db, project := invocationScopeProject(t)
	for i := 0; i < 129; i++ {
		if _, err := (decision.Service{DB: db}).Append(ctx, decision.Record{ProjectID: project, ID: fmt.Sprintf("record-%03d", i), Title: "Decision", Rationale: "Observation", Disposition: "accepted", Actor: "owner", ActorType: "human", Authority: "caller_asserted"}); err != nil {
			t.Fatal(err)
		}
	}
	export, err := EncodeMemoryExport(ctx, db, project)
	if err != nil {
		t.Fatal(err)
	}
	var specs []EnvelopeSpec
	for _, e := range export.Envelopes {
		s, err := DecodeMemoryEnvelope(e.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		specs = append(specs, s)
	}
	scopeActor(t, ctx, db, project, "unused", "")
	if _, err := db.ExecContext(ctx, `UPDATE actor_invocations SET received_at='not-a-time' WHERE id='unused'`); err != nil {
		t.Fatal(err)
	}
	for _, size := range []int{1, 129} {
		measured, work := syncwork.WithWorkMetrics(ctx)
		plan, receipt, err := ImportMemoryRecords(measured, db, project, specs[:size], time.Now())
		if err != nil || !plan.ApplyAllowed || receipt != nil || plan.Counts[PlanAlreadyPresent] != size {
			t.Fatalf("size%d unrelated raw corruption affected import: %+v %v", size, plan, err)
		}
		if work.Counts()["invocation_rows_loaded"] != 0 {
			t.Fatalf("unrelated actor loaded: %v", work.Counts())
		}
	}
	incoming := scopeActor(t, ctx, db, project, "incoming", "")
	collision := incoming
	var payload invocationPayload
	if err := json.Unmarshal(collision.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	payload.Model = "different"
	collision.Payload, _ = json.Marshal(payload)
	collision.Index, _ = json.Marshal(envelopeIndex{Title: payload.Model})
	for _, bundle := range [][]EnvelopeSpec{{collision}, append(append([]EnvelopeSpec{}, specs...), collision)} {
		plan, _, err := ImportMemoryRecords(ctx, db, project, bundle, time.Now())
		if err != nil || plan.ApplyAllowed || plan.Counts[PlanCollision] != 1 {
			t.Fatalf("incoming unreferenced actor collision escaped: %+v %v", plan, err)
		}
	}
	if _, err := db.ExecContext(ctx, `UPDATE actor_invocations SET received_at='not-a-time' WHERE id='incoming'`); err != nil {
		t.Fatal(err)
	}
	for _, bundle := range [][]EnvelopeSpec{{incoming}, append(append([]EnvelopeSpec{}, specs...), incoming)} {
		if _, _, err := ImportMemoryRecords(ctx, db, project, bundle, time.Now()); err == nil {
			t.Fatal("relevant malformed actor accepted")
		}
	}
	var retained string
	if err := db.QueryRowContext(ctx, `SELECT received_at FROM actor_invocations WHERE id='unused'`).Scan(&retained); err != nil || retained != "not-a-time" {
		t.Fatalf("raw observation changed: %q %v", retained, err)
	}
}

func TestScopedInvocationClosureFollowsRetainedReferences(t *testing.T) {
	ctx, db, project := invocationScopeProject(t)
	for _, v := range []struct{ id, parent string }{{"wire-parent", ""}, {"wire-leaf", "wire-parent"}, {"raw-leaf", ""}, {"unused", ""}} {
		scopeActor(t, ctx, db, project, v.id, v.parent)
	}
	if _, err := (worknarrative.Service{DB: db}).Append(ctx, worknarrative.Record{ID: "record", ProjectID: project, Title: "Retained binding", Trigger: "manual", Creator: "test", CreatorInvocationID: "raw-leaf", Claims: []worknarrative.Claim{{Kind: "outcome", Text: "Observation", Authority: "caller_asserted"}}}); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadOperationalRecords(ctx, db, project)
	if err != nil {
		t.Fatal(err)
	}
	var narrative EnvelopeSpec
	for _, spec := range loaded.Records {
		if spec.RecordType == "work_narrative" {
			narrative = spec
		}
	}
	var payload workNarrativePayload
	if err := json.Unmarshal(narrative.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	payload.CreatorInvocationID = "wire-leaf"
	narrative.Payload, _ = json.Marshal(payload)
	for i := range narrative.Refs {
		if narrative.Refs[i].Kind == "actor_invocation" {
			narrative.Refs[i].ID = "wire-leaf"
		}
	}
	encoded, err := EncodeEnvelope(narrative)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := retainEnvelope(ctx, tx, project, "export", encoded); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	want := fullInvocationReference(t, ctx, db, project)
	delete(want, RecordKey("actor_invocation", "unused", 0))
	if _, err := db.ExecContext(ctx, `UPDATE actor_invocations SET received_at='not-a-time' WHERE id='unused'`); err != nil {
		t.Fatal(err)
	}
	measured, work := syncwork.WithWorkMetrics(ctx)
	got := scopeSnapshot(t, measured, db, project, nil)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scoped closure differs from reference: keys%d/%d", len(got), len(want))
	}
	if work.Counts()["import_invocation_closure_rounds"] != 2 || work.Counts()["invocation_rows_loaded"] != 4 {
		t.Fatalf("retained closure not reached as expected: %v", work.Counts())
	}
	for _, id := range []string{"raw-leaf", "wire-leaf", "wire-parent"} {
		if _, ok := got[RecordKey("actor_invocation", id, 0)]; !ok {
			t.Fatalf("required actor omitted: %s", id)
		}
	}
	if _, err := db.ExecContext(ctx, `UPDATE actor_invocations SET received_at='not-a-time' WHERE id='wire-parent'`); err != nil {
		t.Fatal(err)
	}
	tx, err = db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := LocalEnvelopesForImport(ctx, tx, project, nil); err == nil {
		t.Fatal("malformed retained-reference ancestor accepted")
	}
}

func TestScopedInvocationRetainedIdentityIsARoot(t *testing.T) {
	ctx, db, project := invocationScopeProject(t)
	spec := scopeActor(t, ctx, db, project, "retained", "")
	encoded, err := EncodeEnvelope(spec)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := retainEnvelope(ctx, tx, project, "export", encoded); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	got := scopeSnapshot(t, ctx, db, project, nil)
	if !bytes.Equal(got[recordKey(encoded.Envelope)].Bytes, encoded.Bytes) {
		t.Fatal("retained unreferenced actor omitted")
	}
	if _, err := db.ExecContext(ctx, `UPDATE actor_invocations SET received_at='not-a-time' WHERE id='retained'`); err != nil {
		t.Fatal(err)
	}
	tx, err = db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := LocalEnvelopesForImport(ctx, tx, project, nil); err == nil {
		t.Fatal("malformed retained actor ignored")
	}
}

func TestScopedInvocationSnapshotUsesCallerTransaction(t *testing.T) {
	ctx, db, project := invocationScopeProject(t)
	for _, v := range []struct{ id, parent string }{{"before", ""}, {"after", ""}, {"leaf", "before"}} {
		scopeActor(t, ctx, db, project, v.id, v.parent)
	}
	if _, err := (worknarrative.Service{DB: db}).Append(ctx, worknarrative.Record{ID: "record", ProjectID: project, Title: "Snapshot", Trigger: "manual", Creator: "test", CreatorInvocationID: "leaf", Claims: []worknarrative.Claim{{Kind: "outcome", Text: "Observation", Authority: "caller_asserted"}}}); err != nil {
		t.Fatal(err)
	}
	path := sourceDBPath(t, db)
	other, err := database.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	before, err := LocalEnvelopesForImport(ctx, tx, project, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.ExecContext(ctx, `UPDATE actor_invocations SET parent_invocation_id='after' WHERE id='leaf'`); err != nil {
		t.Fatal(err)
	}
	held, err := LocalEnvelopesForImport(ctx, tx, project, nil)
	if err != nil || !reflect.DeepEqual(held, before) {
		t.Fatalf("snapshot crossed a commit: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	after := scopeSnapshot(t, ctx, db, project, nil)
	if _, ok := after[RecordKey("actor_invocation", "after", 0)]; !ok {
		t.Fatal("new snapshot missed new parent")
	}
	if _, ok := after[RecordKey("actor_invocation", "before", 0)]; ok {
		t.Fatal("unreferenced old parent retained")
	}
}

func TestScopedInvocationSourceRejectsUnknownQueries(t *testing.T) {
	source := &importInvocationSource{project: "project", roots: map[string]struct{}{}}
	for _, query := range []string{"SELECT * FROM actor_invocations WHERE project_id=?", "SELECT id FROM actor_invocations\nWHERE project_id=?", "SELECT id FROM ACTOR_INVOCATIONS WHERE project_id=?"} {
		if _, err := source.QueryContext(context.Background(), query, "project"); err == nil {
			t.Fatal("unknown actor query shape accepted")
		}
	}
	if _, err := source.QueryContext(context.Background(), invocationRecordsQuery+" ORDER BY occurred_at,id", "other"); err == nil {
		t.Fatal("foreign project query accepted")
	}
	source.roots["invalid\xff"] = struct{}{}
	if err := source.closeParents(context.Background()); err == nil {
		t.Fatal("non-UTF8 root silently altered")
	}
}

func TestScopedInvocationRecoveryIncludesBothInterpretations(t *testing.T) {
	ctx, db, project := invocationScopeProject(t)
	for _, v := range []struct{ id, parent string }{{"ancestor", ""}, {"project-0123456789abcdef", "ancestor"}, {"[PROJECT_ID]", ""}, {"unused", ""}} {
		scopeActor(t, ctx, db, project, v.id, v.parent)
	}
	original, _, _ := recoveryInvalidationFixture(t)
	spec, err := DecodeMemoryEnvelope(original.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	spec.LineageID = scopeLineage
	var payload workNarrativePayload
	if err := json.Unmarshal(spec.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	payload.CreatorInvocationID = "project-0123456789abcdef"
	spec.Payload, _ = json.Marshal(payload)
	spec.Refs = workNarrativeReferences(payload.Anchors, payload.CreatorInvocationID, payload.Claims)
	original, err = EncodeEnvelope(spec)
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
	recovery, err := RedactionRecoverySpec(RedactionRecovery{Original: original.Bytes, Derived: derived.Bytes, ProjectRoot: "/home/peer/LLMem", ProjectID: "project-0123456789abcdef", Creator: "test", Reason: "Historical re-export"}, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeEnvelope(recovery)
	if err != nil {
		t.Fatal(err)
	}
	check := func(objects []EncodedEnvelope) {
		t.Helper()
		got := scopeSnapshot(t, ctx, db, project, objects)
		for _, id := range []string{"ancestor", "project-0123456789abcdef", "[PROJECT_ID]"} {
			if _, ok := got[RecordKey("actor_invocation", id, 0)]; !ok {
				t.Fatalf("missing recovery dependency %s", id)
			}
		}
		if _, ok := got[RecordKey("actor_invocation", "unused", 0)]; ok {
			t.Fatal("unrelated actor loaded")
		}
	}
	check([]EncodedEnvelope{encoded})
	// Persist just the proof, without creating its embedded narrative. A local
	// proof must retain both dependency interpretations on subsequent snapshots.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := appendRedactionRecovery(ctx, tx, project, encoded); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	check(nil)
	if _, err := db.ExecContext(ctx, "UPDATE actor_invocations SET received_at='bad' WHERE id='ancestor'"); err != nil {
		t.Fatal(err)
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := LocalEnvelopesForImport(ctx, tx, project, []EncodedEnvelope{encoded}); err == nil {
		t.Fatal("malformed recovery ancestor ignored")
	}
}

func TestScopedInvocationClosureBatchesIncomingRoots(t *testing.T) {
	ctx, db, project := invocationScopeProject(t)
	var objects []EncodedEnvelope
	for i := 0; i < 515; i++ {
		id := fmt.Sprintf("actor-%03d", i)
		parent := ""
		if i > 0 {
			parent = fmt.Sprintf("actor-%03d", i-1)
		}
		scopeActor(t, ctx, db, project, id, parent)
		objects = append(objects, EncodedEnvelope{Envelope: Envelope{RecordType: "actor_invocation", RecordID: id}})
	}
	measured, work := syncwork.WithWorkMetrics(ctx)
	got := scopeSnapshot(t, measured, db, project, objects)
	if len(got) != 515 || work.Counts()["invocation_rows_loaded"] != 515 {
		t.Fatalf("incomplete batched roots: records%d counts%v", len(got), work.Counts())
	}
	want := fullInvocationReference(t, ctx, db, project)
	if !reflect.DeepEqual(got, want) {
		t.Fatal("batched closure differs from full reference")
	}
}
