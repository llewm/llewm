package portable

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/decision"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
	"github.com/llewm/llewm/components/continuity/internal/topic"
	"github.com/llewm/llewm/components/continuity/internal/usage"
	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
)

func TestAtomicRevisionSnapshotMatchesOrdinaryExport(t *testing.T) {
	ctx, db, project := invocationScopeProject(t)
	if _, err := (topic.Service{DB: db}).Record(ctx, topic.Topic{ProjectID: project, ID: "revisioned", Title: "Revisioned", Creator: "test"}); err != nil {
		t.Fatal(err)
	}
	actor := scopeActor(t, ctx, db, project, "incoming-only", "")
	incoming, err := EncodeEnvelope(actor)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	ctx, err = WithAtomicImportSnapshot(ctx, tx, project)
	if err != nil {
		t.Fatal(err)
	}
	want, err := LocalRevisionEnvelopesTx(ctx, tx, project) // ordinary fallback
	if err != nil || len(want) == 0 {
		t.Fatalf("ordinary revision snapshot: %d %v", len(want), err)
	}
	ctx, metrics := syncwork.WithWorkMetrics(ctx)
	curated, err := LocalEnvelopesForImport(ctx, tx, project, []EncodedEnvelope{incoming})
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := curated[RecordKey("actor_invocation", "incoming-only", 0)]; !exists {
		t.Fatal("fixture lacks the extra incoming actor")
	}
	got, err := LocalRevisionEnvelopesTx(ctx, tx, project)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("revision view changed with incoming actor: %v", err)
	}
	if metrics.Counts()["atomic_import_revision_snapshot_reuses"] != 1 || metrics.Counts()["operational_full_loads"] != 1 {
		t.Fatal(metrics.Counts())
	}
	for key, object := range got {
		object.Bytes[0] = '!'
		delete(got, key)
	}
	got, err = LocalRevisionEnvelopesTx(ctx, tx, project)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("fork reader changed private cache: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE projects SET name='changed' WHERE id=?`, project); err != nil {
		t.Fatal(err)
	}
	got, err = LocalRevisionEnvelopesTx(ctx, tx, project)
	if err != nil || !reflect.DeepEqual(got, want) || metrics.Counts()["operational_full_loads"] != 2 {
		t.Fatalf("changed guard did not use ordinary reader: %v %+v", err, metrics.Counts())
	}
}

