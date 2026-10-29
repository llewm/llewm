package memsync

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/recordsync"
)

// trackedV3Snapshot reads only paths tracked by Git. Every path and envelope
// is checked before the resulting snapshot can be used for conversion.
func (s Syncer) trackedV3Snapshot(ctx context.Context, repo git, lineage string) (recordsync.ScanResult, error) {
	var result recordsync.ScanResult
	listing, err := repo.run(ctx, "ls-files", "--", portable.MemoryLineageDir(lineage))
	if err != nil {
		return result, err
	}
	root, err := recordsync.OpenRoot(s.Clone)
	if err != nil {
		return result, err
	}
	defer root.Close()
	total := 0
	for _, path := range strings.Fields(listing) {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		object, err := recordsync.ReadObject(root, filepath.FromSlash(path), lineage)
		if err != nil {
			return result, fmt.Errorf("invalid tracked v3 record: %w", err)
		}
		total += len(object.Bytes)
		if total > portable.MaxRecordsBytes || len(result.Objects) >= portable.MaxRecordCount {
			return result, fmt.Errorf("tracked v3 inventory exceeds bounds")
		}
		result.Objects = append(result.Objects, object)
	}
	return result, nil
}

// trackedBlocks never discovers untracked files in the working tree.
func (s Syncer) trackedBlocks(ctx context.Context, repo git, lineage string) ([]recordsync.Block, error) {
	return s.readTrackedBlocks(ctx, repo, lineage, true)
}

