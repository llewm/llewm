package memsync

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

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
	reference := localHistoryMachine(t, "recovery-reference")
	indexed := localHistoryMachine(t, "recovery-indexed")
	want, wantErr := (recordsync.Importer{DB: reference.db, ProjectID: reference.project}).Import(ctx, objects)
	got, gotErr := (recordsync.Index{DB: indexed.db, ProjectID: indexed.project, Lineage: testLineage}).ImportBlocks(ctx, blocks)
	if wantErr != nil || fmt.Sprint(wantErr) != fmt.Sprint(gotErr) || want != got {
		t.Fatalf("recovery import differs: full=%+v %v indexed=%+v %v", want, wantErr, got, gotErr)
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
