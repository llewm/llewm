package portable

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

const maxAtomicSnapshotBytes = 64 << 20

var errAtomicSnapshotSchema = errors.New("atomic snapshot schema is not certified")

type atomicSnapshotKey struct{}
type atomicSnapshotGuard struct {
	changes, mainSchema, tempSchema int64
	evaluatedAt                     time.Time
}
type atomicImportSnapshot struct {
	tx          *sql.Tx
	project     string
	evaluatedAt time.Time
	guard       atomicSnapshotGuard
	local       map[string]EncodedEnvelope
	roots       map[string]struct{}
}

// WithAtomicImportSnapshot reserves the writer and scopes complete-state reuse
// to one sequential semantic operation on this exact transaction and project.
// The caller must not concurrently use tx. No receipt survives a transaction,
// is persisted, or replaces incoming validation. Any write or schema change
// requires a fresh load; an unavailable guard simply disables reuse.
func WithAtomicImportSnapshot(ctx context.Context, tx *sql.Tx, project string) (context.Context, error) {
	if _, err := tx.ExecContext(ctx, `UPDATE projects SET name=name WHERE id=? AND 0`, project); err != nil {
		return ctx, err
	}
	return context.WithValue(ctx, atomicSnapshotKey{}, &atomicImportSnapshot{tx: tx, project: strings.Clone(project), evaluatedAt: time.Now().UTC()}), nil
}

func atomicSnapshotFor(ctx context.Context, source queryer, project string) *atomicImportSnapshot {
	cache, _ := ctx.Value(atomicSnapshotKey{}).(*atomicImportSnapshot)
	if cache == nil || source != cache.tx || project != cache.project {
		return nil
	}
	return cache
}
func importSnapshotTransaction(source queryer) *sql.Tx {
	for {
		switch reader := source.(type) {
		case *sql.Tx:
			return reader
		case *importInvocationSource:
			source = reader.queryer
		case *recordSelectionSource:
			source = reader.queryer
		case *primaryExportSource:
			source = reader.queryer
		case recoveryComparisonSource:
			source = reader.queryer
		default:
			return nil
		}
	}
}

func importSnapshotTime(ctx context.Context, source queryer, project string) time.Time {
	cache, _ := ctx.Value(atomicSnapshotKey{}).(*atomicImportSnapshot)
	if cache != nil && cache.project == project && importSnapshotTransaction(source) == cache.tx {
		return cache.evaluatedAt
	}
	return time.Now().UTC()
}
func (cache *atomicImportSnapshot) current(ctx context.Context) (atomicSnapshotGuard, error) {
	guard := atomicSnapshotGuard{evaluatedAt: cache.evaluatedAt}
	if err := cache.tx.QueryRowContext(ctx, `SELECT total_changes()`).Scan(&guard.changes); err != nil {
		return guard, err
	}
	if err := cache.tx.QueryRowContext(ctx, `PRAGMA main.schema_version`).Scan(&guard.mainSchema); err != nil {
		return guard, err
	}
	if err := cache.tx.QueryRowContext(ctx, `PRAGMA temp.schema_version`).Scan(&guard.tempSchema); err != nil {
		return guard, err
	}
	// A view can evaluate nondeterministic expressions without changing rows.
	// TEMP objects can shadow the compiled canonical input tables entirely.
	knownReaders, err := database.HistoryAtomicSnapshotReaders(ctx, cache.tx)
	if err != nil {
		return guard, err
	}
	if !knownReaders {
		return guard, errAtomicSnapshotSchema
	}
	known, err := database.HistoryCanonicalTracking(ctx, cache.tx)
	if err != nil {
		return guard, err
	}
	if !known {
		return guard, errAtomicSnapshotSchema
	}
	return guard, nil
}
func cloneImportSnapshot(local map[string]EncodedEnvelope) map[string]EncodedEnvelope {
	result := make(map[string]EncodedEnvelope, len(local))
	for key, value := range local {
		result[key] = cloneEncodedEnvelope(value)
	}
	return result
}
func (cache *atomicImportSnapshot) load(ctx context.Context, roots map[string]struct{}) (map[string]EncodedEnvelope, bool) {
	if cache == nil || cache.local == nil {
		return nil, false
	}
	guard, err := cache.current(ctx)
	if err != nil || guard != cache.guard {
		cache.local = nil
		cache.roots = nil
		syncwork.RecordWork(ctx, "atomic_import_snapshot_invalidations", 1)
		return nil, false
	}
	for root := range roots {
		if _, ok := cache.roots[root]; !ok {
			return nil, false
		}
	}
	syncwork.RecordWork(ctx, "atomic_import_snapshot_reuses", 1)
	return cloneImportSnapshot(cache.local), true
}
func (cache *atomicImportSnapshot) store(ctx context.Context, roots map[string]struct{}, local map[string]EncodedEnvelope) {
	if cache == nil {
		return
	}
	cache.local = nil
	cache.roots = nil
	cost := 0
	for _, value := range local {
		cost += 3*len(value.Bytes) + 512
		if cost > maxAtomicSnapshotBytes {
			syncwork.RecordWork(ctx, "atomic_import_snapshot_bounds", 1)
			return
		}
	}
	for id := range roots {
		cost += len(id) + 64
		if cost > maxAtomicSnapshotBytes {
			syncwork.RecordWork(ctx, "atomic_import_snapshot_bounds", 1)
			return
		}
	}
	guard, err := cache.current(ctx)
	if err != nil {
		syncwork.RecordWork(ctx, "atomic_import_snapshot_guard_refusals", 1)
		return
	}
	cache.local = cloneImportSnapshot(local)
	cache.roots = make(map[string]struct{}, len(roots))
	cache.guard = guard
	for id := range roots {
		cache.roots[strings.Clone(id)] = struct{}{}
	}
	syncwork.RecordWork(ctx, "atomic_import_snapshot_loads", 1)
}
