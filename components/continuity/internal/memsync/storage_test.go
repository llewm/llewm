package memsync

import (
	"os"
	"testing"
)

func TestCloneStorageMigrationAPIInspectionAndFullAudit(t *testing.T) {
	t.Parallel()
	ctx, old, remote, access, before := cloneStorageFixture(t)
	inspection, err := InspectStorage(ctx, old.Root, remote, access)
	if err != nil || inspection.State != "working" || inspection.References != len(before.Refs) || inspection.RefsSHA256 != before.refsHash() {
		t.Fatalf("working inspection: %+v %v", inspection, err)
	}
	if _, err := os.Stat(inspection.Paths.Receipt); !os.IsNotExist(err) {
		t.Fatalf("inspection created a preparation: %v", err)
	}
	if _, err := PrepareStorageMigration(ctx, old.Root, remote, access, false); err == nil {
		t.Fatal("preparation omitted stopped-writer assertion")
	}
	if err := CompleteStorageMigration(ctx, old.Root, remote, access, false); err == nil {
		t.Fatal("completion omitted stopped-writer assertion")
	}
	prepared, err := PrepareStorageMigration(ctx, old.Root, remote, access, true)
	if err != nil || prepared.Paths != inspection.Paths || prepared.ReceiptSHA256 == "" {
		t.Fatalf("preparation: %+v %v", prepared, err)
	}
	if err := CompleteStorageMigration(ctx, old.Root, remote, access, true); err != nil {
		t.Fatal(err)
	}
	ready, err := InspectStorage(ctx, old.Root, remote, access)
	if err != nil || ready.State != "bare" || ready.RefsSHA256 != before.refsHash() || ready.ReceiptSHA256 != prepared.ReceiptSHA256 {
		t.Fatalf("completed inspection: %+v %v", ready, err)
	}
	audit, err := AuditStorage(ctx, old.Root, remote, testLineage, access)
	if err != nil || !audit.FullObjectCheck || audit.Blocks != 1 || audit.UniqueEnvelopes != 1 || audit.References != len(before.Refs) {
		t.Fatalf("full audit: %+v %v", audit, err)
	}
	// Damage an old reachable object after completion. A full audit must
	// inspect the real object store, independently of any tree cache.
	repo := access.git(old.Root)
	tree, err := repo.run(ctx, "rev-parse", "HEAD^{tree}")
	if err != nil {
		t.Fatal(err)
	}
	fixtureCorruptObject(t, repo, tree, "tree", []byte("invalid old tree"))
	if _, err := AuditStorage(ctx, old.Root, remote, testLineage, access); err == nil {
		t.Fatal("full audit concealed old object corruption")
	}
}
