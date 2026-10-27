package foldersync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/recordsync"
)

// A manifest makes out-of-order folder replication detectable: seeing the
// manifest is not sufficient until every named block has arrived and validated.
type conversionManifest struct {
	Format  string   `json:"format"`
	Version int      `json:"version"`
	StoreID string   `json:"store_id"`
	Lineage string   `json:"lineage"`
	Blocks  []string `json:"blocks"`
}

func manifestPath(lineage string) (string, error) {
	dir, err := recordsync.BlockDirectory(lineage)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(dir), "conversion.json"), nil
}

// convertV3 prepares a folder lineage while old writers are stopped. V3 files
// are retained as a dormant recovery copy: an external replicator may deliver
// files and deletions in arbitrary order. The caller must explicitly coordinate
// all old writers and verify backups before any live conversion.
func (s Syncer) convertV3(ctx context.Context) error {
	root, err := recordsync.OpenRoot(s.Endpoint.Directory)
	if err != nil {
		return err
	}
	defer root.Close()
	marker, err := readMarker(root)
	if err != nil || marker.StoreID != s.Endpoint.StoreID || s.Endpoint.LineageID != s.Archive.Lineage {
		return fmt.Errorf("folder identity differs from configured conversion target")
	}
	identity, err := (portable.Service{DB: s.DB}).Get(ctx, s.ProjectID)
	if err != nil || identity == nil || identity.LineageID != s.Endpoint.LineageID {
		return fmt.Errorf("project lineage differs from conversion target")
	}
	path, err := manifestPath(s.Endpoint.LineageID)
	if err != nil {
		return err
	}
	if _, err := root.Lstat(path); err == nil {
		return fmt.Errorf("folder lineage already has a conversion manifest")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	scan, err := recordsync.Scan(ctx, root, s.Endpoint.LineageID)
	if err != nil {
		return err
	}
	blocks, err := recordsync.ConvertV3Snapshot(s.Endpoint.LineageID, scan)
	if err != nil {
		return err
	}
	index := recordsync.Index{DB: s.DB, ProjectID: s.ProjectID, Lineage: s.Endpoint.LineageID}
	manifest := conversionManifest{Format: "llewm-folder-block-conversion", Version: 1, StoreID: marker.StoreID, Lineage: s.Endpoint.LineageID, Blocks: []string{}}
	for _, block := range blocks {
		if err := index.ObservePublication(ctx, s.Archive, s.Endpoint.ID(), block); err != nil {
			return err
		}
		live, liveErr := Inspect(s.Endpoint.Directory)
		current, currentErr := readMarker(root)
		if liveErr != nil || live != marker || currentErr != nil || current != marker {
			return fmt.Errorf("folder target changed during conversion")
		}
		if _, err := recordsync.PublishBlock(root, s.Endpoint.LineageID, block); err != nil {
			return err
		}
		manifest.Blocks = append(manifest.Blocks, block.Hash)
	}
	sort.Strings(manifest.Blocks)
	body, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if len(body) > 1<<20 {
		return fmt.Errorf("folder conversion manifest exceeds bounds")
	}
	live, liveErr := Inspect(s.Endpoint.Directory)
	current, currentErr := readMarker(root)
	if liveErr != nil || live != marker || currentErr != nil || current != marker {
		return fmt.Errorf("folder target changed during conversion")
	}
	_, err = recordsync.WriteFile(root, path, body, false)
	return err
}

func verifyConversion(root *os.Root, marker Marker, lineage string, blocks []recordsync.Block) error {
	path, err := manifestPath(lineage)
	if err != nil {
		return err
	}
	body, err := recordsync.ReadFile(root, path, 1<<20)
	if err != nil {
		return fmt.Errorf("folder conversion manifest unavailable: %w", err)
	}
	var manifest conversionManifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		return fmt.Errorf("invalid folder conversion manifest")
	}
	canonical, err := json.Marshal(manifest)
	if err != nil || !bytes.Equal(canonical, body) || manifest.Format != "llewm-folder-block-conversion" || manifest.Version != 1 || manifest.StoreID != marker.StoreID || manifest.Lineage != lineage || manifest.Blocks == nil || len(manifest.Blocks) > portable.MaxRecordCount {
		return fmt.Errorf("invalid folder conversion manifest")
	}
	available := make(map[string]bool, len(blocks))
	for _, block := range blocks {
		available[block.Hash] = true
	}
	for i, hash := range manifest.Blocks {
		if i > 0 && manifest.Blocks[i-1] >= hash {
			return fmt.Errorf("invalid folder conversion block order")
		}
		if !available[hash] {
			return fmt.Errorf("folder conversion block missing or incomplete")
		}
	}
	return nil
}
