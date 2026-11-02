package recordsync

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

// Transport observations have already committed independently. Recovery,
// fork and main semantic writes below form one all-or-nothing unit.
func (s Index) importArchiveAtomic(ctx context.Context, a Archive, scope JournalPublicationScope, plan JournalPublicationPlan) (ImportResult, error) {
	objects, err := s.prepareImportBlocks(ctx, plan.Snapshot)
	if err != nil {
		return ImportResult{}, err
	}
	prepared, err := prepareSemanticImport(objects)
	if err != nil {
		return ImportResult{}, fmt.Errorf("%w: %v", ErrSemanticPending, err)
	}
	syncwork.RecordWork(ctx, "fallback_full_semantic_import", 1)
	syncwork.RecordWork(ctx, "full_semantic_records", int64(len(objects)))
	var result ImportResult
	err = a.store().transact(ctx, func(tx *sql.Tx) error {
		operation, err := portable.WithAtomicImportSnapshot(ctx, tx, s.ProjectID)
		if err != nil {
			return err
		}
		ctx = operation
		importer := Importer{DB: s.DB, ProjectID: s.ProjectID, Now: s.Now}
		if err := importer.importRecordsWithin(ctx, tx, prepared, &result); err != nil {
			return err
		}
		if result.DeferredRecords != 0 {
			return nil
		}
		// An ineligible baseline is an optimization miss, not a failed import.
		// Discard any provisional derived writes without undoing semantic success.
		if _, err := tx.ExecContext(ctx, `SAVEPOINT semantic_replay_baseline`); err != nil {
			return err
		}
		err = s.seedReplayJournalWithin(ctx, tx, a, scope, plan, objects)
		if errors.Is(err, errReplayJournalFull) {
			if _, rollbackErr := tx.ExecContext(ctx, `ROLLBACK TO semantic_replay_baseline`); rollbackErr != nil {
				return rollbackErr
			}
			_, releaseErr := tx.ExecContext(ctx, `RELEASE semantic_replay_baseline`)
			return releaseErr
		}
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `RELEASE semantic_replay_baseline`)
		return err
	})
	if err != nil {
		result = rolledBackImport(result)
		if errors.Is(err, errAtomicImportCollision) {
			return result, nil
		}
		return result, fmt.Errorf("%w: %v", ErrSemanticPending, err)
	}
	return result, nil
}
