package memsync

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"

	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/recordsync"
)

// trackedV3Snapshot reads only paths tracked by Git. Every path and envelope
// is checked before the resulting snapshot can be used for conversion.
func (s Syncer) trackedV3Snapshot(ctx context.Context, repo git, lineage string) (recordsync.ScanResult, error) {
	transport := s.objectReader(repo)
	tree, err := transport.headTree(ctx)
	if err != nil {
		return recordsync.ScanResult{}, err
	}
	return transport.legacySnapshot(ctx, tree, lineage, false)
}

// trackedBlocks reads committed objects; loose files cannot enter the inventory.
func (s Syncer) trackedBlocks(ctx context.Context, repo git, lineage string) ([]recordsync.Block, error) {
	index := recordsync.Index{DB: s.DB, ProjectID: s.ProjectID, Lineage: lineage, Now: s.Now}
	return s.readTrackedBlocks(ctx, repo, lineage, false, func(hash string, body []byte) (recordsync.Block, error) {
		return index.ReadBlockBytes(ctx, hash, body)
	})
}

// trackedPublications records provenance as each verified block is read,
// avoiding a second reconstruction of the same inventory for observation.
func (s Syncer) trackedPublications(ctx context.Context, repo git, lineage string) ([]recordsync.Block, error) {
	if s.Archive == nil || s.Archive.Lineage != lineage {
		return nil, fmt.Errorf("matching history archive is required for block observation")
	}
	index := recordsync.Index{DB: s.DB, ProjectID: s.ProjectID, Lineage: lineage, Now: s.Now}
	return s.readTrackedBlocks(ctx, repo, lineage, true, func(hash string, body []byte) (recordsync.Block, error) {
		return index.ReadPublicationBytes(ctx, hash, body, *s.Archive, s.Endpoint())
	})
}

// readTrackedBlocks can bypass the derived index when reconstructing it.
func (s Syncer) readTrackedBlocks(ctx context.Context, repo git, lineage string, converting bool, read func(string, []byte) (recordsync.Block, error)) ([]recordsync.Block, error) {
	transport := s.objectReader(repo)
	tree, err := transport.headTree(ctx)
	if err != nil || tree == "" {
		return nil, err
	}
	blocks, err := transport.blockDeltaForConversion(ctx, "", tree, lineage, converting)
	if err != nil {
		return nil, err
	}
	for i, block := range blocks {
		blocks[i], err = read(block.Hash, block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("invalid tracked history block: %w", err)
		}
	}
	return blocks, nil
}

// RebuildInventory holds the clone lock from the Git snapshot through the
// atomic index replacement. The callback must validate every other configured
// endpoint independently and must not acquire the Git clone lock.
func (s Syncer) RebuildInventory(ctx context.Context, other func(context.Context) (map[string][]recordsync.Block, error)) error {
	if s.Archive == nil {
		return fmt.Errorf("matching history archive is required")
	}
	repo, err := s.repository(ctx)
	if err != nil {
		return err
	}
	lock, err := lockFile(filepath.Join(s.Clone, "continuity-sync.lock"))
	if err != nil {
		return err
	}
	defer lock.Close()
	identity, err := (portable.Service{DB: s.DB}).Get(ctx, s.ProjectID)
	if err != nil {
		return err
	}
	if identity == nil || identity.LineageID != s.Archive.Lineage {
		return fmt.Errorf("Git route differs from selected project's lineage")
	}
	blocks, err := s.readTrackedBlocks(ctx, repo, identity.LineageID, false, func(hash string, body []byte) (recordsync.Block, error) {
		return recordsync.DecodeBlock(identity.LineageID, hash, body)
	})
	if err != nil {
		return err
	}
	inventories, err := other(ctx)
	if err != nil {
		return err
	}
	if inventories == nil {
		inventories = make(map[string][]recordsync.Block)
	}
	if _, exists := inventories[s.Endpoint()]; exists {
		return fmt.Errorf("duplicate Git inventory endpoint")
	}
	inventories[s.Endpoint()] = blocks
	if err := (recordsync.Index{DB: s.DB, ProjectID: s.ProjectID, Lineage: identity.LineageID, Now: s.Now}).Rebuild(ctx, inventories); err != nil {
		return err
	}
	_, _, err = s.observeGitPublications(ctx, repo, identity.LineageID, true)
	return err
}

// trackedBlockObservations resolves exact envelope hashes from tracked blocks;
// neither untracked blocks nor records from a different lineage qualify.
func (s Syncer) trackedBlockObservations(ctx context.Context, repo git, lineage string, hashes ...string) (map[string][]byte, error) {
	blocks, err := s.trackedBlocks(ctx, repo, lineage)
	if err != nil {
		return nil, err
	}
	requested := make(map[string]bool, len(hashes))
	for _, hash := range hashes {
		requested[hash] = true
	}
	bodies := make(map[string][]byte, len(requested))
	for _, block := range blocks {
		for _, object := range block.Objects {
			if requested[object.Envelope.EnvelopeSHA256] {
				bodies[object.Envelope.EnvelopeSHA256] = append([]byte(nil), object.Bytes...)
			}
		}
	}
	if len(bodies) != len(requested) {
		return nil, fmt.Errorf("all requested observations must already be tracked in this lineage")
	}
	return bodies, nil
}