func TestAtomicRevisionSnapshotDoesNotHideChangedBoundActor(t *testing.T) {
	ctx, _, tx, project := atomicSnapshotFixture(t)
	if _, err := LocalEnvelopesForImport(ctx, tx, project, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE actor_invocations SET received_at='malformed' WHERE id='bound'`); err != nil {
		t.Fatal(err)
	}
	if _, err := LocalRevisionEnvelopesTx(ctx, tx, project); err == nil {
		t.Fatal("revision lookup hid invalid ordinary export dependency after a write")
	}
}

func atomicSnapshotFixture(t *testing.T) (context.Context, *sql.DB, *sql.Tx, string) {
	t.Helper()
	ctx, db, project := invocationScopeProject(t)
	if _, err := (decision.Service{DB: db}).Append(ctx, decision.Record{ProjectID: project, ID: "atomic", Title: "Original", Rationale: "Observation", Disposition: "accepted", Actor: "owner", ActorType: "human", Authority: "caller_asserted"}); err != nil {
		t.Fatal(err)
	}
	scopeActor(t, ctx, db, project, "bound", "")
	if _, err := (worknarrative.Service{DB: db}).Append(ctx, worknarrative.Record{ID: "bound-narrative", ProjectID: project, Title: "Bound actor", Trigger: "manual", Creator: "test", CreatorInvocationID: "bound", Claims: []worknarrative.Claim{{Kind: "outcome", Text: "Observation", Authority: "caller_asserted"}}}); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tx.Rollback() })
	ctx, err = WithAtomicImportSnapshot(ctx, tx, project)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, db, tx, project
}
func TestAtomicSnapshotReuseOwnsBuffersAndTransaction(t *testing.T) {
	ctx, db, tx, project := atomicSnapshotFixture(t)
	ctx, metrics := syncwork.WithWorkMetrics(ctx)
	first, err := LocalEnvelopesForImport(ctx, tx, project, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := cloneImportSnapshot(first)
	for key, value := range first {
		value.Bytes[0] = '!'
		value.Envelope.Payload[0] = '!'
		if len(value.Envelope.Index) > 0 {
			value.Envelope.Index[0] = '!'
		}
		delete(first, key)
	}
	second, err := LocalEnvelopesForImport(ctx, tx, project, nil)
	if err != nil || !reflect.DeepEqual(second, want) {
		t.Fatalf("cached buffers changed: %v", err)
	}
	if metrics.Counts()["atomic_import_snapshot_reuses"] != 1 || metrics.Counts()["operational_full_loads"] != 1 {
		t.Fatal(metrics.Counts())
	}
	other, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Rollback()
	cacheTime := atomicSnapshotFor(ctx, tx, project)
	savedTime := cacheTime.evaluatedAt
	cacheTime.evaluatedAt = savedTime.Add(-24 * time.Hour)
	if importSnapshotTime(ctx, other, project).Equal(cacheTime.evaluatedAt) {
		t.Fatal("evaluation time crossed transactions")
	}
	cacheTime.evaluatedAt = savedTime
	if _, err := LocalEnvelopesForImport(ctx, other, project, nil); err != nil {
		t.Fatal(err)
	}
	if metrics.Counts()["operational_full_loads"] != 2 {
		t.Fatal("cached state crossed transactions")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	cache := atomicSnapshotFor(ctx, tx, project)
	if _, ok := cache.load(ctx, nil); ok {
		t.Fatal("finalized transaction reused snapshot")
	}
}
func TestAtomicSnapshotInvalidatesWritesAndSchemas(t *testing.T) {
	for _, mode := range []string{"write", "trigger", "main_schema", "temp_schema", "savepoint"} {
		t.Run(mode, func(t *testing.T) {
			ctx, _, tx, project := atomicSnapshotFixture(t)
			if mode == "trigger" {
				for _, sql := range []string{`CREATE TABLE fixture_atomic_trigger(v INTEGER)`, `CREATE TRIGGER fixture_atomic_sideeffect AFTER INSERT ON fixture_atomic_trigger BEGIN UPDATE actor_invocations SET client='Changed through trigger'; END`} {
					if _, err := tx.ExecContext(ctx, sql); err != nil {
						t.Fatal(err)
					}
				}
			}
			ctx, metrics := syncwork.WithWorkMetrics(ctx)
			before, err := LocalEnvelopesForImport(ctx, tx, project, nil)
			if err != nil {
				t.Fatal(err)
			}
			var statements []string
			switch mode {
			case "write":
				statements = []string{`UPDATE actor_invocations SET client='Changed directly'`}
			case "trigger":
				statements = []string{`INSERT INTO fixture_atomic_trigger VALUES(1)`}
			case "main_schema":
				statements = []string{`CREATE TABLE fixture_atomic_schema(v INTEGER)`}
			case "temp_schema":
				statements = []string{`CREATE TEMP TABLE fixture_atomic_schema(v INTEGER)`}
			case "savepoint":
				statements = []string{`SAVEPOINT fixture`, `UPDATE actor_invocations SET client='Rolled back'`, `ROLLBACK TO fixture`, `RELEASE fixture`}
			}
			for _, sql := range statements {
				if _, err := tx.ExecContext(ctx, sql); err != nil {
					t.Fatal(err)
				}
			}
			after, err := LocalEnvelopesForImport(ctx, tx, project, nil)
			if err != nil {
				t.Fatal(err)
			}
			if metrics.Counts()["atomic_import_snapshot_invalidations"] != 1 || metrics.Counts()["operational_full_loads"] != 2 {
				t.Fatal(metrics.Counts())
			}
			key := RecordKey("actor_invocation", "bound", 0)
			changed := !bytes.Equal(before[key].Bytes, after[key].Bytes)
			if changed != (mode == "write" || mode == "trigger") {
				t.Fatalf("wrong mutation visibility: %s", mode)
			}
		})
	}
}
func TestAtomicSnapshotDoesNotHideNewInvocationRoots(t *testing.T) {
	ctx, db, project := invocationScopeProject(t)
	actor := scopeActor(t, ctx, db, project, "new-root", "")
	object, err := EncodeEnvelope(actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE actor_invocations SET received_at='malformed' WHERE id='new-root'`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	ctx, err = WithAtomicImportSnapshot(ctx, tx, project)
	if err != nil {
		t.Fatal(err)
	}
	ctx, metrics := syncwork.WithWorkMetrics(ctx)
	if _, err := LocalEnvelopesForImport(ctx, tx, project, nil); err != nil {
		t.Fatalf("unrelated raw corruption affected scope: %v", err)
	}
	if _, err := LocalEnvelopesForImport(ctx, tx, project, []EncodedEnvelope{object}); err == nil {
		t.Fatal("cached closure hid malformed incoming identity")
	}
	if metrics.Counts()["atomic_import_snapshot_reuses"] != 0 || metrics.Counts()["operational_full_loads"] != 2 {
		t.Fatal(metrics.Counts())
	}
}
func TestAtomicSnapshotBoundsAndEvaluationTime(t *testing.T) {
	ctx, _, tx, project := atomicSnapshotFixture(t)
	cache := atomicSnapshotFor(ctx, tx, project)
	if _, err := LocalEnvelopesForImport(ctx, tx, project, nil); err != nil {
		t.Fatal(err)
	}
	oversized := map[string]EncodedEnvelope{}
	body := make([]byte, 128<<10)
	for i := 0; i < 512; i++ {
		oversized[fmt.Sprint(i)] = EncodedEnvelope{Bytes: body}
	}
	cache.store(ctx, nil, oversized)
	if cache.local != nil {
		t.Fatal("oversized snapshot retained")
	}
	before := cache.evaluatedAt.Add(-usage.FinishedAfter + time.Second).Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO usage_requests(project_id,client,session_id,request_id,model,occurred_at,input_tokens,cache_write_tokens,cache_write_long_tokens,cache_read_tokens,output_tokens,reasoning_tokens,sidechain) VALUES(?,'codex','boundary','one','model',?,1,0,0,0,0,0,0)`, project, before); err != nil {
		t.Fatal(err)
	}
	first, err := loadUsageSummarySpecs(ctx, tx, project, scopeLineage)
	if err != nil {
		t.Fatal(err)
	}
	cache.evaluatedAt = cache.evaluatedAt.Add(2 * time.Second) // Simulate a later operation's evaluation boundary.
	second, err := loadUsageSummarySpecs(ctx, tx, project, scopeLineage)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 0 || len(second) != 1 {
		t.Fatalf("summary evaluation time ignored: %d %d", len(first), len(second))
	}
	next, err := WithAtomicImportSnapshot(ctx, tx, project)
	if err != nil {
		t.Fatal(err)
	}
	if atomicSnapshotFor(next, tx, project) == cache {
		t.Fatal("new operation inherited snapshot")
	}
}

func TestAtomicSnapshotSeparatesConcurrentReadersAndSerializesWriters(t *testing.T) {
	ctx, db, tx, project := atomicSnapshotFixture(t)
	before, err := LocalEnvelopesForImport(ctx, tx, project, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE actor_invocations SET client='Uncommitted client' WHERE id='bound'`); err != nil {
		t.Fatal(err)
	}
	staged, err := LocalEnvelopesForImport(ctx, tx, project, nil)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	observed, err := LocalEnvelopesForImport(ctx, reader, project, nil)
	reader.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	key := RecordKey("actor_invocation", "bound", 0)
	if !bytes.Equal(observed[key].Bytes, before[key].Bytes) || bytes.Equal(observed[key].Bytes, staged[key].Bytes) {
		t.Fatal("reader saw another transaction's staged snapshot")
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `PRAGMA busy_timeout=25`); err != nil {
		t.Fatal(err)
	}
	other, err := conn.BeginTx(ctx, nil)
	if err == nil {
		other.Rollback()
		t.Fatal("concurrent writer acquired validated snapshot")
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	other, err = conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("writer remained blocked after atomic commit: %v", err)
	}
	defer other.Rollback()
	visible, err := LocalEnvelopesForImport(ctx, other, project, nil)
	if err != nil || !bytes.Equal(visible[key].Bytes, staged[key].Bytes) {
		t.Fatalf("next transaction missed committed data: %v", err)
	}
}

