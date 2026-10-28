package recordsync

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/llewm/llewm/components/continuity/internal/buildinfo"
	"github.com/llewm/llewm/components/continuity/internal/portable"
)

type captureGuard struct{ route, generation, archive string }

func (a Archive) captureGuard(ctx context.Context, db *sql.DB, projectID string) (captureGuard, error) {
	var g captureGuard
	build := buildinfo.Current()
	if build.BuildDirty {
		return g, fmt.Errorf("uncommitted build cannot reuse capture proofs")
	}
	body, _ := json.Marshal([]any{"capture-v1", build.GitCommit, a.Directory, a.Lineage})
	g.route = Hash(string(body))
	if err := db.QueryRowContext(ctx, `SELECT token FROM history_sync_generation WHERE id=1`).Scan(&g.generation); err != nil {
		return g, err
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
	if guardErr == nil && before.generation != "" {
		var generation, archive string
		err := db.QueryRowContext(ctx, `SELECT generation,archive FROM history_sync_proofs WHERE project_id=? AND route=?`, projectID, before.route).Scan(&generation, &archive)
		if err == nil && generation == before.generation && archive == before.archive {
			after, err := a.captureGuard(ctx, db, projectID)
			if err == nil && after == before {
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
		if err == nil && after == before {
			_, _ = db.ExecContext(ctx, `INSERT INTO history_sync_proofs(project_id,route,generation,tree,archive) SELECT ?,?,?,'',? WHERE EXISTS(SELECT 1 FROM history_sync_generation WHERE id=1 AND token=?) ON CONFLICT(project_id,route) DO UPDATE SET generation=excluded.generation,archive=excluded.archive`, projectID, before.route, before.generation, before.archive, before.generation)
		}
	}
	return false, nil
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
