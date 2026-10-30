package memsync

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/llewm/llewm/components/continuity/internal/buildinfo"
	"github.com/llewm/llewm/components/continuity/internal/config"
	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/recordsync"
	"github.com/llewm/llewm/components/continuity/internal/redact"
)

// Bump when the completion proof's interpretation changes. These device-local
// proofs are disposable; a miss or uncertainty always runs the reference cycle.
const syncProofVersion = 1

type syncGuard struct{ route, generation, tree, archive string }

func (s Syncer) guard(ctx context.Context, repo git) (syncGuard, error) {
	var g syncGuard
	identity, err := (portable.Service{DB: s.DB}).Get(ctx, s.ProjectID)
	if err != nil || identity == nil {
		return g, err
	}
	directory := ""
	if s.Archive != nil {
		if s.Archive.Lineage != identity.LineageID {
			return g, fmt.Errorf("archive lineage differs")
		}
		directory = s.Archive.Directory
	} else {
		var projectRoot string
		if err := s.DB.QueryRowContext(ctx, `SELECT root FROM projects WHERE id=?`, s.ProjectID).Scan(&projectRoot); err != nil {
			return g, err
		}
		directory = filepath.Join(config.StateDir(projectRoot), "history")
	}
	a := recordsync.Archive{Directory: directory, Lineage: identity.LineageID}
	g.archive, err = a.ChangeStamp(ctx)
	if err != nil {
		return g, err
	}
	var generationErr error
	g.generation, generationErr = database.SyncGeneration(ctx, s.DB, s.ProjectID)
	if generationErr != nil {
		return g, generationErr
	}
	g.tree, err = lineageTree(ctx, repo, "HEAD", identity.LineageID)
	if err != nil {
		return g, err
	}
	// Route and policy changes cannot inherit another endpoint's proof.
	build := buildinfo.Current()
	if build.BuildDirty {
		return g, fmt.Errorf("uncommitted build cannot reuse sync proofs")
	}
	body, _ := json.Marshal([]any{syncProofVersion, recordsync.IndexVersion, build.GitCommit, s.Remote, s.Clone, identity.LineageID, identity.InstanceID, directory, s.Relay})
	g.route = recordsync.Hash(string(body))
	return g, nil
}

func lineageTree(ctx context.Context, repo git, ref, lineage string) (string, error) {
	partition := strings.TrimPrefix(lineage, "lineage:")
	return repo.run(ctx, "ls-tree", ref, "--", "v2/"+partition, "v3/"+partition, "v4/"+partition)
}

func (s Syncer) tryUnchanged(ctx context.Context, repo git, p plan, g syncGuard) (Result, bool, bool) {
	fetched := false
	var saved syncGuard
	err := s.DB.QueryRowContext(ctx, `SELECT generation,tree,archive FROM history_sync_proofs WHERE project_id=? AND route=?`, s.ProjectID, g.route).Scan(&saved.generation, &saved.tree, &saved.archive)
	reason := ""
	switch {
	case err != nil:
		reason = "fallback_unchanged_proof_unavailable"
	case g.generation == "":
		reason = "fallback_unchanged_generation_unknown"
	case g.generation != saved.generation:
		reason = "fallback_unchanged_generation_changed"
	case g.tree != saved.tree:
		reason = "fallback_unchanged_tree_changed"
	case g.archive != saved.archive:
		reason = "fallback_unchanged_archive_changed"
	}
	if reason != "" {
		recordsync.RecordWork(ctx, reason, 1)
		return Result{}, false, fetched
	}
	clean, err := repo.run(ctx, "status", "--porcelain", "--untracked-files=no")
	if err != nil || clean != "" {
		recordsync.RecordWork(ctx, "fallback_unchanged_worktree_unclean", 1)
		return Result{}, false, fetched
	}
	head, err := repo.run(ctx, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return Result{}, false, fetched
	}
	// A prior incomplete delivery must still take the normal push/repair path.
	if unpushed(ctx, repo, remoteBranch(ctx, repo, head)) != 0 {
		recordsync.RecordWork(ctx, "fallback_unchanged_delivery_pending", 1)
		return Result{}, false, fetched
	}
	if err := probe(ctx, repo, s.Remote, p.deadline); err != nil {
		return Result{}, false, fetched
	}
	if err := s.fetch(ctx, repo, p); err != nil {
		return Result{}, false, fetched
	}
	fetched = true
	branch := remoteBranch(ctx, repo, head)
	remoteTree, err := lineageTree(ctx, repo, "origin/"+branch, s.lineageForGuard(ctx))
	if err != nil || remoteTree != g.tree {
		recordsync.RecordWork(ctx, "fallback_unchanged_remote_tree", 1)
		return Result{}, false, fetched
	}
	if _, err := repo.run(ctx, "rebase", "--quiet", "origin/"+branch); err != nil {
		_, _ = repo.run(context.WithoutCancel(ctx), "rebase", "--abort")
		return Result{}, false, fetched
	}
	after, err := s.guard(ctx, repo)
	if err != nil || after != g || !upToDate(ctx, repo, branch) {
		return Result{}, false, fetched
	}
	result := Result{Outcome: OutcomeSynced, Detail: "synced with " + redact.String(s.Remote), Unchanged: true}
	result.ImportedHead, _ = repo.run(ctx, "rev-parse", "--verify", "--quiet", "HEAD")
	if s.Leases != nil {
		result.LeaseNotes = s.syncLeases(ctx, repo, p)
	}
	return result, true, fetched
}

func (s Syncer) lineageForGuard(ctx context.Context) string {
	identity, err := (portable.Service{DB: s.DB}).Get(ctx, s.ProjectID)
	if err != nil || identity == nil {
		return ""
	}
	return identity.LineageID
}

func (s Syncer) rememberUnchanged(ctx context.Context, repo git, before syncGuard, result Result) {
	if result.Outcome != OutcomeSynced || result.Collisions+result.Forks+result.SupersessionForks+result.DeferredRecords+result.UnpushedCommits != 0 || result.SemanticPending != "" {
		return
	}
	after, err := s.guard(ctx, repo)
	if err != nil || before.generation == "" || before != after {
		return
	}
	// A concurrent canonical write changes the generation in the same SQLite
	// transaction. Never bless that write by reading a newer token here.
	_ = database.RememberSyncProof(ctx, s.DB, s.ProjectID, before.route, before.generation, before.tree, before.archive)
}
