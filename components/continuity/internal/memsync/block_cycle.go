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
	lock, err := lockFile(filepath.Join(s.Clone, ".git", "continuity-sync.lock"))
	if err != nil {
		return result, err
	}
	defer lock.Close()
	s.discovery = &blockTreeDiscovery{}
	before, guardErr := s.guard(local, repo)
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
		archive, err := recordsync.OpenArchive(filepath.Join(config.StateDir(projectRoot), "history"), identity.LineageID)
		if err != nil {
			return result, err
		}
		s.Archive = &archive
	}
	if s.Archive.Lineage != identity.LineageID {
		return result, fmt.Errorf("matching history archive is required")
	}
	if cache, err := openBlockTreeCache(s.Archive.Directory); err == nil {
		defer cache.Close()
		s.discovery.cache = cache
	}
	// Rebuild receipt membership and provenance from already tracked local
	// history before classifying operational records as locally originated or
	// planning publication. This also repairs damaged entries that are present.
	tracked, err := s.trackedBlocks(local, repo, identity.LineageID)
	if err != nil {
		return result, err
	}
	index := recordsync.Index{DB: s.DB, ProjectID: s.ProjectID, Lineage: identity.LineageID, Now: s.Now}
	for _, block := range tracked {
		if _, err := index.ObservePublication(local, *s.Archive, s.Endpoint(), block); err != nil {
			return result, err
		}
	}
	old, err := s.trackedV3Snapshot(local, repo, identity.LineageID)
	if err != nil {
		return result, err
	}
	if len(old.Objects) != 0 && !p.convert {
		return result, fmt.Errorf("format-3 observations require explicit conversion: stop old writers, then run `llewm sync convert --writers-stopped`")
	}
	for _, object := range old.Objects {
		if _, err := s.Archive.Put(object, s.Endpoint()); err != nil {
			return result, err
		}
	}
	if _, err := s.Archive.CaptureProject(local, s.DB, s.ProjectID); err != nil {
		return result, err
	}
	paths, count, snapshot, err := s.publishBlocks(local, identity.LineageID, tracked)
	result.ExportedRecords = count
	if err != nil {
		return result, err
	}
	if err := commitBlocks(local, repo, paths, fmt.Sprintf("continuity: %s +%d records", identity.InstanceID, count)); err != nil {
		return result, err
	}
	head, err := repo.run(local, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return result, err
	}
	branch := head
	importBlocks := func() error {
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
		if err := importCurrent(); err != nil {
			return err
		}
		// Convert older format-2 records first; this operation refuses them in
		// an ordinary sync. Both conversion steps require stopped old writers.
		var legacy Result
		if err := retireFormerFormat(local, repo, s.Clone, *identity, p.convert, &legacy); err != nil {
			return err
		}
		result.ExportedRecords += legacy.ExportedRecords
		snapshot, err := s.trackedV3Snapshot(local, repo, identity.LineageID)
		if err != nil {
			return err
		}
		if len(snapshot.Objects) != 0 && !p.convert {
			return fmt.Errorf("format-3 observations require explicit conversion: stop old writers, then run `llewm sync convert --writers-stopped`")
		}
		if p.convert {
			converted, err := s.convertTrackedV3(local, repo, *identity)
			if err != nil {
				return err
			}
			result.ExportedRecords += converted
			result.Converted = true
			if converted != 0 {
				return importCurrent()
			}
		}
		return nil
	}
	offline, deferred, imported := false, false, false
	for attempt := 0; ; attempt++ {
		if err := stopped(ctx, "fetching"); err != nil {
			return result, err
		}
		if attempt == 0 && !prefetched {
			if err := probe(ctx, repo, s.Remote, p.deadline); errors.Is(err, errBudget) {
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
			if err := s.fetch(ctx, repo, p); errors.Is(err, errBudget) {
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
			if _, err := repo.run(local, "rev-parse", "--verify", "--quiet", "HEAD"); err != nil {
				if _, err := repo.run(local, "checkout", "--quiet", "-B", branch, "origin/"+branch); err != nil {
					return result, err
				}
			} else if _, err := repo.run(local, "rebase", "--quiet", "origin/"+branch); err != nil {
				_, _ = repo.run(local, "rebase", "--abort")
				return result, err
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
		return result, fmt.Errorf("format-4 conversion needs the remote, and it did not finish: %s", result.Detail)
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
		result.Detail = "history observations converted to format 4; " + result.Detail
	}
	if result.Forks > 0 {
		result.Detail = fmt.Sprintf("%d record revisions have several versions; each is kept as a fork (llewm fork list). %s", result.Forks, result.Detail)
	}
	if result.SupersessionForks > 0 {
		result.Detail = fmt.Sprintf("%d records were superseded more than once; every successor is kept as a fork (llewm fork list). %s", result.SupersessionForks, result.Detail)
	}
	if result.Collisions > 0 {
		result.Outcome = OutcomeCollision
		result.Detail = fmt.Sprintf("%d records have conflicting versions; other records were imported. %s", result.Collisions, result.Detail)
	}
	if result.SemanticPending != "" {
		result.Outcome = OutcomeError
		result.Detail = "transport processed; " + result.SemanticPending
	}
	result.UnpushedCommits = unpushed(local, repo, branch)
	if !p.convert && guardErr == nil {
		s.rememberUnchanged(local, repo, before, result)
	}
	return result, nil
}