// readTrackedBlocks can bypass the derived index when reconstructing it.
func (s Syncer) readTrackedBlocks(ctx context.Context, repo git, lineage string, indexed bool) ([]recordsync.Block, error) {
	directory, err := recordsync.BlockDirectory(lineage)
	if err != nil {
		return nil, err
	}
	paths, err := s.trackedBlockPaths(ctx, repo, directory)
	if err != nil {
		return nil, err
	}
	root, err := recordsync.OpenRoot(s.Clone)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	var blocks []recordsync.Block
	index := recordsync.Index{DB: s.DB, ProjectID: s.ProjectID, Lineage: lineage, Now: s.Now}
	count, total := 0, 0
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var block recordsync.Block
		if indexed {
			block, err = index.ReadBlock(ctx, root, filepath.FromSlash(path))
		} else {
			block, err = recordsync.ReadBlock(root, lineage, filepath.FromSlash(path))
		}
		if err != nil {
			return nil, fmt.Errorf("invalid tracked history block: %w", err)
		}
		count += len(block.Objects)
		total += len(block.Bytes)
		if count > portable.MaxRecordCount || total > portable.MaxRecordsBytes {
			return nil, fmt.Errorf("tracked block inventory exceeds bounds")
		}
		blocks = append(blocks, block)
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
	lock, err := lockFile(filepath.Join(s.Clone, ".git", "continuity-sync.lock"))
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
	var old Result
	if err := retireFormerFormat(ctx, repo, s.Clone, *identity, false, &old); err != nil {
		return err
	}
	legacy, err := s.trackedV3Snapshot(ctx, repo, identity.LineageID)
	if err != nil {
		return err
	}
	if len(legacy.Objects) != 0 {
		return fmt.Errorf("format-3 observations require explicit conversion before inventory rebuild")
	}
	blocks, err := s.readTrackedBlocks(ctx, repo, identity.LineageID, false)
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
	return (recordsync.Index{DB: s.DB, ProjectID: s.ProjectID, Lineage: identity.LineageID, Now: s.Now}).Rebuild(ctx, inventories)
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

func (s Syncer) publishBlocks(ctx context.Context, lineage string, known []recordsync.Block) ([]string, int, []recordsync.Block, error) {
	if s.Archive == nil || s.Archive.Lineage != lineage {
		return nil, 0, nil, fmt.Errorf("matching history archive is required for block publication")
	}
	index := recordsync.Index{DB: s.DB, ProjectID: s.ProjectID, Lineage: lineage, Now: s.Now}
	blocks, inventory, err := index.PlanPublicationWithInventory(ctx, *s.Archive, s.Endpoint(), s.Relay, known)
	if err != nil {
		return nil, 0, nil, err
	}
	root, err := recordsync.OpenRoot(s.Clone)
	if err != nil {
		return nil, 0, nil, err
	}
	defer root.Close()
	count := 0
	var paths []string
	directory, err := recordsync.BlockDirectory(lineage)
	if err != nil {
		return nil, 0, nil, err
	}
	for _, block := range blocks {
		if err := ctx.Err(); err != nil {
			return paths, count, nil, err
		}
		added, err := recordsync.PublishBlock(root, lineage, block)
		if err != nil {
			return paths, count, nil, err
		}
		paths = append(paths, filepath.ToSlash(filepath.Join(directory, block.Hash+".block")))
		if added {
			count += len(block.Objects)
		}
	}
	return paths, count, inventory, nil
}

// commitBlocks stages only explicitly validated block paths. Never add a whole
// directory: untracked working-tree files are not part of the transport.
func commitBlocks(ctx context.Context, repo git, paths []string, message string) error {
	if _, err := repo.run(ctx, "diff", "--cached", "--quiet"); err != nil {
		return fmt.Errorf("block publication requires a clean Git index")
	}
	for _, path := range paths {
		if _, err := repo.run(ctx, "add", "--", path); err != nil {
			return err
		}
	}
	if _, err := repo.run(ctx, "diff", "--cached", "--quiet"); err == nil {
		return nil
	} else if exitCode(err) != 1 {
		return err
	}
	_, err := repo.run(ctx, "commit", "--quiet", "-m", message)
	return err
}

func (s Syncer) importTrackedBlocks(ctx context.Context, repo git, lineage string, snapshot []recordsync.Block) (recordsync.ImportResult, error) {
	if s.Archive == nil || s.Archive.Lineage != lineage {
		return recordsync.ImportResult{}, fmt.Errorf("matching history archive is required for block import")
	}
	blocks, err := s.trackedBlocks(ctx, repo, lineage)
	if err != nil {
		return recordsync.ImportResult{}, err
	}
	index := recordsync.Index{DB: s.DB, ProjectID: s.ProjectID, Lineage: lineage, Now: s.Now}
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
		if _, err := index.ObservePublication(ctx, *s.Archive, s.Endpoint(), block); err != nil {
			return recordsync.ImportResult{}, err
		}
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

// convertTrackedV3 validates every tracked v3 path, stages complete blocks and
// only then removes the v3 paths in the same Git commit. The caller is
// responsible for fetching first and stopping all old writers before invoking
// conversion. Original bytes remain in Git history and the local archive.
func (s Syncer) convertTrackedV3(ctx context.Context, repo git, identity portable.Identity) (int, error) {
	if s.Archive == nil || s.Archive.Lineage != identity.LineageID {
		return 0, fmt.Errorf("matching history archive is required for conversion")
	}
	scan, err := s.trackedV3Snapshot(ctx, repo, identity.LineageID)
	if err != nil {
		return 0, err
	}
	blocks, err := recordsync.ConvertV3Snapshot(identity.LineageID, scan)
	if err != nil {
		return 0, err
	}
	if len(scan.Objects) == 0 {
		return 0, nil
	}
	if _, err := repo.run(ctx, "diff", "--cached", "--quiet"); err != nil {
		return 0, fmt.Errorf("conversion requires a clean Git index")
	}
	index := recordsync.Index{DB: s.DB, ProjectID: s.ProjectID, Lineage: identity.LineageID}
	root, err := recordsync.OpenRoot(s.Clone)
	if err != nil {
		return 0, err
	}
	defer root.Close()
	for _, block := range blocks {
		if _, err := index.ObservePublication(ctx, *s.Archive, s.Endpoint(), block); err != nil {
			return 0, err
		}
		if _, err := recordsync.PublishBlock(root, identity.LineageID, block); err != nil {
			return 0, err
		}
	}
	directory, err := recordsync.BlockDirectory(identity.LineageID)
	if err != nil {
		return 0, err
	}
	for _, block := range blocks {
		path := filepath.ToSlash(filepath.Join(directory, block.Hash+".block"))
		if _, err := repo.run(ctx, "add", "--", path); err != nil {
			return 0, err
		}
	}
	if _, err := repo.run(ctx, "rm", "-r", "-q", "--", portable.MemoryLineageDir(identity.LineageID)); err != nil {
		return 0, err
	}
	message := fmt.Sprintf("continuity: %s convert to format 4, %d observations", identity.InstanceID, len(scan.Objects))
	if _, err := repo.run(ctx, "commit", "--quiet", "-m", message); err != nil {
		return 0, err
	}
	return len(scan.Objects), nil
}
