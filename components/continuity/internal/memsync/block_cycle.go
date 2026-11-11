package memsync

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/llewm/llewm/components/continuity/internal/config"
	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/recordsync"
	"github.com/llewm/llewm/components/continuity/internal/redact"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

// cycleBlocks is the Git block workflow, including guarded reuse of a completed
// cycle when the local inputs and fetched lineage tree remain unchanged.
func (s Syncer) cycleBlocks(ctx context.Context, p plan) (Result, error) {
	var result Result
	if err := stopped(ctx, "starting"); err != nil {
		return result, err
	}
	local := context.WithoutCancel(ctx)
	repo, err := s.repository(local)
	if err != nil {
		return result, err
	}
	lock, err := lockFile(filepath.Join(s.Clone, "continuity-sync.lock"))
	if err != nil {
		return result, err
	}
	defer lock.Close()
	s.objects = &objectTransport{repo: repo}
	before, guardErr := s.guard(local, repo)
	if guardErr != nil {
		syncwork.RecordWork(ctx, "fallback_unchanged_guard_unavailable", 1)
	}
	prefetched := false
	if !p.convert && guardErr == nil {
		unchanged, ok, fetched := s.tryUnchanged(ctx, repo, p, before)
		prefetched = fetched
		if ok {
			return unchanged, nil
		}
	}
	if err := stopped(ctx, "exporting"); err != nil {
		return result, err
	}
	identity, err := (portable.Service{DB: s.DB}).Get(local, s.ProjectID)
	if err != nil {
		return result, err
	}
	if identity == nil {
		return result, fmt.Errorf("project portable identity is not initialized")
	}
	if s.Archive == nil {
		var projectRoot string
		if err := s.DB.QueryRowContext(local, `SELECT root FROM projects WHERE id=?`, s.ProjectID).Scan(&projectRoot); err != nil {
			return result, err
		}
		archive, err := recordsync.OpenArchive(local, s.DB, s.ProjectID, identity.LineageID, filepath.Join(config.StateDir(projectRoot), "history"))
		if err != nil {
			return result, err
		}
		s.Archive = &archive
	}
	if s.Archive.Lineage != identity.LineageID {
		return result, fmt.Errorf("matching history archive is required")
	}
	if cache, err := openBlockTreeCache(s.Clone); err == nil {
		defer cache.Close()
		s.objects.trees.cache = cache
	}
	// Rebuild receipt membership and provenance from already tracked local
	// history before classifying operational records as locally originated or
	// planning publication. This also repairs damaged entries that are present.
	tree, err := s.objects.headTree(local)
	if err != nil {
		return result, err
	}
	if err := s.objects.validateBlockLayoutForConversion(local, tree, identity.LineageID, p.convert); err != nil {
		return result, err
	}
	if p.convert {
		for _, previous := range []bool{true, false} {
			old, err := s.objects.legacySnapshot(local, tree, identity.LineageID, previous)
			if err != nil {
				return result, err
			}
			for _, object := range old.Objects {
				if _, err := s.Archive.Put(object, s.Endpoint()); err != nil {
					return result, err
				}
			}
		}
	}
	// The local namespace layout is validated. Fetch fills only remote
	// refs while observation/capture/publication keep their commit boundaries.
	// Git maintenance is disabled by the runner; all local writes still finish
	// before any remote history is merged or imported, even if fetching fails.
	var fetching *fetchTask
	if !p.convert && !prefetched {
		fetching = startFetchTask(ctx, func(worker context.Context) fetchResult {
			if err := probe(worker, repo, s.Remote, p.deadline); err != nil {
				return fetchResult{err: err, probe: true}
			}
			return fetchResult{err: s.fetch(worker, repo, p)}
		})
		defer fetching.close()
	}
	var observation *gitObservationReceipt
	if p.convert {
		if _, err := s.trackedPublications(local, repo, identity.LineageID); err != nil {
			return result, err
		}
	} else {
		_, observed, err := s.observeGitPublications(local, repo, identity.LineageID, false)
		if err != nil {
			return result, err
		}
		observation = observed
	}
	if _, err := s.Archive.CaptureProject(local, s.DB, s.ProjectID); err != nil {
		return result, err
	}
	blocks, snapshot, err := s.publishBlocks(local, repo, identity.LineageID, observation)
	if err != nil {
		return result, err
	}
	count, err := commitBlocks(local, repo, identity.LineageID, blocks, fmt.Sprintf("continuity: %s", identity.InstanceID), p.convert)
	if err != nil {
		return result, err
	}
	result.ExportedRecords = count
	captured, err := s.objects.currentBranch(local)
	if err != nil {
		return result, err
	}
	head, err := repo.run(local, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return result, err
	}
	branch := head
	importBlocks := func() error {
		if p.convert {
			converted, err := s.convertLegacy(local, repo, *identity)
			if err != nil {
				return err
			}
			result.ExportedRecords += converted
			result.Converted = true
		}
		importCurrent := func() error {
			imported, err := s.importTrackedBlocks(local, repo, identity.LineageID, snapshot)
			result.ImportedRecords += imported.ImportedRecords
			result.Collisions, result.Forks, result.SupersessionForks, result.DeferredRecords = imported.Collisions, imported.Forks, imported.SupersessionForks, imported.DeferredRecords
			if err != nil {
				if errors.Is(err, recordsync.ErrSemanticPending) {
					result.SemanticPending = redact.String(err.Error())
					return nil
				}
				return err
			}
			if result.Collisions == 0 && result.DeferredRecords == 0 && result.SemanticPending == "" {
				result.ImportedHead, _ = repo.run(local, "rev-parse", "--verify", "--quiet", "HEAD")
			}
			return nil
		}
		return importCurrent()
	}
	offline, deferred, imported := false, false, false
	for attempt := 0; ; attempt++ {
		if err := stopped(ctx, "fetching"); err != nil {
			return result, err
		}
		var probeErr, fetchErr error
		if attempt == 0 && fetching != nil {
			fetched := fetching.wait()
			if fetched.probe {
				probeErr = fetched.err
			} else {
				fetchErr = fetched.err
			}
			prefetched = fetched.err == nil
		}
		if attempt == 0 && !prefetched {
			if fetching == nil {
				probeErr = probe(ctx, repo, s.Remote, p.deadline)
			}
			if err := probeErr; errors.Is(err, errBudget) {
				deferred, result.Detail = true, fmt.Sprintf("no time left to fetch within %s; a background sync continues", p.budget)
				break
			} else if err != nil {
				if ctxErr := stopped(ctx, "fetching"); ctxErr != nil {
					return result, ctxErr
				}
				offline, result.Detail = true, "remote unreachable: "+redact.String(err.Error())
				break
			}
		}
		if attempt != 0 || !prefetched {
			if attempt != 0 || fetching == nil {
				fetchErr = s.fetch(ctx, repo, p)
			}
			if err := fetchErr; errors.Is(err, errBudget) {
				deferred, result.Detail = true, fmt.Sprintf("fetch did not finish within %s; a background sync continues", p.budget)
				break
			} else if err != nil {
				if ctxErr := stopped(ctx, "importing"); ctxErr != nil {
					return result, ctxErr
				}
				offline, result.Detail = true, "remote unreachable: "+redact.String(err.Error())
				break
			}
		}
		branch = remoteBranch(local, repo, head)
		if remoteExists(local, repo, branch) {
			current, err := s.objects.currentBranch(local)
			if err != nil {
				return result, err
			}
			remote, err := repo.run(local, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+branch)
			if err != nil {
				return result, err
			}
			validate := func(ctx context.Context, base, ours, theirs string) error {
				for _, candidate := range []string{ours, theirs} {
					if candidate == "" {
						continue
					}
					if _, err := s.objects.blockDeltaForConversion(ctx, base, candidate, identity.LineageID, p.convert); err != nil {
						return err
					}
					if p.convert {
						for _, previous := range []bool{true, false} {
							if _, err := s.objects.legacySnapshot(ctx, candidate, identity.LineageID, previous); err != nil {
								return err
							}
						}
					}
				}
				return nil
			}
			next, err := s.objects.mergeCommits(local, current.OID, remote, fmt.Sprintf("continuity: %s merge immutable history", identity.InstanceID), validate, s.objects.finishBlockMerge(identity.LineageID, p.convert))
			if err != nil {
				return result, err
			}
			if next != current.OID {
				if err := s.objects.advanceBranch(local, current, next); err != nil {
					return result, err
				}
			}
		}
		if err := importBlocks(); err != nil {
			return result, fmt.Errorf("import memory blocks: %w", err)
		}
		imported = true
		if upToDate(local, repo, branch) {
			break
		}
		if !p.push {
			deferred, result.Detail = true, "fetched and imported; a background sync pushes"
			break
		}
		if err := stopped(ctx, "pushing"); err != nil {
			return result, err
		}
		err := s.push(ctx, repo, branch, p)
		if err == nil {
			break
		} else if stoppedErr := stopped(ctx, "retrying the push"); stoppedErr != nil {
			return result, stoppedErr
		} else if attempt == 1 {
			offline, result.Detail = true, "push rejected: "+redact.String(err.Error())
			break
		}
	}
	if imported && !offline && s.Leases != nil {
		result.LeaseNotes = s.syncLeases(ctx, repo, p)
	}
	if p.convert && !imported {
		return result, fmt.Errorf("format-5 conversion needs the remote, and it did not finish: %s", result.Detail)
	}
	if offline || deferred {
		if !imported {
			if err := importBlocks(); err != nil {
				return result, fmt.Errorf("import local memory blocks: %w", err)
			}
		}
		result.Outcome = OutcomeOffline
		if deferred {
			result.Outcome = OutcomeDeferred
		}
	} else {
		result.Outcome, result.Detail = OutcomeSynced, "synced with "+redact.String(s.Remote)
	}
	if p.convert {
		result.Detail = "history observations converted to format 5; " + result.Detail
	}
	if result.Forks > 0 {
		result.Detail = fmt.Sprintf("%d record revisions have several versions; each is kept as a fork (llewm fork list). %s", result.Forks, result.Detail)
	}
	if result.SupersessionForks > 0 {
		result.Detail = fmt.Sprintf("%d records were superseded more than once; every successor is kept as a fork (llewm fork list). %s", result.SupersessionForks, result.Detail)
	}
	if result.Collisions > 0 {
		result.Outcome = OutcomeCollision
		result.Detail = fmt.Sprintf("%d records have conflicting versions; the semantic import batch was rolled back. %s", result.Collisions, result.Detail)
	}
	if result.SemanticPending != "" {
		result.Outcome = OutcomeError
		result.Detail = "transport processed; " + result.SemanticPending
	}
	result.UnpushedCommits = unpushed(local, repo, branch)
	result.Publication, err = s.recordPublication(local, repo, identity.LineageID, captured.OID, branch, !offline && !deferred)
	if err != nil {
		return result, fmt.Errorf("record publication receipt: %w", err)
	}
	if !p.convert && guardErr == nil {
		s.rememberUnchanged(local, repo, before, result)
	}
	return result, nil
}
