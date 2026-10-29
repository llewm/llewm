package recordsync

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/llewm/llewm/components/continuity/internal/buildinfo"
	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/portable"
)

type captureGuard struct{ route, generation, archive string }

func (a Archive) captureGuard(ctx context.Context, db *sql.DB, projectID string) (captureGuard, error) {
	var g captureGuard
	build := buildinfo.Current()
	if build.BuildDirty {
		return g, fmt.Errorf("uncommitted build cannot reuse capture proofs")
	}
	body, _ := json.Marshal([]any{"capture-v2", build.GitCommit, a.Directory, a.Lineage})
	g.route = Hash(string(body))
	var generationErr error
	g.generation, generationErr = database.SyncGeneration(ctx, db, projectID)
	if generationErr != nil {
		return g, generationErr
	}
	var err error
	g.archive, err = a.ChangeStamp(ctx)
	return g, err
}

// CaptureProject reuses a completed local export/capture only while both its
// database dependencies and archive remain unchanged. It says nothing about
// endpoint publication. Uncertain or missing proofs take the complete export.
func (a Archive) CaptureProject(ctx context.Context, db *sql.DB, projectID string) (bool, error) {
	identity, err := (portable.Service{DB: db}).Get(ctx, projectID)
	if err != nil {
		return false, err
	}
	if identity == nil || identity.LineageID != a.Lineage {
		return false, fmt.Errorf("matching project/archive lineage required")
	}
	before, guardErr := a.captureGuard(ctx, db, projectID)
	var cursor database.SyncCursor
	if guardErr == nil && before.generation != "" {
		var changes []database.SyncChange
		var fallback string
		cursor, changes, fallback, err = database.ReadSyncChanges(ctx, db, projectID, before.route, before.archive, 4096)
		if err == nil && fallback == "" && cursor.Generation == before.generation && captureMetadataOnly(changes) {
			after, err := a.captureGuard(ctx, db, projectID)
			if err == nil && after == before {
				// Consume only the observed batch. A racing canonical write
				// prevents acknowledgement and will be retried next time.
				_, _ = database.RememberSyncCursor(ctx, db, cursor, before.archive)
				return true, nil
			}
		}
	}
	export, err := portable.EncodeMemoryExport(ctx, db, projectID)
	if err != nil {
		return false, err
	}
	if export.Identity.LineageID != a.Lineage {
		return false, fmt.Errorf("project lineage changed during export")
	}
	if err := a.CaptureLocal(export.Envelopes); err != nil {
		return false, err
	}
	if guardErr == nil && before.generation != "" && captureDependenciesClosed(export.Envelopes) {
		after, err := a.captureGuard(ctx, db, projectID)
		if err == nil && after == before && cursor.Generation == before.generation {
			_, _ = database.RememberSyncCursor(ctx, db, cursor, before.archive)
		}
	}
	return false, nil
}

// Derived block membership is not a canonical export input. Other changes,
// including retained envelopes, deletes and dependency arrivals, still require
// the complete exporter until their dependency closure is qualified.
func captureMetadataOnly(changes []database.SyncChange) bool {
	for _, change := range changes {
		switch change.Table {
		case "history_index_blocks", "history_index_publications", "history_index_records", "history_index_refs":
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
