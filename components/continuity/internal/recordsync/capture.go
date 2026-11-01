package recordsync

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/llewm/llewm/components/continuity/internal/buildinfo"
	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

func (a Archive) captureRoute() (string, error) {
	build := buildinfo.Current()
	if build.BuildDirty {
		return "", fmt.Errorf("uncommitted build cannot reuse capture proofs")
	}
	body, _ := json.Marshal([]any{"capture-sqlite-v1", build.GitCommit, a.ProjectID, a.Lineage})
	return Hash(string(body)), nil
}

// CaptureProject binds canonical export progress to the immutable archive epoch.
// Archive insertion and cursor acknowledgement share the writer transaction;
// foreign arrivals and ordinary appends need no filesystem receipt or scan.
func (a Archive) CaptureProject(ctx context.Context, db *sql.DB, project string) (bool, error) {
	if db != a.DB || project != a.ProjectID {
		return false, fmt.Errorf("matching project/archive database required")
	}
	route, routeErr := a.captureRoute()
	var epoch string
	var cursor database.SyncCursor
	var changes []database.SyncChange
	var fallback string
	var cursorErr error
	if err := a.store().view(ctx, func(tx *sql.Tx) error {
		stamp, err := a.store().stampWithin(ctx, tx, "ready")
		if err != nil {
			return err
		}
		epoch = stamp.Epoch
		if routeErr == nil {
			cursor, changes, fallback, cursorErr = database.ReadSyncChangesWithin(ctx, tx, project, route, epoch, 4096)
		}
		return nil
	}); err != nil {
		return false, err
	}
	recordCaptureCursorFallback(ctx, fallback, cursorErr)
	qualified := routeErr == nil && cursorErr == nil && fallback == ""
	if qualified && captureMetadataOnly(changes) {
		reused := false
		err := a.store().transact(ctx, func(tx *sql.Tx) error {
			stamp, err := a.store().stampWithin(ctx, tx, "ready")
			if err != nil {
				return err
			}
			if stamp.Epoch != epoch {
				return nil
			}
			reused, err = database.RememberSyncCursorWithin(ctx, tx, cursor, epoch)
			return err
		})
		if err != nil {
			return false, err
		}
		if reused {
			return true, nil
		}
		qualified = false
	}
	var exported portable.MemoryExport
	var delta bool
	var err error
	if qualified {
		exported, cursor, delta, err = portable.EncodeMemoryDelta(ctx, db, cursor, changes)
		if err != nil {
			return false, err
		}
	}
	if !delta {
		syncwork.RecordWork(ctx, "capture_full_exports", 1)
		exported, err = portable.EncodeMemoryExport(ctx, db, project)
	}
	if err != nil {
		return false, err
	}
	if exported.Identity.LineageID != a.Lineage {
		return false, fmt.Errorf("project lineage changed during export")
	}
	acknowledged := false
	err = a.store().transact(ctx, func(tx *sql.Tx) error {
		stamp, err := a.store().stampWithin(ctx, tx, "ready")
		if err != nil {
			return err
		}
		if !delta || stamp.Epoch != epoch {
			if _, err := a.store().auditWithin(ctx, tx); err != nil {
				return err
			}
		}
		if err := a.captureLocalWithin(ctx, tx, exported.Envelopes); err != nil {
			return err
		}
		if routeErr == nil && cursorErr == nil && stamp.Epoch == epoch && captureDependenciesClosed(exported.Envelopes) {
			acknowledged, err = database.RememberSyncCursorWithin(ctx, tx, cursor, epoch)
		}
		return err
	})
	if err != nil {
		return false, err
	}
	if delta && !acknowledged {
		syncwork.RecordWork(ctx, "fallback_capture_publication_receipt", 1)
		syncwork.RecordWork(ctx, "capture_full_exports", 1)
		complete, err := portable.EncodeMemoryExport(ctx, db, project)
		if err != nil {
			return false, err
		}
		if complete.Identity.LineageID != a.Lineage {
			return false, fmt.Errorf("project lineage changed during export")
		}
		if err := a.store().transact(ctx, func(tx *sql.Tx) error {
			if _, err := a.store().auditWithin(ctx, tx); err != nil {
				return err
			}
			return a.captureLocalWithin(ctx, tx, complete.Envelopes)
		}); err != nil {
			return false, err
		}
	}
	return false, nil
}

// Derived block membership is not a canonical export input. Canonical changes
// use selected export only for qualified journal batches; uncertainty still
// requires the complete exporter.
func captureMetadataOnly(changes []database.SyncChange) bool {
	for _, change := range changes {
		switch change.Table {
		case "history_index_blocks", "history_index_publications", "history_index_envelopes", "history_index_block_members", "history_index_refs":
		default:
			return false
		}
	}
	return true
}

// Raw invocations deliberately do not invalidate generations until referenced
// by indexed/retained history. A standalone capture must not cache an export
// with an unresolved actor reference before an endpoint has indexed it.
func captureDependenciesClosed(objects []portable.EncodedEnvelope) bool {
	actors := map[string]bool{}
	for _, object := range objects {
		if object.Envelope.RecordType == "actor_invocation" {
			actors[object.Envelope.RecordID] = true
		}
	}
	for _, object := range objects {
		for _, ref := range object.Envelope.Refs {
			if ref.Kind == "actor_invocation" && !actors[ref.ID] {
				return false
			}
		}
	}
	return true
}

// Keep diagnostic categories static: errors and database contents never enter
// metric names, including if a future cursor reader adds a fallback reason.
func recordCaptureCursorFallback(ctx context.Context, reason string, err error) {
	if err != nil {
		syncwork.RecordWork(ctx, "fallback_capture_cursor_error", 1)
		return
	}
	var name string
	switch reason {
	case "":
		return
	case "missing_cursor":
		name = "fallback_capture_missing_cursor"
	case "damaged_cursor":
		name = "fallback_capture_damaged_cursor"
	case "materialization_changed":
		name = "fallback_capture_archive_changed"
	case "global_change":
		name = "fallback_capture_global_change"
	case "missing_changes":
		name = "fallback_capture_missing_changes"
	case "unknown_tracking":
		name = "fallback_capture_unknown_tracking"
	case "delta_limit":
		name = "fallback_capture_delta_limit"
	default:
		name = "fallback_capture_unknown_cursor_reason"
	}
	syncwork.RecordWork(ctx, name, 1)
}
