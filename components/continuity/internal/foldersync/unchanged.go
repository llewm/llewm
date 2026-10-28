package foldersync

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/llewm/llewm/components/continuity/internal/buildinfo"
	"github.com/llewm/llewm/components/continuity/internal/recordsync"
)

type completionGuard struct{ route, generation, endpoint, archive string }

func (s Syncer) completionGuard(ctx context.Context) (completionGuard, error) {
	var g completionGuard
	build := buildinfo.Current()
	if build.BuildDirty {
		return g, fmt.Errorf("uncommitted build cannot reuse completion proofs")
	}
	body, _ := json.Marshal([]any{"folder-v1", build.GitCommit, recordsync.IndexVersion, s.Endpoint.ID(), s.Endpoint.Directory, s.Endpoint.LineageID, s.Endpoint.StoreID, s.Endpoint.Relay, s.Archive.Directory})
	g.route = recordsync.Hash(string(body))
	if err := s.DB.QueryRowContext(ctx, `SELECT token FROM history_sync_generation WHERE id=1`).Scan(&g.generation); err != nil {
		return g, err
	}
	var err error
	g.endpoint, err = recordsync.DirectoryChangeStamp(ctx, s.Endpoint.Directory)
	if err != nil {
		return g, err
	}
	g.archive, err = s.Archive.ChangeStamp(ctx)
	return g, err
}

func (s Syncer) completedUnchanged(ctx context.Context, g completionGuard) bool {
	var generation, endpoint, archive string
	err := s.DB.QueryRowContext(ctx, `SELECT generation,tree,archive FROM history_sync_proofs WHERE project_id=? AND route=?`, s.ProjectID, g.route).Scan(&generation, &endpoint, &archive)
	if err != nil || generation != g.generation || endpoint != g.endpoint || archive != g.archive {
		return false
	}
	after, err := s.completionGuard(ctx)
	return err == nil && after == g
}

func (s Syncer) rememberCompletion(ctx context.Context, g completionGuard, result Result) {
	if result.Outcome != "published_locally" || result.Collisions+result.DeferredRecords+result.Forks+result.SupersessionForks != 0 {
		return
	}
	after, err := s.completionGuard(ctx)
	if err != nil || g.generation == "" || after != g {
		return
	}
	_, _ = s.DB.ExecContext(ctx, `INSERT INTO history_sync_proofs(project_id,route,generation,tree,archive) SELECT ?,?,?,?,? WHERE EXISTS(SELECT 1 FROM history_sync_generation WHERE id=1 AND token=?) ON CONFLICT(project_id,route) DO UPDATE SET generation=excluded.generation,tree=excluded.tree,archive=excluded.archive`, s.ProjectID, g.route, g.generation, g.endpoint, g.archive, g.generation)
}
