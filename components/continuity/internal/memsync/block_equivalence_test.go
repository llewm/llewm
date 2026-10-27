package memsync

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/foldersync"
	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/recordsync"
)

// Exercise actual Git and folder delivery into a shared archive, then compare
// the experimental block importer with the reference importer at each step.
func TestBlockImportMatchesFullScanAcrossTransports(t *testing.T) {
	ctx := context.Background()
	remote := bareRemote(t)
	folderSource := localHistoryMachine(t, "block-folder-source")
	gitSource := newMachine(t, "block-git-source", remote)
	bridge := newMachine(t, "block-bridge", remote)
	folderArchive := archiveMachine(t, &folderSource)
	bridgeArchive := archiveMachine(t, &bridge)
	shared := t.TempDir()
	marker, err := foldersync.Create(shared)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := foldersync.Endpoint{Name: "block-test", Directory: shared, StoreID: marker.StoreID, LineageID: testLineage}
	full := localHistoryMachine(t, "block-reference")
	indexed := localHistoryMachine(t, "block-indexed")
	index := recordsync.Index{DB: indexed.db, ProjectID: indexed.project, Lineage: testLineage}
	for round := 0; round < 2; round++ {
		folderSource.decide(t, fmt.Sprintf("folder-%d", round), "Folder observation")
		gitSource.decide(t, fmt.Sprintf("git-%d", round), "Git observation")
		folderCycle(t, folderSource, folderArchive, endpoint)
		gitSource.sync(t, OutcomeSynced)
		bridge.sync(t, OutcomeSynced)
		folderCycle(t, bridge, bridgeArchive, endpoint)
		objects, err := bridgeArchive.Objects(ctx, "", true)
		if err != nil {
			t.Fatal(err)
		}
		var blocks []recordsync.Block
		for _, object := range objects {
			block, err := recordsync.EncodeBlock(testLineage, []portable.EncodedEnvelope{object})
			if err != nil {
				t.Fatal(err)
			}
			blocks = append(blocks, block)
		}
		want, wantErr := (recordsync.Importer{DB: full.db, ProjectID: full.project}).Import(ctx, objects)
		got, gotErr := index.ImportBlocks(ctx, blocks)
		if fmt.Sprint(wantErr) != fmt.Sprint(gotErr) || want != got {
			t.Fatalf("round %d: full=%+v %v indexed=%+v %v", round, want, wantErr, got, gotErr)
		}
		canonical := func(m machine) [][]byte {
			t.Helper()
			export, err := portable.EncodeMemoryExport(ctx, m.db, m.project)
			if err != nil {
				t.Fatal(err)
			}
			var result [][]byte
			for _, object := range export.Envelopes {
				result = append(result, object.Bytes)
			}
			sort.Slice(result, func(i, j int) bool { return bytes.Compare(result[i], result[j]) < 0 })
			return result
		}
		a, b := canonical(full), canonical(indexed)
		if len(a) != len(b) || len(a) < (round+1)*2 {
			t.Fatalf("round %d: exported counts differ or records missing: %d %d", round, len(a), len(b))
		}
		for i := range a {
			if !bytes.Equal(a[i], b[i]) {
				t.Fatalf("round %d: exported record %d differs", round, i)
			}
		}
	}
}
