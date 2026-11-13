package memsync

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/recordsync"
)

func TestRadixConversionPreservesFormat4BlobsParentsAndStalePeers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	remote := bareRemote(t)
	source := newMachine(t, "format4-source", remote)
	for i := 0; i < 65; i++ {
		source.decide(t, fmt.Sprintf("old-%d", i), "Exact retained decision")
	}
	export, err := portable.EncodeMemoryExport(ctx, source.db, source.project)
	if err != nil || len(export.Envelopes) != 65 {
		t.Fatalf("fixture export: %d %v", len(export.Envelopes), err)
	}
	repo, err := source.syncer.repository(ctx)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{"README": []byte("opaque root remains"), "v4/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb/blocks/opaque": []byte("other lineage remains")}
	for _, object := range export.Envelopes {
		block, err := recordsync.EncodeBlock(testLineage, []portable.EncodedEnvelope{object})
		if err != nil {
			t.Fatal(err)
		}
		files["v4/"+strings.TrimPrefix(testLineage, "lineage:")+"/blocks/"+block.Hash+".block"] = block.Bytes
	}
	oldHead := fixtureCommitFiles(t, repo, files, nil, "format4 exact blocks")
	if _, err := repo.run(ctx, "push", "--quiet", "origin", "HEAD"); err != nil {
		t.Fatal(err)
	}
	converter, stale := newMachine(t, "converter", remote), newMachine(t, "stale-peer", remote)
	transport := &objectTransport{repo: repo}
	oldCommit, err := transport.readCommit(ctx, oldHead)
	if err != nil {
		t.Fatal(err)
	}
	oldLeaves, err := transport.conversionBlockLeaves(ctx, oldCommit.Tree, testLineage)
	if err != nil || len(oldLeaves) != 65 {
		t.Fatalf("old blocks: %d %v", len(oldLeaves), err)
	}
	result, err := converter.syncer.Convert(ctx)
	if err != nil || !result.Converted || result.ImportedRecords != 65 || result.Collisions+result.DeferredRecords+result.Forks != 0 {
		t.Fatalf("convert: %+v %v", result, err)
	}
	checkRepo, err := converter.syncer.repository(ctx)
	if err != nil {
		t.Fatal(err)
	}
	check := &objectTransport{repo: checkRepo}
	tree, err := check.headTree(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := check.validateBlockLayout(ctx, tree, testLineage); err != nil {
		t.Fatal(err)
	}
	if audit, err := AuditStorage(ctx, converter.syncer.Clone, remote, testLineage, Access{}); err != nil || audit.ConversionRequired || len(audit.TransportFormats) != 1 || audit.TransportFormats[0] != 5 || audit.UniqueEnvelopes != 65 {
		t.Fatalf("converted audit: %+v %v", audit, err)
	}
	nextLeaves, err := check.conversionBlockLeaves(ctx, tree, testLineage)
	if err != nil || len(nextLeaves) != 65 {
		t.Fatalf("new blocks: %d %v", len(nextLeaves), err)
	}
	for name, leaf := range oldLeaves {
		if nextLeaves[name] != leaf {
			t.Fatalf("rewrote exact block %s", name)
		}
	}
	if _, err := checkRepo.run(ctx, "merge-base", "--is-ancestor", oldHead, "HEAD"); err != nil {
		t.Fatalf("rewrote ancestry: %v", err)
	}
	for _, parts := range [][]string{{"v4", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}, {"README"}} {
		// README is a blob, so compare the root entry directly via Git.
		path := strings.Join(parts, "/")
		a, err := checkRepo.run(ctx, "rev-parse", oldHead+":"+path)
		if err != nil {
			t.Fatal(err)
		}
		b, err := checkRepo.run(ctx, "rev-parse", "HEAD:"+path)
		if err != nil || a != b {
			t.Fatalf("unrelated entry changed: %s %v", path, err)
		}
	}
	if _, err := stale.syncer.Sync(ctx); err == nil || !strings.Contains(err.Error(), "explicit conversion") {
		t.Fatalf("stale format4 peer adopted ordinary sync: %v", err)
	}
	if result, err := stale.syncer.Convert(ctx); err != nil || result.Collisions+result.DeferredRecords+result.Forks != 0 {
		t.Fatalf("stale conversion: %+v %v", result, err)
	}
	converter.decide(t, "new-after-conversion", "Bounded format5 arrival")
	converter.sync(t, OutcomeSynced)
	if result := stale.sync(t, OutcomeSynced); result.ImportedRecords != 1 {
		t.Fatalf("lost post-conversion arrival: %+v", result)
	}
	if result, err := converter.syncer.Convert(ctx); err != nil || result.ExportedRecords != 0 {
		t.Fatalf("conversion not idempotent: %+v %v", result, err)
	}
}