func (s Syncer) publishBlocks(ctx context.Context, repo git, lineage string, observation ...*gitObservationReceipt) ([]recordsync.Block, []recordsync.Block, error) {
	if s.Archive == nil || s.Archive.Lineage != lineage {
		return nil, nil, fmt.Errorf("matching history archive is required for block publication")
	}
	index := recordsync.Index{DB: s.DB, ProjectID: s.ProjectID, Lineage: lineage, Now: s.Now}
	if len(observation) == 0 || observation[0] == nil {
		return index.PlanPublicationWithInventory(ctx, *s.Archive, s.Endpoint(), s.Relay)
	}
	scope, err := s.journalPublicationScope(ctx, repo, lineage, observation[0].Stamp, observation[0].Tree)
	if err != nil {
		return nil, nil, err
	}
	plan, err := index.PlanJournalPublication(ctx, *s.Archive, scope, false)
	if err != nil {
		return nil, nil, err
	}
	if err := s.advanceObservedInventory(lineage, observation[0], plan.Observed); err != nil {
		return nil, nil, err
	}
	return plan.Blocks, plan.Snapshot, nil
}

// commitBlocks publishes only exact validated objects through an attached
// branch's compare-and-swap. Loose files and indexes cannot enter the commit.
func commitBlocks(ctx context.Context, repo git, lineage string, blocks []recordsync.Block, message string, converting bool) (int, error) {
	transport := &objectTransport{repo: repo}
	branch, err := transport.currentBranch(ctx)
	if err != nil {
		return 0, err
	}
	before := ""
	width := 40
	if branch.OID != "" {
		commit, err := transport.readCommit(ctx, branch.OID)
		if err != nil {
			return 0, err
		}
		before, width = commit.Tree, len(branch.OID)
	} else {
		format, err := repo.run(ctx, "rev-parse", "--show-object-format")
		if err != nil {
			return 0, err
		}
		if format == "sha256" {
			width = 64
		} else if format != "sha1" {
			return 0, fmt.Errorf("unsupported memory object format")
		}
	}
	after, count, err := transport.appendBlocksForConversion(ctx, before, lineage, blocks, width, converting)
	if err != nil || before == after {
		return 0, err
	}
	var parents []string
	if branch.OID != "" {
		parents = []string{branch.OID}
	}
	commit, err := transport.writeCommit(ctx, after, parents, fmt.Sprintf("%s +%d records", message, count))
	if err != nil {
		return 0, err
	}
	if err := transport.advanceBranch(ctx, branch, commit); err != nil {
		return 0, err
	}
	return count, nil
}

func (s Syncer) importTrackedBlocks(ctx context.Context, repo git, lineage string, snapshot []recordsync.Block) (recordsync.ImportResult, error) {
	if s.Archive == nil || s.Archive.Lineage != lineage {
		return recordsync.ImportResult{}, fmt.Errorf("matching history archive is required for block import")
	}
	blocks, observed, err := s.observeGitPublications(ctx, repo, lineage, false)
	if err != nil {
		return recordsync.ImportResult{}, err
	}
	index := recordsync.Index{DB: s.DB, ProjectID: s.ProjectID, Lineage: lineage, Now: s.Now}
	if observed != nil {
		scope, err := s.journalPublicationScope(ctx, repo, lineage, observed.Stamp, observed.Tree)
		if err != nil {
			return recordsync.ImportResult{}, err
		}
		plan, err := index.PlanJournalPublication(ctx, *s.Archive, scope, true)
		if err != nil {
			return recordsync.ImportResult{}, err
		}
		if err := s.advanceObservedInventory(lineage, observed, plan.Observed); err != nil {
			return recordsync.ImportResult{}, err
		}
		return index.ImportBlocks(ctx, plan.Snapshot)
	}
	// The publication snapshot is complete at its scan time. Reuse it only
	// when every fetched record was already present with the same bytes;
	// otherwise rescan after observation to include newly received records.
	// Unrelated concurrent arrivals after the snapshot are picked up next cycle.
	known := make(map[string][]byte)
	for _, block := range snapshot {
		for _, object := range block.Objects {
			known[object.Envelope.EnvelopeSHA256] = object.Bytes
		}
	}
	complete := snapshot != nil
	for _, block := range blocks {
		for _, object := range block.Objects {
			if !bytes.Equal(known[object.Envelope.EnvelopeSHA256], object.Bytes) {
				complete = false
			}
		}
	}
	all := snapshot
	if !complete {
		all, err = s.Archive.AuthorizedBlocks(ctx, "", true)
		if err != nil {
			return recordsync.ImportResult{}, err
		}
	}
	return index.ImportBlocks(ctx, all)
}
