package recordsync

import (
	"os"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/database"
)

func TestArchiveStorageMigrationAPIRequiresStoppedWritersAndAuditsDurableBytes(t *testing.T) {
	ctx, store, p := archiveCutoverFixture(t)
	archive := Archive{DB: store.DB, ProjectID: store.ProjectID, Lineage: store.Lineage}
	paths := ArchiveStoragePaths{Source: p.Source, Retained: p.Retained, DatabaseBackup: p.DatabaseBackup}
	legacy, err := archive.InspectLegacyStorage(ctx, p.Source)
	if err != nil || legacy.State != "legacy" || legacy.Envelopes != 1 || legacy.Origins != 1 {
		t.Fatalf("legacy inspection: %+v %v", legacy, err)
	}
	if err := archive.CheckStorage(ctx, p.Source); err == nil {
		t.Fatal("read-only route check initialized or accepted legacy storage")
	}
	if _, err := archive.PrepareStorageMigration(ctx, paths, false); err == nil {
		t.Fatal("preparation omitted stopped-writer assertion")
	}
	if _, err := archive.CompleteStorageMigration(ctx, paths, false); err == nil {
		t.Fatal("completion omitted stopped-writer assertion")
	}
	if _, err := os.Stat(p.DatabaseBackup); !os.IsNotExist(err) {
		t.Fatalf("rejected operation created backup: %v", err)
	}
	prepared, err := archive.PrepareStorageMigration(ctx, paths, true)
	if err != nil || prepared.State != "staging" || prepared.Envelopes != 1 || prepared.Origins != 1 || prepared.ReceiptSHA256 == "" {
		t.Fatalf("prepare: %+v %v", prepared, err)
	}
	ready, err := archive.CompleteStorageMigration(ctx, paths, true)
	if err != nil || ready.State != "ready" || ready.EnvelopesSHA256 != prepared.EnvelopesSHA256 || ready.OriginsSHA256 != prepared.OriginsSHA256 || ready.ReceiptSHA256 != prepared.ReceiptSHA256 {
		t.Fatalf("complete: %+v %v", ready, err)
	}
	var dbPath string
	if err := store.DB.QueryRowContext(ctx, `SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&dbPath); err != nil {
		t.Fatal(err)
	}
	ro, err := database.OpenReadOnly(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	reader := Archive{DB: ro, ProjectID: store.ProjectID, Lineage: store.Lineage}
	if err := reader.CheckStorage(ctx, p.Source); err != nil {
		t.Fatalf("ready route required write capability: %v", err)
	}
	if audit, err := reader.AuditStorage(ctx); err != nil || audit != ready {
		t.Fatalf("read-only audit: %+v %v", audit, err)
	}
	objects, err := archive.Objects(ctx, "", true)
	if err != nil {
		t.Fatal(err)
	}
	block, err := EncodeBlock(store.Lineage, objects)
	if err != nil {
		t.Fatal(err)
	}
	index := Index{DB: store.DB, ProjectID: store.ProjectID, Lineage: store.Lineage}
	if err := index.AddBlock(ctx, block); err != nil {
		t.Fatal(err)
	}
	if err := index.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	audit, err := archive.AuditStorage(ctx)
	if err != nil || audit != ready {
		t.Fatalf("derived inventory affected durable audit: %+v %v", audit, err)
	}
	foreign := archive
	foreign.Lineage = "lineage:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if _, err := foreign.AuditStorage(ctx); err == nil {
		t.Fatal("audit accepted another lineage")
	}
}
