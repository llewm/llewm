package memsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/llewm/llewm/components/continuity/internal/buildinfo"
	"github.com/llewm/llewm/components/continuity/internal/recordsync"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

type gitObservationReceipt struct {
	Version int                         `json:"version"`
	Route   string                      `json:"route"`
	Tree    string                      `json:"tree"`
	Stamp   recordsync.ObservationStamp `json:"stamp"`
}

func (s Syncer) observationRoute(lineage string) (key, route string, err error) {
	paths, err := StoragePaths(s.Clone)
	if err != nil {
		return "", "", err
	}
	body, _ := json.Marshal([]any{"git-observation-v1", recordsync.IndexVersion, buildinfo.Current().GitCommit, s.ProjectID, lineage, paths.Clone, s.Endpoint()})
	route = recordsync.Hash(string(body))
	return "git-observation:" + route, route, nil
}

// observeGitPublications reuses only an exact, archive/metadata-bound prior
// observation. The immutable tree delta verifies every new selected block;
// uncertainty reads the complete current transport and audits durable storage.
// The caller holds the clone lock through observation and subsequent publication.
func (s Syncer) observeGitPublications(ctx context.Context, repo git, lineage string, forceFull bool) ([]recordsync.Block, *gitObservationReceipt, error) {
	if s.Archive == nil || s.Archive.Lineage != lineage {
		return nil, nil, fmt.Errorf("matching archive required for Git observation")
	}
	key, route, err := s.observationRoute(lineage)
	if err != nil {
		return nil, nil, err
	}
	transport := s.objectReader(repo)
	tree, err := transport.headTree(ctx)
	if err != nil {
		return nil, nil, err
	}
	index := recordsync.Index{DB: s.DB, ProjectID: s.ProjectID, Lineage: lineage, Now: s.Now}
	current, trackingErr := index.ObservationStamp(ctx, *s.Archive)
	if trackingErr != nil && !errors.Is(trackingErr, recordsync.ErrObservationTracking) {
		return nil, nil, trackingErr
	}
	var previous gitObservationReceipt
	savedErr := s.Archive.Status(key, &previous)
	full := forceFull || buildinfo.Current().BuildDirty || trackingErr != nil || savedErr != nil || previous.Version != 1 || previous.Route != route || previous.Stamp != current || (previous.Tree != "" && !validGitOID(previous.Tree))
	before := ""
	if !full {
		before = previous.Tree
	} else {
		syncwork.RecordWork(ctx, "fallback_git_observation_full", 1)
	}
	for attempt := 0; attempt < 2; attempt++ {
		var blocks []recordsync.Block
		if tree != "" {
			blocks, err = transport.blockDelta(ctx, before, tree, lineage)
			if err != nil {
				return nil, nil, err
			}
		} else if before != "" {
			return nil, nil, fmt.Errorf("observed Git history became empty")
		}

		mutation, accepted, err := index.ObservePublicationBatch(ctx, *s.Archive, s.Endpoint(), previous.Stamp, full, blocks)
		if err != nil {
			return nil, nil, err
		}
		if !accepted {
			if full {
				return nil, nil, fmt.Errorf("complete Git observation was not accepted")
			}
			full, before = true, ""
			syncwork.RecordWork(ctx, "fallback_git_observation_race", 1)
			continue
		}
		stamp := mutation.After
		if stamp == (recordsync.ObservationStamp{}) || buildinfo.Current().BuildDirty {
			return blocks, nil, nil
		}
		if err := s.journalAfterObservation(ctx, repo, lineage, tree, mutation); err != nil {
			return nil, nil, err
		}
		next := gitObservationReceipt{Version: 1, Route: route, Tree: tree, Stamp: stamp}
		if next != previous {
			if err := s.Archive.SaveStatus(key, next); err != nil {
				return nil, nil, err
			}
		}
		if !full {
			syncwork.RecordWork(ctx, "git_observation_delta", 1)
		}
		return blocks, &next, nil
	}
	return nil, nil, fmt.Errorf("Git observation changed repeatedly")
}

func (s Syncer) advanceObservedInventory(lineage string, receipt *gitObservationReceipt, stamp recordsync.ObservationStamp) error {
	if receipt == nil || stamp == (recordsync.ObservationStamp{}) {
		return nil
	}
	key, route, err := s.observationRoute(lineage)
	if err != nil {
		return err
	}
	if receipt.Route != route || receipt.Stamp.ArchiveEpoch != stamp.ArchiveEpoch || receipt.Stamp.Schema != stamp.Schema {
		return fmt.Errorf("publication observation binding changed")
	}
	if receipt.Stamp == stamp {
		return nil
	}
	receipt.Stamp = stamp
	return s.Archive.SaveStatus(key, *receipt)
}
