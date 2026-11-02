package recordsync

import (
	"context"
	"fmt"
	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/database"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

func matchingIndexBlocks(t testing.TB, count int) (Block, Block) {
	t.Helper()
	var objects []portable.EncodedEnvelope
	for i := 0; i < count+1; i++ {
		e, err := portable.EncodeEnvelope(portable.EnvelopeSpec{LineageID: lineage, RecordType: "decision", RecordSchemaVersion: 1, RecordID: fmt.Sprintf("record-%04d", i), RecordRevision: 1, RecordedAt: time.Unix(1234567890, 0), Authority: "caller_asserted", Payload: []byte(`{}`), Refs: []portable.Reference{{Kind: "decision", ID: "target"}}})
		if err != nil {
			t.Fatal(err)
		}
		objects = append(objects, e)
	}
	full, err := EncodeBlock(lineage, objects)
	if err != nil {
		t.Fatal(err)
	}
	subset, err := EncodeBlock(lineage, objects[:count])
	if err != nil {
		t.Fatal(err)
	}
	return full, subset
}

func TestMatchingIndexBatchesPreserveSequentialStateAndTriggerEffects(t *testing.T) {
	full, subset := matchingIndexBlocks(t, 65)
	for _, mode := range []string{"known", "body", "missing_ref", "extra_ref", "membership_hook", "generation_temp_hook", "repair_hook", "rollback"} {
		t.Run(mode, func(t *testing.T) {
			var snapshots [][]string
			var journals [][]string
			for _, sequential := range []bool{true, false} {
				ctx, db, project := indexedTestProject(t, "match-"+mode)
				index := Index{DB: db, ProjectID: project, Lineage: lineage}
				if err := index.AddBlock(ctx, full); err != nil {
					t.Fatal(err)
				}
				first, last := subset.Objects[0].Envelope.EnvelopeSHA256, subset.Objects[len(subset.Objects)-1].Envelope.EnvelopeSHA256
				conn, err := db.Conn(ctx)
				if err != nil {
					t.Fatal(err)
				}
				tx, err := conn.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				switch mode {
				case "body", "repair_hook":
					_, err = tx.ExecContext(ctx, "UPDATE history_index_envelopes SET record_id='damaged' WHERE project_id=? AND envelope_hash=?", project, first)
				case "missing_ref":
					_, err = tx.ExecContext(ctx, "DELETE FROM history_index_refs WHERE project_id=? AND envelope_hash=?", project, last)
				case "extra_ref":
					_, err = tx.ExecContext(ctx, "INSERT INTO history_index_refs VALUES(?,?,?,?)", project, last, "decision", "extra")
				}
				if err != nil {
					t.Fatal(err)
				}
				switch mode {
				case "membership_hook":
					_, err = tx.ExecContext(ctx, `CREATE TRIGGER fixture_membership_hook AFTER INSERT ON history_index_block_members WHEN new.block_hash='`+subset.Hash+`' AND new.envelope_hash='`+first+`' BEGIN UPDATE history_index_envelopes SET record_id='hooked' WHERE envelope_hash='`+last+`'; END`)
				case "generation_temp_hook":
					_, err = tx.ExecContext(ctx, `CREATE TEMP TRIGGER fixture_generation_hook AFTER UPDATE ON history_index_inventory_generation WHEN (SELECT count(*) FROM history_index_block_members WHERE block_hash='`+subset.Hash+`')=1 BEGIN DELETE FROM history_index_refs WHERE envelope_hash='`+last+`'; END`)
				case "repair_hook":
					_, err = tx.ExecContext(ctx, `CREATE TRIGGER fixture_repair_hook AFTER UPDATE ON history_index_envelopes WHEN new.envelope_hash='`+first+`' BEGIN DELETE FROM history_index_refs WHERE envelope_hash='`+last+`'; END`)
				case "rollback":
					_, err = tx.ExecContext(ctx, `ALTER TABLE history_index_block_members ADD COLUMN fixture_check INTEGER CHECK(block_hash<>'`+subset.Hash+`' OR envelope_hash<>'`+subset.Objects[40].Envelope.EnvelopeSHA256+`')`)
				}
				if err != nil {
					t.Fatal(err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
				var cursor int64
				if err := db.QueryRowContext(ctx, "SELECT COALESCE(max(sequence),0) FROM history_sync_changes WHERE project_id=?", project).Scan(&cursor); err != nil {
					t.Fatal(err)
				}
				measured, work := syncwork.WithWorkMetrics(ctx)
				tx, err = conn.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := tx.ExecContext(ctx, "INSERT INTO history_index_blocks VALUES(?,?,?)", project, subset.Hash, IndexVersion); err != nil {
					t.Fatal(err)
				}
				if sequential {
					err = index.insertBlockObjectsSequential(measured, tx, subset)
				} else {
					err = index.insertBlockObjects(measured, tx, subset)
				}
				if mode == "rollback" {
					if err == nil {
						t.Fatal("fixture failure ignored")
					}
					tx.Rollback()
				} else {
					if err != nil {
						t.Fatal(err)
					}
					if err := tx.Commit(); err != nil {
						t.Fatal(err)
					}
				}
				conn.Close()
				if !sequential {
					want := int64(0)
					if mode == "known" {
						want = 3
					}
					if mode == "rollback" {
						want = 2
					}
					if got := work.Counts()["inventory_membership_batches"]; got != want {
						t.Fatalf("batch attempts%d want%d: %v", got, want, work.Counts())
					}
				}
				var snapshot []string
				for _, query := range []string{`SELECT record_type||':'||record_id||':'||record_revision||':'||envelope_json FROM history_index_envelopes WHERE project_id=? ORDER BY envelope_hash`, `SELECT block_hash||':'||envelope_hash FROM history_index_block_members WHERE project_id=? ORDER BY block_hash,envelope_hash`, `SELECT envelope_hash||':'||ref_kind||':'||ref_id FROM history_index_refs WHERE project_id=? ORDER BY envelope_hash,ref_kind,ref_id`} {
					rows, err := db.QueryContext(ctx, query, project)
					if err != nil {
						t.Fatal(err)
					}
					for rows.Next() {
						var s string
						if err := rows.Scan(&s); err != nil {
							t.Fatal(err)
						}
						snapshot = append(snapshot, s)
					}
					if err := rows.Err(); err != nil {
						t.Fatal(err)
					}
					rows.Close()
				}
				snapshots = append(snapshots, snapshot)
				rows, err := db.QueryContext(ctx, "SELECT table_name,row_key,operation FROM history_sync_changes WHERE project_id=? AND sequence>? ORDER BY sequence", project, cursor)
				if err != nil {
					t.Fatal(err)
				}
				var journal []string
				for rows.Next() {
					var table, key, op string
					if err := rows.Scan(&table, &key, &op); err != nil {
						t.Fatal(err)
					}
					journal = append(journal, table+":"+strings.ReplaceAll(key, project, "[PROJECT]")+":"+op)
				}
				if err := rows.Err(); err != nil {
					t.Fatal(err)
				}
				rows.Close()
				journals = append(journals, journal)
				if mode != "rollback" {
					if _, err := index.blockObjects(ctx, []Block{subset}); err != nil {
						t.Fatalf("unrepaired block: %v", err)
					}
				}
			}
			if !reflect.DeepEqual(snapshots[0], snapshots[1]) || !reflect.DeepEqual(journals[0], journals[1]) {
				t.Fatal("batch differs from sequential state or journal order")
			}
		})
	}
}

func TestMatchingIndexReadOnlyShortcut(t *testing.T) {
	ctx, db, project := indexedTestProject(t, "matching-read-only")
	full, _ := matchingIndexBlocks(t, 65)
	index := Index{DB: db, ProjectID: project, Lineage: lineage}
	if err := index.AddBlock(ctx, full); err != nil {
		t.Fatal(err)
	}
	measured, work := syncwork.WithWorkMetrics(ctx)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var blockers []string
	for _, table := range []string{"history_index_envelopes", "history_index_block_members", "history_index_refs"} {
		for _, event := range []string{"INSERT", "UPDATE", "DELETE"} {
			name := "fixture_no_write_" + table + "_" + event
			blockers = append(blockers, name)
			if _, err := tx.ExecContext(ctx, "CREATE TRIGGER "+name+" BEFORE "+event+" ON "+table+" BEGIN SELECT RAISE(ABORT,'unexpected write'); END"); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := index.insertBlockObjects(measured, tx, full); err != nil {
		t.Fatal(err)
	}
	if got := work.Counts(); got["inventory_match_shortcuts"] != 1 || got["inventory_match_queries"] != 6 || got["inventory_match_records"] != 66 {
		t.Fatalf("not bounded match reads: %v", got)
	}
	for _, name := range blockers {
		if _, err := tx.ExecContext(ctx, "DROP TRIGGER "+name); err != nil {
			t.Fatal(err)
		}
	}
	// Current-transaction damage cannot be hidden by an earlier successful read.
	if _, err := tx.ExecContext(ctx, "DELETE FROM history_index_refs WHERE project_id=?", project); err != nil {
		t.Fatal(err)
	}
	if _, matched, err := index.blockObjectsMatchWithin(context.Background(), tx, full); err != nil || matched {
		t.Fatalf("current damage matched: %t %v", matched, err)
	}
}

func BenchmarkKnownIndexMemberships(b *testing.B) {
	ctx := context.Background()
	db, err := database.Initialize(ctx, filepath.Join(b.TempDir(), "index.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	project, err := (activity.Store{DB: db}).EnsureProject(ctx, b.TempDir(), "benchmark")
	if err != nil {
		b.Fatal(err)
	}
	full, subset := matchingIndexBlocks(b, 256)
	index := Index{DB: db, ProjectID: project, Lineage: lineage}
	if err := index.AddBlock(ctx, full); err != nil {
		b.Fatal(err)
	}
	for _, mode := range []string{"sequential", "batched"} {
		b.Run(mode, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					b.Fatal(err)
				}
				if _, err := tx.ExecContext(ctx, "INSERT INTO history_index_blocks VALUES(?,?,?)", project, subset.Hash, IndexVersion); err != nil {
					b.Fatal(err)
				}
				if mode == "sequential" {
					err = index.insertBlockObjectsSequential(ctx, tx, subset)
				} else {
					err = index.insertBlockObjects(ctx, tx, subset)
				}
				if err != nil {
					tx.Rollback()
					b.Fatal(err)
				}
				if err := tx.Rollback(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
