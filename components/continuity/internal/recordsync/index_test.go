package recordsync

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/portable"
)

func TestBlockIndexVersionsDependentsAndRebuild(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := database.Initialize(ctx, filepath.Join(root, "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	project, err := (activity.Store{DB: db}).EnsureProject(ctx, root, "index-test")
	if err != nil {
		t.Fatal(err)
	}
	makeEnvelope := func(id, payload string, refs []portable.Reference) portable.EncodedEnvelope {
		t.Helper()
		e, err := portable.EncodeEnvelope(portable.EnvelopeSpec{LineageID: lineage, RecordType: "decision", RecordSchemaVersion: 1, RecordID: id, RecordRevision: 1, RecordedAt: time.Now().UTC(), Authority: "caller_asserted", Payload: []byte(payload), Refs: refs})
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	a := makeEnvelope("target", `{"value":1}`, nil)
	b := makeEnvelope("target", `{"value":2}`, nil)
	dependent := makeEnvelope("dependent", `{}`, []portable.Reference{{Kind: "decision", ID: "target"}})
	first, err := EncodeBlock(lineage, []portable.EncodedEnvelope{a, dependent})
	if err != nil {
		t.Fatal(err)
	}
	second, err := EncodeBlock(lineage, []portable.EncodedEnvelope{b})
	if err != nil {
		t.Fatal(err)
	}
	index := Index{DB: db, ProjectID: project, Lineage: lineage}
	if _, err := db.ExecContext(ctx, `CREATE TRIGGER fail_index_record BEFORE INSERT ON history_index_records WHEN NEW.record_id='dependent' BEGIN SELECT RAISE(ABORT,'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := index.AddBlock(ctx, first); err == nil {
		t.Fatal("injected index failure was ignored")
	}
	var blocks int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM history_index_blocks WHERE project_id=?`, project).Scan(&blocks); err != nil || blocks != 0 {
		t.Fatalf("partial block index survived rollback: %d %v", blocks, err)
	}
	if _, err := db.ExecContext(ctx, `DROP TRIGGER fail_index_record`); err != nil {
		t.Fatal(err)
	}
	for _, block := range []Block{first, second, first} {
		if err := index.AddBlock(ctx, block); err != nil {
			t.Fatal(err)
		}
	}
	versions, err := index.Versions(ctx, "decision", "target", 1)
	if err != nil || len(versions) != 2 {
		t.Fatalf("missing competing versions: %d %v", len(versions), err)
	}
	dependents, err := index.Dependents(ctx, "decision", "target")
	if err != nil || len(dependents) != 1 || dependents[0].Envelope.RecordID != "dependent" {
		t.Fatalf("missing dependent: %+v %v", dependents, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE history_index_blocks SET index_version=0 WHERE project_id=?`, project); err != nil {
		t.Fatal(err)
	}
	if _, err := index.Versions(ctx, "decision", "target", 1); err == nil {
		t.Fatal("accepted stale index version")
	}
	if err := index.AddBlock(ctx, first); err == nil {
		t.Fatal("mixed index versions")
	}
	if err := index.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	if err := index.AddBlock(ctx, first); err != nil {
		t.Fatal(err)
	}
	versions, err = index.Versions(ctx, "decision", "target", 1)
	if err != nil || len(versions) != 1 {
		t.Fatalf("rebuild failed: %d %v", len(versions), err)
	}
}

func TestBlockInventoryRejectsDamagedIdentityAndReferences(t *testing.T) {
	ctx, db, project := indexedTestProject(t, "damaged-index")
	object, err := portable.EncodeEnvelope(portable.EnvelopeSpec{LineageID: lineage, RecordType: "decision", RecordSchemaVersion: 1, RecordID: "dependent", RecordRevision: 1, RecordedAt: time.Now().UTC(), Authority: "caller_asserted", Payload: []byte(`{}`), Refs: []portable.Reference{{Kind: "decision", ID: "target"}}})
	if err != nil {
		t.Fatal(err)
	}
	block, err := EncodeBlock(lineage, []portable.EncodedEnvelope{object})
	if err != nil {
		t.Fatal(err)
	}
	index := Index{DB: db, ProjectID: project, Lineage: lineage}
	for _, mutation := range []string{
		`UPDATE history_index_records SET record_id='wrong'`,
		`DELETE FROM history_index_refs`,
		`INSERT INTO history_index_refs(project_id,block_hash,envelope_hash,ref_kind,ref_id) SELECT project_id,block_hash,envelope_hash,'decision','extra' FROM history_index_records`,
	} {
		if err := index.Clear(ctx); err != nil {
			t.Fatal(err)
		}
		if err := index.AddBlock(ctx, block); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, mutation); err != nil {
			t.Fatal(err)
		}
		if _, err := index.blockObjects(ctx, []Block{block}); !errors.Is(err, ErrIndexRebuild) {
			t.Fatalf("mutation %q was not detected: %v", mutation, err)
		}
	}
}

func TestImportRebuildRequestPreservesPublicationMembership(t *testing.T) {
	ctx, db, project := indexedTestProject(t, "rebuild-publications")
	object, err := portable.EncodeEnvelope(portable.EnvelopeSpec{LineageID: lineage, RecordType: "decision", RecordSchemaVersion: 1, RecordID: "record", RecordRevision: 1, RecordedAt: time.Now().UTC(), Authority: "caller_asserted", Payload: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	block, err := EncodeBlock(lineage, []portable.EncodedEnvelope{object})
	if err != nil {
		t.Fatal(err)
	}
	index := Index{DB: db, ProjectID: project, Lineage: lineage}
	if err := index.AddBlock(ctx, block); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO history_index_publications(project_id,endpoint_hash,block_hash) VALUES(?,?,?)`, project, Hash("other endpoint"), block.Hash); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE history_index_records SET record_id='corrupt' WHERE project_id=?`, project); err != nil {
		t.Fatal(err)
	}
	if _, err := index.ImportBlocks(ctx, []Block{block}); !errors.Is(err, ErrIndexRebuild) {
		t.Fatalf("expected rebuild request: %v", err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM history_index_publications WHERE project_id=?`, project).Scan(&count); err != nil || count != 1 {
		t.Fatalf("publication membership changed: %d %v", count, err)
	}
}
