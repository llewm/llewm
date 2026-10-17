package portable

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
	"github.com/llewm/llewm/components/continuity/internal/procedures"
	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
)

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