func TestAtomicSnapshotRefusesUncertifiedSchemaReads(t *testing.T) {
	for _, mode := range []string{"temp_view", "main_view", "changed_reviewed_view", "missing_tracking"} {
		t.Run(mode, func(t *testing.T) {
			ctx, _, tx, project := atomicSnapshotFixture(t)
			switch mode {
			case "temp_view":
				rows, err := tx.QueryContext(ctx, `PRAGMA main.table_info(actor_invocations)`)
				if err != nil {
					t.Fatal(err)
				}
				var columns []string
				for rows.Next() {
					var id, notnull, pk int
					var name, kind string
					var defaultValue any
					if err := rows.Scan(&id, &name, &kind, &notnull, &defaultValue, &pk); err != nil {
						t.Fatal(err)
					}
					if name == "model" {
						columns = append(columns, `hex(randomblob(16)) AS model`)
					} else {
						columns = append(columns, `"`+name+`"`)
					}
				}
				if err := rows.Err(); err != nil {
					t.Fatal(err)
				}
				rows.Close()
				if _, err := tx.ExecContext(ctx, `CREATE TEMP VIEW actor_invocations AS SELECT `+strings.Join(columns, ",")+` FROM main.actor_invocations`); err != nil {
					t.Fatal(err)
				}
			case "main_view":
				if _, err := tx.ExecContext(ctx, `CREATE VIEW fixture_atomic_view AS SELECT random() AS value`); err != nil {
					t.Fatal(err)
				}
			case "changed_reviewed_view":
				var body string
				if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE name='fork_rejected_records'`).Scan(&body); err != nil {
					t.Fatal(err)
				}
				if _, err := tx.ExecContext(ctx, `DROP VIEW fork_rejected_records`); err != nil {
					t.Fatal(err)
				}
				if _, err := tx.ExecContext(ctx, strings.ReplaceAll(body, "'supersession'", "'SUPERSESSION'")); err != nil {
					t.Fatal(err)
				}
			case "missing_tracking":
				var name string
				if err := tx.QueryRowContext(ctx, `SELECT name FROM sqlite_schema WHERE type='trigger' AND name LIKE 'history_sync_%' ORDER BY name LIMIT 1`).Scan(&name); err != nil {
					t.Fatal(err)
				}
				if _, err := tx.ExecContext(ctx, `DROP TRIGGER "`+strings.ReplaceAll(name, `"`, `""`)+`"`); err != nil {
					t.Fatal(err)
				}
			}
			ctx, metrics := syncwork.WithWorkMetrics(ctx)
			first, err := LocalEnvelopesForImport(ctx, tx, project, nil)
			if err != nil {
				t.Fatal(err)
			}
			second, err := LocalEnvelopesForImport(ctx, tx, project, nil)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "temp_view" {
				key := RecordKey("actor_invocation", "bound", 0)
				if bytes.Equal(first[key].Bytes, second[key].Bytes) {
					t.Fatal("nondeterministic view was hidden by stale snapshot")
				}
			}
			counts := metrics.Counts()
			if counts["atomic_import_snapshot_reuses"] != 0 || counts["atomic_import_snapshot_loads"] != 0 || counts["atomic_import_snapshot_guard_refusals"] != 2 || counts["operational_full_loads"] != 2 {
				t.Fatal(counts)
			}
		})
	}
}
