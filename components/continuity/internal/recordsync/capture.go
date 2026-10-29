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
	var changes []database.SyncChange
	var fallback string
	var deltaEligible bool
	if guardErr == nil && before.generation != "" {
		cursor, changes, fallback, err = database.ReadSyncChanges(ctx, db, projectID, before.route, before.archive, 4096)
		deltaEligible = err == nil && fallback == "" && cursor.Generation == before.generation
		if err == nil && fallback == "" && cursor.Generation == before.generation && captureMetadataOnly(changes) {
			after, err := a.captureGuard(ctx, db, projectID)
			if err == nil && after == before {
				// Consume only the observed batch. A racing canonical write
				// prevents acknowledgement and will be retried next time.
				_, _ = database.RememberSyncCursor(ctx, db, cursor, before.archive)
				return true, nil
			}
			// A failed second guard is uncertainty about the archive, not
			// evidence that an empty selected export would validate it.
			deltaEligible = false
		}
	}
	var export portable.MemoryExport
	var delta bool
	if deltaEligible {
		export, cursor, delta, err = portable.EncodeMemoryDelta(ctx, db, cursor, changes)
		if err != nil {
			return false, err
		}
		if delta {
			// The exporter retained only these selected identities while
			// holding the writer. Its receipt includes those exact writes.
			before.generation = cursor.Generation
		}
	}
	if !delta {
		export, err = portable.EncodeMemoryExport(ctx, db, projectID)
	}
	if err != nil {
		return false, err
	}
	if export.Identity.LineageID != a.Lineage {
		return false, fmt.Errorf("project lineage changed during export")
	}
	var publication *capturePublication
	var beforeCreate func()
	eligible := guardErr == nil && before.generation != "" && cursor.Generation == before.generation && captureDependenciesClosed(export.Envelopes)
	acknowledged := false
	if eligible {
		// The exporter may itself retain new envelopes. Do not build a
		// filesystem receipt for a database snapshot it already invalidated.
		generation, err := database.SyncGeneration(ctx, db, projectID)
		eligible = err == nil && generation == before.generation
		if eligible {
			// Snapshot only before the first actual publication. Revalidating
			// existing immutable files needs no metadata maps or extra walks.
			attempted := false
			beforeCreate = func() {
				if !attempted {
					attempted = true
					publication = a.prepareCapturePublication(ctx, before.archive, export.Envelopes)
				}
			}
		}
	}
	if err := a.captureLocal(export.Envelopes, beforeCreate); err != nil {
		return false, err
	}
	if eligible {
		published := publication.completedStamp(ctx, a)
		after, err := a.captureGuard(ctx, db, projectID)
		if err == nil && after.route == before.route && after.generation == before.generation && cursor.Generation == before.generation && (after.archive == before.archive || published != "" && after.archive == published) {
			acknowledged, _ = database.RememberSyncCursor(ctx, db, cursor, after.archive)
		}
	}
	if delta && !acknowledged {
		// Never let a failed publication receipt turn selected export into
		// partial validation. Repair/validate all local records this cycle;
		// leave the old cursor untouched so recovery remains conservative.
		complete, err := portable.EncodeMemoryExport(ctx, db, projectID)
		if err != nil {
			return false, err
		}
		if complete.Identity.LineageID != a.Lineage {
			return false, fmt.Errorf("project lineage changed during export")
		}
		if err := a.captureLocal(complete.Envelopes, nil); err != nil {
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
