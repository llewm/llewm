package recordsync

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/llewm/llewm/components/continuity/internal/buildinfo"
	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

func (a Archive) captureRoute(ctx context.Context) (string, error) {
	build := buildinfo.Current()
	if build.BuildDirty {
		return "", fmt.Errorf("uncommitted build cannot reuse capture proofs")
	}
	body, _ := json.Marshal([]any{"capture-sqlite-v1", build.GitCommit, a.ProjectID, a.Lineage})
	return syncwork.ScopedValidationKey(ctx, Hash(string(body))), nil
}

// CaptureProject binds canonical export progress to the immutable archive epoch.
// Archive insertion and cursor acknowledgement share the writer transaction;
// foreign arrivals and ordinary appends need no filesystem receipt or scan.
func (a Archive) CaptureProject(ctx context.Context, db *sql.DB, project string) (bool, error) {
	if db != a.DB || project != a.ProjectID {
		return false, fmt.Errorf("matching project/archive database required")
	}
	route, routeErr := a.captureRoute(ctx)
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
	verified := a.freshCaptureExportObservations(exported)
	acknowledged := false
	err = a.store().transact(ctx, func(tx *sql.Tx) error {
		stamp, err := a.store().stampWithin(ctx, tx, "ready")
		if err != nil {
			return err
		}
		if !delta || stamp.Epoch != epoch {
			if _, err := a.store().auditVerifiedWithin(ctx, tx, verified); err != nil {
				return err
			}
		}
		if err := a.captureLocalVerifiedWithin(ctx, tx, exported.Envelopes, verified); err != nil {
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
		verified := a.freshCaptureExportObservations(complete)
		if err := a.store().transact(ctx, func(tx *sql.Tx) error {
			if _, err := a.store().auditVerifiedWithin(ctx, tx, verified); err != nil {
				return err
			}
			return a.captureLocalVerifiedWithin(ctx, tx, complete.Envelopes, verified)
		}); err != nil {
			return false, err
		}
	}
	return false, nil
}

const maxCaptureExportObservations = 8192

// Call only with the immediately preceding package-owned EncodeMemoryExport or
// EncodeMemoryDelta result. Those readers fully validate/reconstruct canonical
// bytes. Public CaptureLocal inputs never enter this map. The map adds bounded
// metadata references to already-owned export bytes, without copying bodies or
// retaining any database receipt across the export/capture transaction boundary.
func (a Archive) freshCaptureExportObservations(exported portable.MemoryExport) map[string]portable.EncodedEnvelope {
	verified := make(map[string]portable.EncodedEnvelope, min(maxCaptureExportObservations, len(exported.Envelopes)))
	for _, object := range exported.Envelopes {
		if len(verified) == maxCaptureExportObservations {
			break
		}
		e := object.Envelope
		// Encoding can normalize invalid UTF-8, and permits metadata larger
		// than transport's outer bound. Use the decoder for those inputs.
		metadataExact := utf8.ValidString(e.RecordID) && utf8.ValidString(e.Authority)
		for _, ref := range e.Refs {
			metadataExact = metadataExact && utf8.ValidString(ref.Kind) && utf8.ValidString(ref.ID)
		}
		if metadataExact && len(object.Bytes) <= MaxObjectBytes && e.LineageID == a.Lineage && e.Format == "llewm-portable-record" && e.FormatVersion == portable.PortableFormatVersion && portable.SupportedEnvelope(e) {
			verified[e.EnvelopeSHA256] = object
		}
	}
	return verified
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