func TestRadixConversionRefusesCorruptFormat4BeforeRetirement(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	remote := bareRemote(t)
	source := newMachine(t, "corrupt-format4-source", remote)
	repo, err := source.syncer.repository(ctx)
	if err != nil {
		t.Fatal(err)
	}
	block := transportBlockFixture(t, testLineage, "corrupt-old-block")
	path := "v4/" + strings.TrimPrefix(testLineage, "lineage:") + "/blocks/" + block.Hash + ".block"
	old := fixtureCommitFiles(t, repo, map[string][]byte{path: []byte("invalid block bytes")}, nil, "corrupt format4 fixture")
	if _, err := repo.run(ctx, "push", "--quiet", "origin", "HEAD"); err != nil {
		t.Fatal(err)
	}
	converter := newMachine(t, "refuse-corrupt-format4", remote)
	if result, err := converter.syncer.Convert(ctx); err == nil || result.Converted {
		t.Fatalf("corrupt conversion succeeded: %+v %v", result, err)
	}
	check, err := converter.syncer.repository(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if head, err := check.run(ctx, "rev-parse", "HEAD"); err != nil || head != old {
		t.Fatalf("retired corrupt evidence: %s %v", head, err)
	}
	if heads, err := check.run(ctx, "ls-remote", "origin", "HEAD"); err != nil || !strings.HasPrefix(heads, old) {
		t.Fatalf("pushed corrupt conversion: %s %v", heads, err)
	}
}

func TestRadixConversionLogicalDeltaRefusesLostOldBlocks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	transport := objectTransportFixture(t, "sha1")
	block := transportBlockFixture(t, testLineage, "old-identity")
	leaf := objectBlob(t, transport, block.Hash+".block", string(block.Bytes), 40)
	before := transportBlockTree(t, transport, "v4", testLineage, []blockTreeEntry{leaf}, 40)
	other := transportBlockFixture(t, testLineage, "other-identity")
	after, _, err := transport.appendBlocks(ctx, "", testLineage, []recordsync.Block{other}, 40)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transport.blockDeltaForConversion(ctx, before, after, testLineage, true); err == nil {
		t.Fatal("accepted conversion that omitted the old block")
	}
	after, _, err = transport.appendBlocks(ctx, after, testLineage, []recordsync.Block{block}, 40)
	if err != nil {
		t.Fatal(err)
	}
	if delta, err := transport.blockDeltaForConversion(ctx, before, after, testLineage, true); err != nil || len(delta) != 1 || delta[0].Hash != other.Hash {
		t.Fatalf("incorrect relocation delta: %+v %v", delta, err)
	}
}

func TestRadixConversionResumesMixedNamespaces(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	remote := bareRemote(t)
	source := newMachine(t, "partial-conversion-source", remote)
	source.decide(t, "one", "First retained block")
	source.decide(t, "two", "Second retained block")
	export, err := portable.EncodeMemoryExport(ctx, source.db, source.project)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := source.syncer.repository(ctx)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	partition := strings.TrimPrefix(testLineage, "lineage:")
	for i, object := range export.Envelopes {
		block, err := recordsync.EncodeBlock(testLineage, []portable.EncodedEnvelope{object})
		if err != nil {
			t.Fatal(err)
		}
		files["v4/"+partition+"/blocks/"+block.Hash+".block"] = block.Bytes
		if i == 0 {
			files["v5/"+partition+"/blocks/"+block.Hash+".block"] = block.Bytes
		}
	}
	partial := fixtureCommitFiles(t, repo, files, nil, "interrupted conversion fixture")
	if _, err := repo.run(ctx, "push", "--quiet", "origin", "HEAD"); err != nil {
		t.Fatal(err)
	}
	converter := newMachine(t, "resume-partial-conversion", remote)
	if _, err := converter.syncer.Sync(ctx); err == nil {
		t.Fatal("ordinary sync adopted mixed formats")
	}
	result, err := converter.syncer.Convert(ctx)
	if err != nil || result.ImportedRecords != len(export.Envelopes) || result.Collisions+result.Forks+result.DeferredRecords != 0 {
		t.Fatalf("resume: %+v %v", result, err)
	}
	checkRepo, err := converter.syncer.repository(ctx)
	if err != nil {
		t.Fatal(err)
	}
	check := &objectTransport{repo: checkRepo}
	tree, err := check.headTree(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := check.validateBlockLayout(ctx, tree, testLineage); err != nil {
		t.Fatal(err)
	}
	if blocks, err := check.blockDelta(ctx, "", tree, testLineage); err != nil || len(blocks) != len(export.Envelopes) {
		t.Fatalf("lost/duplicated retained blocks: %d %v", len(blocks), err)
	}
	if _, err := checkRepo.run(ctx, "merge-base", "--is-ancestor", partial, "HEAD"); err != nil {
		t.Fatalf("lost interrupted history: %v", err)
	}
}
