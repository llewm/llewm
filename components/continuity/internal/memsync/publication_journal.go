package memsync

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/llewm/llewm/components/continuity/internal/recordsync"
)

func (s Syncer) journalPublicationScope(ctx context.Context, repo git, lineage string, observed recordsync.ObservationStamp, tree string) (recordsync.JournalPublicationScope, error) {
	_, route, err := s.observationRoute(lineage)
	if err != nil {
		return recordsync.JournalPublicationScope{}, err
	}
	body, _ := json.Marshal([]any{"git-publication-journal-v1", route, s.Relay})
	scope := recordsync.JournalPublicationScope{Endpoint: s.Endpoint(), Consumer: recordsync.Hash(string(body)), Relay: s.Relay, Observed: observed, Tree: tree}
	transport := s.objectReader(repo)
	directory, err := recordsync.BlockDirectory(lineage)
	if err != nil {
		return scope, err
	}
	var entries []blockTreeEntry
	loaded := false
	scope.HasBlock = func(ctx context.Context, hash string) (bool, error) {
		if len(hash) != 64 || !validGitOID(hash) {
			return false, fmt.Errorf("invalid publication block identity")
		}
		if !loaded {
			selected, err := transport.treeAt(ctx, tree, strings.Split(filepath.ToSlash(directory), "/")...)
			if err != nil {
				return false, err
			}
			if selected != "" {
				entries, err = transport.trees.read(ctx, repo, selected)
				if err != nil {
					return false, err
				}
			}
			loaded = true
		}
		name := hash + ".block"
		position := sort.Search(len(entries), func(i int) bool { return entries[i].name >= name })
		if position == len(entries) || entries[position].name != name {
			return false, nil
		}
		entry := entries[position]
		if entry.mode != "100644" || entry.kind != "blob" {
			return false, fmt.Errorf("invalid committed publication block")
		}
		return true, nil
	}
	scope.AppendOnly = func(ctx context.Context, before string) (bool, error) {
		if before == tree {
			return true, nil
		}
		if tree == "" {
			return before == "", nil
		}
		_, err := transport.blockDelta(ctx, before, tree, lineage)
		return err == nil, err
	}
	return scope, nil
}

func (s Syncer) journalAfterObservation(ctx context.Context, repo git, lineage, tree string, mutation recordsync.ObservationMutation) error {
	scope, err := s.journalPublicationScope(ctx, repo, lineage, mutation.After, tree)
	if err != nil {
		return err
	}
	return (recordsync.Index{DB: s.DB, ProjectID: s.ProjectID, Lineage: lineage, Now: s.Now}).AdvancePublicationObservation(ctx, *s.Archive, scope, mutation)
}
