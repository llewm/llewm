package memsync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
	"github.com/llewm/llewm/components/continuity/internal/fork"
	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/procedures"
	"github.com/llewm/llewm/components/continuity/internal/recordsync"
	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
)

func TestBlockRedactionRepairUsesTrackedObservations(t *testing.T) {
	ctx := context.Background()
	m := newMachine(t, "block-repair", bareRemote(t))
	projectRoot, peerProject := "/different/peer/root", "project-0123456789abcdef"
	if _, err := (worknarrative.Service{DB: m.db}).Append(ctx, worknarrative.Record{ID: "repair-block", ProjectID: m.project, Title: "Original", Trigger: "manual", Creator: "test", Claims: []worknarrative.Claim{{Kind: "outcome", Text: projectRoot + " " + peerProject, Authority: "caller_asserted"}}}); err != nil {
		t.Fatal(err)
	}
	export, err := portable.EncodeMemoryExport(ctx, m.db, m.project)
	if err != nil || len(export.Envelopes) != 1 {
		t.Fatalf("export: %d %v", len(export.Envelopes), err)
	}
	original := export.Envelopes[0]
	spec, err := portable.DecodeMemoryEnvelope(original.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	spec.Payload = []byte(strings.ReplaceAll(strings.ReplaceAll(string(spec.Payload), projectRoot, "[PROJECT_ROOT]"), peerProject, "[PROJECT_ID]"))
	derived, err := portable.EncodeEnvelope(spec)
	if err != nil {
		t.Fatal(err)
	}
	block, err := recordsync.EncodeBlock(testLineage, []portable.EncodedEnvelope{original, derived})
	if err != nil {
		t.Fatal(err)
	}
	root, err := recordsync.OpenRoot(m.syncer.Clone)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recordsync.PublishBlock(root, testLineage, block); err != nil {
		root.Close()
		t.Fatal(err)
	}
	root.Close()
	repo, err := m.syncer.repository(ctx)
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := recordsync.BlockDirectory(testLineage)
	if err := commitBlocks(ctx, repo, []string{filepath.ToSlash(filepath.Join(dir, block.Hash+".block"))}, "fixture: repair observations"); err != nil {
		t.Fatal(err)
	}
	preview, err := m.syncer.RepairRedaction(ctx, original.Envelope.EnvelopeSHA256, derived.Envelope.EnvelopeSHA256, projectRoot, peerProject, "test", "historical re-export", "", false)
	if err != nil {
		t.Fatal(err)
	}
	result, err := m.syncer.RepairRedaction(ctx, original.Envelope.EnvelopeSHA256, derived.Envelope.EnvelopeSHA256, projectRoot, peerProject, "test", "historical re-export", preview.RecoveryID, true)
	if err != nil || !result.Applied {
		t.Fatalf("repair: %+v %v", result, err)
	}
}

func TestRedactionRecoveryConvergesWithoutRewritingObservations(t *testing.T) {
	ctx := context.Background()
	remote := bareRemote(t)
	a := newMachine(t, "source", remote)
	b := newMachine(t, "peer", remote)
	var root string
	if err := b.db.QueryRow(`SELECT root FROM projects WHERE id=?`, b.project).Scan(&root); err != nil {
		t.Fatal(err)
	}
	text := root + " " + b.project
	if _, err := (checkpoint.Service{DB: a.db}).CreateWithActions(ctx, checkpoint.Checkpoint{ProjectID: a.project, TaskID: "recovery", Task: "Recover", Summary: text, NextAction: "Verify", Status: "in_progress"}); err != nil {
		t.Fatal(err)
	}
	if _, err := (worknarrative.Service{DB: a.db}).Append(ctx, worknarrative.Record{ID: "original", ProjectID: a.project, Title: "Original", Trigger: "manual", Creator: "codex", Claims: []worknarrative.Claim{{Kind: "outcome", Text: text, Authority: "caller_asserted"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := (procedures.Service{DB: a.db}).Append(ctx, procedures.Procedure{ProjectID: a.project, ID: "exact", Title: "Exact", Purpose: text, Kind: "runbook", Status: "accepted", Platforms: []string{"linux"}, DeclaredEffects: []string{"install"}, Steps: []procedures.Step{{ID: "copy", Description: "Copy", ArgvTemplate: []string{"copy", text}}}, Source: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`INSERT INTO roadmap_nodes(project_id,node_id,revision,title,status,priority,source_path,authority,recorded_at,schema_version,kind,evidence_refs_json) VALUES(?,'equivalent-node',1,'Node','active',10,'docs/roadmap.md','caller_asserted','2026-01-01T00:00:00Z',1,'work_item','[]')`, a.project); err != nil {
		t.Fatal(err)
	}
	a.sync(t, OutcomeSynced)
	b.sync(t, OutcomeSynced)
	local, err := portable.LocalEnvelopes(ctx, a.db, a.project)
	if err != nil {
		t.Fatal(err)
	}
	type pair struct{ original, derived portable.EncodedEnvelope }
	var pairs []pair
	var variants []portable.EncodedEnvelope
	for _, e := range local {
		if e.Envelope.RecordType == "roadmap_node" {
			spec, err := portable.DecodeMemoryEnvelope(e.Bytes)
			if err != nil {
				t.Fatal(err)
			}
			var payload map[string]any
			if err := json.Unmarshal(spec.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			payload["kind"], payload["evidence_refs"] = "work_item", []string{}
			spec.RecordSchemaVersion = 2
			spec.Payload, err = json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			equivalent, err := portable.EncodeEnvelope(spec)
			if err != nil {
				t.Fatal(err)
			}
			variants = append(variants, equivalent)
			continue
		}
		if e.Envelope.RecordType != "checkpoint" && e.Envelope.RecordType != "work_narrative" && e.Envelope.RecordType != "procedure" {
			continue
		}
		spec, err := portable.DecodeMemoryEnvelope(e.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		spec.Payload = []byte(strings.ReplaceAll(strings.ReplaceAll(string(spec.Payload), root, "[PROJECT_ROOT]"), b.project, "[PROJECT_ID]"))
		d, err := portable.EncodeEnvelope(spec)
		if err != nil {
			t.Fatal(err)
		}
		pairs = append(pairs, pair{e, d})
		variants = append(variants, d)
	}
	// Simulate the historical writer with an append-only block.
	block, err := recordsync.EncodeBlock(testLineage, variants)
	if err != nil {
		t.Fatal(err)
	}
	transportRoot, err := recordsync.OpenRoot(a.syncer.Clone)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recordsync.PublishBlock(transportRoot, testLineage, block); err != nil {
		transportRoot.Close()
		t.Fatal(err)
	}
	transportRoot.Close()
	repo, err := a.syncer.repository(ctx)
	if err != nil {
		t.Fatal(err)
	}
	directory, _ := recordsync.BlockDirectory(testLineage)
	if err := commitBlocks(ctx, repo, []string{filepath.ToSlash(filepath.Join(directory, block.Hash+".block"))}, "fixture: historical re-export"); err != nil {
		t.Fatal(err)
	}
	collision := a.sync(t, OutcomeCollision)
	if collision.Collisions != 2 || collision.Forks != 1 {
		t.Fatalf("fixture: %#v", collision)
	}
	b.sync(t, OutcomeCollision)
	for index, p := range pairs {
		preview, err := a.syncer.RepairRedaction(ctx, p.original.Envelope.EnvelopeSHA256, p.derived.Envelope.EnvelopeSHA256, root, b.project, "codex", "Historical peer re-export", "", false)
		if err != nil {
			t.Fatal(err)
		}
		var count int
		if err := a.db.QueryRow(`SELECT COUNT(*) FROM redaction_recoveries`).Scan(&count); err != nil || count != index {
			t.Fatalf("preview wrote: %d %v", count, err)
		}
		if _, err := a.syncer.RepairRedaction(ctx, p.original.Envelope.EnvelopeSHA256, p.derived.Envelope.EnvelopeSHA256, root, b.project, "codex", "Historical peer re-export", "wrong", true); err == nil {
			t.Fatal("apply accepted wrong preview ID")
		}
		applied, err := a.syncer.RepairRedaction(ctx, p.original.Envelope.EnvelopeSHA256, p.derived.Envelope.EnvelopeSHA256, root, b.project, "codex", "Historical peer re-export", preview.RecoveryID, true)
		if err != nil || !applied.Applied {
			t.Fatalf("apply: %#v %v", applied, err)
		}
		replay, err := a.syncer.RepairRedaction(ctx, p.original.Envelope.EnvelopeSHA256, p.derived.Envelope.EnvelopeSHA256, root, b.project, "codex", "Historical peer re-export", preview.RecoveryID, true)
		if err != nil || replay.Applied || replay.Receipt != nil {
			t.Fatalf("replay: %#v %v", replay, err)
		}
	}
	for _, m := range []machine{a, b, a, b} {
		result := m.sync(t, OutcomeSynced)
		if result.Collisions != 0 || result.Forks != 0 || result.UnpushedCommits != 0 {
			t.Fatalf("not converged: %#v", result)
		}
		heads, err := fork.RevisionForks(ctx, m.db, m.project, "")
		if err != nil || len(heads) != 0 {
			t.Fatalf("forks: %#v %v", heads, err)
		}
	}
	fresh := newMachine(t, "fresh", remote)
	fresh.sync(t, OutcomeSynced)
	// Replay the recovered history into two empty projects and compare the
	// block-backed importer with the current full scan.
	blocks, err := a.syncer.trackedBlocks(ctx, repo, testLineage)
	if err != nil {
		t.Fatal(err)
	}
	var objects []portable.EncodedEnvelope
	for _, block := range blocks {
		objects = append(objects, block.Objects...)
	}
	// Retired records remain transport-valid but are ignored by the full
	// importer. Their presence must not block an otherwise valid proof.
	retired := objects[0].Envelope
	retired.RecordType, retired.EnvelopeSHA256 = "contract", ""
	unsigned, err := json.Marshal(retired)
	if err != nil {
		t.Fatal(err)
	}
	unsigned, err = portable.CanonicalizeJSON(unsigned)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(unsigned)
	retired.EnvelopeSHA256 = hex.EncodeToString(sum[:])
	wire, err := json.Marshal(retired)
	if err != nil {
		t.Fatal(err)
	}
	wire, err = portable.CanonicalizeJSON(wire)
	if err != nil {
		t.Fatal(err)
	}
	retiredObject, err := portable.DecodeTransportEnvelope(wire)
	if err != nil {
		t.Fatal(err)
	}
	retiredBlock, err := recordsync.EncodeBlock(testLineage, []portable.EncodedEnvelope{retiredObject})
	if err != nil {
		t.Fatal(err)
	}
	objects = append(objects, retiredObject)
	blocks = append(blocks, retiredBlock)
	reference := localHistoryMachine(t, "recovery-reference")
	indexed := localHistoryMachine(t, "recovery-indexed")
	want, wantErr := (recordsync.Importer{DB: reference.db, ProjectID: reference.project}).Import(ctx, objects)
	got, gotErr := (recordsync.Index{DB: indexed.db, ProjectID: indexed.project, Lineage: testLineage}).ImportBlocks(ctx, blocks)
	if wantErr != nil || fmt.Sprint(wantErr) != fmt.Sprint(gotErr) || want != got {
		t.Fatalf("recovery import differs: full=%+v %v indexed=%+v %v", want, wantErr, got, gotErr)
	}
	// After the initial import, one full unchanged replay can establish a proof;
	// the following replay must preserve the reference importer's result.
	for i := 0; i < 2; i++ {
		want, wantErr = (recordsync.Importer{DB: reference.db, ProjectID: reference.project}).Import(ctx, objects)
		got, gotErr = (recordsync.Index{DB: indexed.db, ProjectID: indexed.project, Lineage: testLineage}).ImportBlocks(ctx, blocks)
		if wantErr != nil || fmt.Sprint(wantErr) != fmt.Sprint(gotErr) || want != got {
			t.Fatalf("recovery replay %d differs: full=%+v %v indexed=%+v %v", i, want, wantErr, got, gotErr)
		}
	}
	var proofs int
	if err := indexed.db.QueryRow(`SELECT COUNT(*) FROM history_index_valid_sets WHERE project_id=? AND set_hash LIKE 'recovery:%' AND local_state_hash<>''`, indexed.project).Scan(&proofs); err != nil || proofs != 1 {
		t.Fatalf("unchanged recovery proof missing: %d %v", proofs, err)
	}
	reversed := append([]recordsync.Block{}, blocks...)
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}
	var reversedObjects []portable.EncodedEnvelope
	for _, block := range reversed {
		reversedObjects = append(reversedObjects, block.Objects...)
	}
	want, wantErr = (recordsync.Importer{DB: reference.db, ProjectID: reference.project}).Import(ctx, reversedObjects)
	got, gotErr = (recordsync.Index{DB: indexed.db, ProjectID: indexed.project, Lineage: testLineage}).ImportBlocks(ctx, reversed)
	if wantErr != nil || fmt.Sprint(wantErr) != fmt.Sprint(gotErr) || want != got {
		t.Fatalf("reordered equivalent observations differ: full=%+v %v indexed=%+v %v", want, wantErr, got, gotErr)
	}
	var previousState string
	if err := indexed.db.QueryRow(`SELECT local_state_hash FROM history_index_valid_sets WHERE project_id=? AND set_hash LIKE 'recovery:%'`, indexed.project).Scan(&previousState); err != nil {
		t.Fatal(err)
	}
	for _, m := range []machine{reference, indexed} {
		service := worknarrative.Service{DB: m.db, Now: func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }}
		if _, err := service.Append(ctx, worknarrative.Record{ID: "local-change", ProjectID: m.project, Title: "Local change", Trigger: "manual", Creator: "test", Claims: []worknarrative.Claim{{Kind: "outcome", Text: "Changed local state", Authority: "caller_asserted"}}}); err != nil {
			t.Fatal(err)
		}
	}
	want, wantErr = (recordsync.Importer{DB: reference.db, ProjectID: reference.project}).Import(ctx, objects)
	got, gotErr = (recordsync.Index{DB: indexed.db, ProjectID: indexed.project, Lineage: testLineage}).ImportBlocks(ctx, blocks)
	if wantErr != nil || fmt.Sprint(wantErr) != fmt.Sprint(gotErr) || want != got {
		t.Fatalf("changed-state replay differs: full=%+v %v indexed=%+v %v", want, wantErr, got, gotErr)
	}
	var nextState string
	if err := indexed.db.QueryRow(`SELECT local_state_hash FROM history_index_valid_sets WHERE project_id=? AND set_hash LIKE 'recovery:%'`, indexed.project).Scan(&nextState); err != nil || nextState == previousState {
		t.Fatalf("changed local state did not invalidate the proof: %v", err)
	}
	wantExport, err := portable.EncodeMemoryExport(ctx, reference.db, reference.project)
	if err != nil {
		t.Fatal(err)
	}
	gotExport, err := portable.EncodeMemoryExport(ctx, indexed.db, indexed.project)
	if err != nil {
		t.Fatal(err)
	}
	wantRecords, gotRecords := map[string][]byte{}, map[string][]byte{}
	for _, object := range wantExport.Envelopes {
		wantRecords[object.Envelope.EnvelopeSHA256] = object.Bytes
	}
	for _, object := range gotExport.Envelopes {
		gotRecords[object.Envelope.EnvelopeSHA256] = object.Bytes
	}
	if len(wantRecords) != len(gotRecords) {
		t.Fatalf("recovery record counts differ: %d %d", len(wantRecords), len(gotRecords))
	}
	for hash, body := range wantRecords {
		if !bytes.Equal(body, gotRecords[hash]) {
			t.Fatalf("recovery record %s differs", hash)
		}
	}
	// Simulate a lost local correction: an old proof must not suppress its
	// recovery from the immutable input history.
	var correction string
	if err := indexed.db.QueryRow(`SELECT id FROM redaction_recoveries WHERE project_id=? ORDER BY id LIMIT 1`, indexed.project).Scan(&correction); err != nil {
		t.Fatal(err)
	}
	for _, m := range []machine{reference, indexed} {
		// Disposable fixtures only: simulate damage despite the normal
		// append-only trigger, which prevents this mutation in live stores.
		if _, err := m.db.Exec(`DROP TRIGGER redaction_recoveries_no_delete`); err != nil {
			t.Fatal(err)
		}
		if _, err := m.db.Exec(`DELETE FROM redaction_recoveries WHERE project_id=? AND id=?`, m.project, correction); err != nil {
			t.Fatal(err)
		}
	}
	want, wantErr = (recordsync.Importer{DB: reference.db, ProjectID: reference.project}).Import(ctx, objects)
	got, gotErr = (recordsync.Index{DB: indexed.db, ProjectID: indexed.project, Lineage: testLineage}).ImportBlocks(ctx, blocks)
	if wantErr != nil || fmt.Sprint(wantErr) != fmt.Sprint(gotErr) || want != got || want.ImportedRecords == 0 {
		t.Fatalf("missing correction was skipped: full=%+v %v indexed=%+v %v", want, wantErr, got, gotErr)
	}
	// A genuinely different versioned roadmap node must first be recorded as
	// a fork; subsequent unchanged replays can prove the recorded observations.
	var equivalent portable.EncodedEnvelope
	for _, object := range variants {
		if object.Envelope.RecordType == "roadmap_node" {
			equivalent = object
			break
		}
	}
	variantSpec, err := portable.DecodeMemoryEnvelope(equivalent.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(variantSpec.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	payload["kind"] = "milestone"
	variantSpec.Payload, err = json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	variantObject, err := portable.EncodeEnvelope(variantSpec)
	if err != nil {
		t.Fatal(err)
	}
	variantBlock, err := recordsync.EncodeBlock(testLineage, []portable.EncodedEnvelope{variantObject})
	if err != nil {
		t.Fatal(err)
	}
	conflictObjects := append(append([]portable.EncodedEnvelope{}, objects...), variantObject)
	conflictBlocks := append(append([]recordsync.Block{}, blocks...), variantBlock)
	want, wantErr = (recordsync.Importer{DB: reference.db, ProjectID: reference.project}).Import(ctx, conflictObjects)
	got, gotErr = (recordsync.Index{DB: indexed.db, ProjectID: indexed.project, Lineage: testLineage}).ImportBlocks(ctx, conflictBlocks)
	if wantErr != nil || fmt.Sprint(wantErr) != fmt.Sprint(gotErr) || want != got || want.Forks+want.Collisions == 0 {
		t.Fatalf("unrecovered variant was skipped: full=%+v %v indexed=%+v %v", want, wantErr, got, gotErr)
	}
	for i := 0; i < 3; i++ {
		want, wantErr = (recordsync.Importer{DB: reference.db, ProjectID: reference.project}).Import(ctx, conflictObjects)
		got, gotErr = (recordsync.Index{DB: indexed.db, ProjectID: indexed.project, Lineage: testLineage}).ImportBlocks(ctx, conflictBlocks)
		if wantErr != nil || fmt.Sprint(wantErr) != fmt.Sprint(gotErr) || want != got {
			t.Fatalf("known fork replay %d differs: full=%+v %v indexed=%+v %v", i, want, wantErr, got, gotErr)
		}
	}
	if err := indexed.db.QueryRow(`SELECT COUNT(*) FROM history_index_valid_sets WHERE project_id=? AND set_hash LIKE 'recovery:v3:%' AND local_state_hash<>''`, indexed.project).Scan(&proofs); err != nil || proofs < 2 {
		t.Fatalf("known fork replay did not establish a proof: %d %v", proofs, err)
	}
	// Simulate damage to one append-only fork observation in disposable
	// fixtures. A prior proof must not suppress the reference repair.
	variantHash := variantObject.Envelope.EnvelopeSHA256
	for _, m := range []machine{reference, indexed} {
		if _, err := m.db.Exec(`DROP TRIGGER fork_head_observations_no_delete`); err != nil {
			t.Fatal(err)
		}
		result, err := m.db.Exec(`DELETE FROM fork_head_observations WHERE project_id=? AND envelope_sha256=?`, m.project, variantHash)
		if err != nil {
			t.Fatal(err)
		}
		if deleted, err := result.RowsAffected(); err != nil || deleted != 1 {
			t.Fatalf("fixture observation not removed: %d %v", deleted, err)
		}
	}
	want, wantErr = (recordsync.Importer{DB: reference.db, ProjectID: reference.project}).Import(ctx, conflictObjects)
	got, gotErr = (recordsync.Index{DB: indexed.db, ProjectID: indexed.project, Lineage: testLineage}).ImportBlocks(ctx, conflictBlocks)
	if wantErr != nil || fmt.Sprint(wantErr) != fmt.Sprint(gotErr) || want != got {
		t.Fatalf("missing fork observation replay differs: full=%+v %v indexed=%+v %v", want, wantErr, got, gotErr)
	}
	for _, m := range []machine{reference, indexed} {
		var found int
		if err := m.db.QueryRow(`SELECT COUNT(*) FROM fork_head_observations WHERE project_id=? AND envelope_sha256=?`, m.project, variantHash).Scan(&found); err != nil || found != 1 {
			t.Fatalf("fork observation was not restored: %d %v", found, err)
		}
	}
	// Revision-zero competing narratives are collisions, never recorded forks.
	variant := localHistoryMachine(t, "recovery-collision")
	if _, err := (worknarrative.Service{DB: variant.db}).Append(ctx, worknarrative.Record{ID: "original", ProjectID: variant.project, Title: "Different observation", Trigger: "manual", Creator: "codex", Claims: []worknarrative.Claim{{Kind: "outcome", Text: "Unrelated content", Authority: "caller_asserted"}}}); err != nil {
		t.Fatal(err)
	}
	variantExport, err := portable.EncodeMemoryExport(ctx, variant.db, variant.project)
	if err != nil || len(variantExport.Envelopes) != 1 {
		t.Fatalf("collision export: %d %v", len(variantExport.Envelopes), err)
	}
	collisionBlock, err := recordsync.EncodeBlock(testLineage, variantExport.Envelopes)
	if err != nil {
		t.Fatal(err)
	}
	collisionObjects := append(append([]portable.EncodedEnvelope{}, objects...), variantExport.Envelopes...)
	collisionBlocks := append(append([]recordsync.Block{}, blocks...), collisionBlock)
	want, wantErr = (recordsync.Importer{DB: reference.db, ProjectID: reference.project}).Import(ctx, collisionObjects)
	got, gotErr = (recordsync.Index{DB: indexed.db, ProjectID: indexed.project, Lineage: testLineage}).ImportBlocks(ctx, collisionBlocks)
	if wantErr != nil || fmt.Sprint(wantErr) != fmt.Sprint(gotErr) || want != got || want.Collisions == 0 {
		t.Fatalf("revision-zero collision was skipped: full=%+v %v indexed=%+v %v", want, wantErr, got, gotErr)
	}
	for _, m := range []machine{a, b, fresh} {
		procedure, err := (procedures.Service{DB: m.db}).Get(ctx, m.project, "exact", 1)
		if err != nil || procedure.Steps[0].ArgvTemplate[1] != text {
			t.Fatalf("lost exact procedure: %#v %v", procedure, err)
		}
		repo, err := m.syncer.repository(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range pairs {
			for _, e := range []portable.EncodedEnvelope{p.original, p.derived} {
				bodies, err := m.syncer.trackedBlockObservations(ctx, repo, testLineage, e.Envelope.EnvelopeSHA256)
				if err != nil || !bytes.Equal(bodies[e.Envelope.EnvelopeSHA256], e.Bytes) {
					t.Fatal("immutable observation changed")
				}
			}
		}
	}
}
