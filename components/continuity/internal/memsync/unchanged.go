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
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

// Bump when the completion proof's interpretation changes. These device-local
// proofs are disposable; a miss or uncertainty always runs the reference cycle.
const syncProofVersion = 5

type syncGuard struct{ route, generation, tree, archive string }

func (s Syncer) guard(ctx context.Context, repo git) (syncGuard, error) {
	var g syncGuard
	identity, err := (portable.Service{DB: s.DB}).Get(ctx, s.ProjectID)
	if err != nil || identity == nil {
		return g, err
	}
	var projectRoot string
	if err := s.DB.QueryRowContext(ctx, `SELECT root FROM projects WHERE id=?`, s.ProjectID).Scan(&projectRoot); err != nil {
		return g, err
	}
	directory := filepath.Join(config.StateDir(projectRoot), "history")
	var a recordsync.Archive
	if s.Archive != nil {
		a = *s.Archive
		if a.Lineage != identity.LineageID || a.DB != s.DB || a.ProjectID != s.ProjectID {
			return g, fmt.Errorf("archive project binding differs")
		}
	} else {
		a, err = recordsync.OpenArchive(ctx, s.DB, s.ProjectID, identity.LineageID, directory)
		if err != nil {
			return g, err
		}
	}
	g.archive, err = a.ChangeStamp(ctx)
	if err != nil {
		return g, err
	}
	var generationErr error
	g.generation, generationErr = database.SyncGeneration(ctx, s.DB, s.ProjectID)
	if generationErr != nil {
		return g, generationErr
	}
	observation, err := (recordsync.Index{DB: s.DB, ProjectID: s.ProjectID, Lineage: identity.LineageID}).ObservationStamp(ctx, a)
	if err != nil {
		return g, err
	}
	tree, err := s.objectReader(repo).headTree(ctx)
	if err != nil {
		return g, err
	}
	scope, err := s.journalPublicationScope(ctx, repo, identity.LineageID, observation, tree)
	if err != nil {
		return g, err
	}
	publication, err := (recordsync.Index{DB: s.DB, ProjectID: s.ProjectID, Lineage: identity.LineageID}).PublicationJournalStamp(ctx, a, scope)
	if err != nil {
		return g, err
	}
	trackingBody, _ := json.Marshal([]any{g.archive, observation, publication})
	g.archive = recordsync.Hash(string(trackingBody))
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
	g.route = syncwork.ScopedValidationKey(ctx, recordsync.Hash(string(body)))
	return g, nil
}

func lineageTree(ctx context.Context, repo git, ref, lineage string) (string, error) {
	oid, err := repo.run(ctx, "rev-parse", "--verify", "--quiet", "--end-of-options", ref)
	if err != nil {
		return "", err
	}
	transport := &objectTransport{repo: repo}
	commit, err := transport.readCommit(ctx, oid)
	if err != nil {
		return "", err
	}
	return transport.lineageIdentity(ctx, commit.Tree, lineage)
}

func (t *objectTransport) lineageIdentity(ctx context.Context, tree, lineage string) (string, error) {
	if err := t.validateBlockLayout(ctx, tree, lineage); err != nil {
		return "", err
	}
	selected, err := t.treeAt(ctx, tree, "v5", strings.TrimPrefix(lineage, "lineage:"))
	if err != nil {
		return "", err
	}
	return "v5:" + selected, nil
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
		syncwork.RecordWork(ctx, reason, 1)
		return Result{}, false, fetched
	}
	head, err := repo.run(ctx, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return Result{}, false, fetched
	}
	// A prior incomplete delivery must still take the normal push/repair path.
	if unpushed(ctx, repo, remoteBranch(ctx, repo, head)) != 0 {
		syncwork.RecordWork(ctx, "fallback_unchanged_delivery_pending", 1)
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
		syncwork.RecordWork(ctx, "fallback_unchanged_remote_tree", 1)
		return Result{}, false, fetched
	}
	transport := s.objectReader(repo)
	current, err := transport.currentBranch(ctx)
	if err != nil {
		return Result{}, false, fetched
	}
	remote, err := repo.run(ctx, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+branch)
	if err != nil {
		return Result{}, false, fetched
	}
	lineage := s.lineageForGuard(ctx)
	next, err := transport.mergeCommits(ctx, current.OID, remote, "continuity: merge unchanged lineage history", func(ctx context.Context, _, ours, theirs string) error {
		for _, tree := range []string{ours, theirs} {
			selected, err := transport.lineageIdentity(ctx, tree, lineage)
			if err != nil {
				return err
			}
			if selected != g.tree {
				return fmt.Errorf("selected lineage changed during unchanged sync")
			}
		}
		return nil
	}, nil)
	if err != nil {
		return Result{}, false, fetched
	}
	if next != current.OID {
		if err := transport.advanceBranch(context.WithoutCancel(ctx), current, next); err != nil {
			return Result{}, false, fetched
		}
	}
	after, err := s.guard(ctx, repo)
	if err != nil || after != g || !upToDate(ctx, repo, branch) {
		return Result{}, false, fetched
	}
	result := Result{Outcome: OutcomeSynced, Detail: "synced with " + redact.String(s.Remote), Unchanged: true}
	result.ImportedHead, _ = repo.run(ctx, "rev-parse", "--verify", "--quiet", "HEAD")
	result.Publication, err = s.recordPublication(ctx, repo, lineage, result.ImportedHead, branch, true)
	if err != nil {
		return Result{}, false, fetched
	}
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
