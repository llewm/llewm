package recordsync

import (
	"bytes"
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
		`UPDATE history_index_records SET envelope_json=replace(envelope_json,'"payload":{}','"payload":{"changed":true}')`,
		`DELETE FROM history_index_refs`,
		`INSERT INTO history_index_refs(project_id,block_hash,envelope_hash,ref_kind,ref_id) SELECT project_id,block_hash,envelope_hash,'decision','extra' FROM history_index_records`,
	} {
		if err := index.Clear(ctx); err != nil {
			t.Fatal(err)
		}
		if err := index.AddBlock(ctx, block); err != nil {
			t.Fatal(err)
		}
		objects, err := index.blockObjects(ctx, []Block{block, block})
		if err != nil || len(objects) != 1 || !bytes.Equal(objects[0].Bytes, object.Bytes) || len(objects[0].Envelope.Refs) != 1 || objects[0].Envelope.Refs[0] != object.Envelope.Refs[0] || !bytes.Equal(objects[0].Envelope.Payload, object.Envelope.Payload) {
			t.Fatalf("valid overlapping inventory differs: %+v %v", objects, err)
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
	if _, err := index.ImportBlocks(ctx, []Block{block}); errors.Is(err, ErrIndexRebuild) {
		t.Fatalf("validated block did not repair its inventory: %v", err)
	}
	if _, err := index.blockObjects(ctx, []Block{block}); err != nil {
		t.Fatalf("inventory remains damaged: %v", err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM history_index_publications WHERE project_id=?`, project).Scan(&count); err != nil || count != 1 {
		t.Fatalf("publication membership changed: %d %v", count, err)
	}
}

func TestIndexedReadRejectsDamagedInventory(t *testing.T) {
	ctx, db, project := indexedTestProject(t, "indexed-read")
	block, err := EncodeBlock(lineage, []portable.EncodedEnvelope{fixture(t)})
	if err != nil {
		t.Fatal(err)
	}
	root, err := OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, err := PublishBlock(root, lineage, block); err != nil {
		t.Fatal(err)
	}
	path, err := blockPath(lineage, block.Hash)
	if err != nil {
		t.Fatal(err)
	}
	index := Index{DB: db, ProjectID: project, Lineage: lineage}
	for i := 0; i < 2; i++ {
		read, err := index.ReadBlock(ctx, root, path)
		if err != nil || len(read.Objects) != 1 || read.Hash != block.Hash {
			t.Fatalf("indexed read %d: %+v %v", i, read, err)
		}
	}
	if _, err := db.ExecContext(ctx, `UPDATE history_index_records SET record_id='wrong' WHERE project_id=?`, project); err != nil {
		t.Fatal(err)
	}
	if repaired, err := index.ReadBlock(ctx, root, path); err != nil || repaired.Hash != block.Hash {
		t.Fatalf("damaged inventory was not repaired: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO history_index_blocks(project_id,block_hash,index_version) VALUES(?,?,0)`, project, Hash("unavailable")); err != nil {
		t.Fatal(err)
	}
	if _, err := index.ScanBlocks(ctx, root); err != nil {
		t.Fatalf("unrelated stale block prevented this scan: %v", err)
	}
	if _, err := index.Versions(ctx, "decision", "id", 1); !errors.Is(err, ErrIndexRebuild) {
		t.Fatalf("global lookup hid an unavailable stale block: %v", err)
	}
}

func TestRepairBlockPreservesOtherEndpointMembership(t *testing.T) {
	ctx, db, project := indexedTestProject(t, "repair-block")
	block, err := EncodeBlock(lineage, []portable.EncodedEnvelope{fixture(t)})
	if err != nil {
		t.Fatal(err)
	}
	index := Index{DB: db, ProjectID: project, Lineage: lineage}
	if err := index.Rebuild(ctx, map[string][]Block{"folder:a": {block}, "git:b": {block}}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE history_index_records SET record_id='wrong' WHERE project_id=?`, project); err != nil {
		t.Fatal(err)
	}
	bad := block
	bad.Hash = Hash("wrong")
	if err := index.RepairBlock(ctx, bad); err == nil {
		t.Fatal("invalid repair block accepted")
	}
	if err := index.RepairBlock(ctx, block); err != nil {
		t.Fatal(err)
	}
	if _, err := index.blockObjects(ctx, []Block{block}); err != nil {
		t.Fatalf("repaired block invalid: %v", err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM history_index_publications WHERE project_id=? AND block_hash=?`, project, block.Hash).Scan(&count); err != nil || count != 2 {
		t.Fatalf("lost endpoint membership: %d %v", count, err)
	}
}

func TestRepairStoredIsAtomicAndPreservesMembership(t *testing.T) {
	ctx, db, project := indexedTestProject(t, "repair-stored")
	first, err := EncodeBlock(lineage, []portable.EncodedEnvelope{fixture(t)})
	if err != nil {
		t.Fatal(err)
	}
	secondObject, err := portable.EncodeEnvelope(portable.EnvelopeSpec{LineageID: lineage, RecordType: "decision", RecordSchemaVersion: 1, RecordID: "second", RecordedAt: time.Now().UTC(), Authority: "caller_asserted", Payload: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	second, err := EncodeBlock(lineage, []portable.EncodedEnvelope{secondObject})
	if err != nil {
		t.Fatal(err)
	}
	index := Index{DB: db, ProjectID: project, Lineage: lineage}
	if err := index.Rebuild(ctx, map[string][]Block{"folder:a": {first}, "git:b": {second}}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE history_index_blocks SET index_version=0 WHERE project_id=?`, project); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE history_index_records SET envelope_json='{}' WHERE project_id=? AND block_hash=?`, project, second.Hash); err != nil {
		t.Fatal(err)
	}
	if err := index.RepairStored(ctx); !errors.Is(err, ErrIndexRebuild) {
		t.Fatalf("incomplete inventory accepted: %v", err)
	}
	var stale int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM history_index_blocks WHERE project_id=? AND index_version=0`, project).Scan(&stale); err != nil || stale != 2 {
		t.Fatalf("partial repair committed: %d %v", stale, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE history_index_records SET envelope_json=? WHERE project_id=? AND block_hash=?`, string(secondObject.Bytes), project, second.Hash); err != nil {
		t.Fatal(err)
	}
	if err := index.RepairStored(ctx); err != nil {
		t.Fatal(err)
	}
	var members int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM history_index_publications WHERE project_id=?`, project).Scan(&members); err != nil || members != 2 {
		t.Fatalf("repair changed endpoint memberships: %d %v", members, err)
	}
}

func TestRebuildRestoresEndpointMembershipAtomically(t *testing.T) {
	ctx, db, project := indexedTestProject(t, "rebuild-endpoints")
	object, err := portable.EncodeEnvelope(portable.EnvelopeSpec{LineageID: lineage, RecordType: "decision", RecordSchemaVersion: 1, RecordID: "record", RecordRevision: 1, RecordedAt: time.Now().UTC(), Authority: "caller_asserted", Payload: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	block, err := EncodeBlock(lineage, []portable.EncodedEnvelope{object})
	if err != nil {
		t.Fatal(err)
	}
	index := Index{DB: db, ProjectID: project, Lineage: lineage}
	inventories := map[string][]Block{"folder:a": {block}, "git:b": {block}}
	if err := index.Rebuild(ctx, inventories); err != nil {
		t.Fatal(err)
	}
	check := func() {
		t.Helper()
		var blocks, members int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM history_index_blocks WHERE project_id=?`, project).Scan(&blocks); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM history_index_publications WHERE project_id=?`, project).Scan(&members); err != nil {
			t.Fatal(err)
		}
		if blocks != 1 || members != 2 {
			t.Fatalf("inventory changed: %d blocks, %d memberships", blocks, members)
		}
	}
	check()
	// A whole endpoint remains required; staged intent can be reconstructed.
	if err := index.Rebuild(ctx, map[string][]Block{"folder:a": {block}}); !errors.Is(err, ErrIndexRebuild) {
		t.Fatalf("missing endpoint accepted: %v", err)
	}
	check()
	if err := index.Rebuild(ctx, map[string][]Block{"folder:a": {}, "git:b": {}}); err != nil {
		t.Fatalf("valid staged block not preserved: %v", err)
	}
	check()
	if _, err := db.ExecContext(ctx, `UPDATE history_index_records SET envelope_json='{}' WHERE project_id=?`, project); err != nil {
		t.Fatal(err)
	}
	if err := index.Rebuild(ctx, map[string][]Block{"folder:a": {}, "git:b": {}}); !errors.Is(err, ErrIndexRebuild) {
		t.Fatalf("damaged staged block accepted: %v", err)
	}
	check()
	bad := block
	bad.Hash = Hash("wrong")
	if err := index.Rebuild(ctx, map[string][]Block{"folder:a": {bad}}); err == nil {
		t.Fatal("accepted invalid block")
	}
	check()
	if _, err := db.ExecContext(ctx, `CREATE TRIGGER fail_rebuild BEFORE INSERT ON history_index_publications BEGIN SELECT RAISE(ABORT,'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := index.Rebuild(ctx, inventories); err == nil {
		t.Fatal("ignored injected failure")
	}
	check()
}
