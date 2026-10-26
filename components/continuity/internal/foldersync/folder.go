// Package foldersync transports immutable curated records through an explicitly
// configured local directory. A separate application may replicate that directory.
package foldersync

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/recordsync"
	"github.com/llewm/llewm/components/continuity/internal/redact"
)

const MarkerFile = ".llewm-history.json"

type Marker struct {
	Format  string `json:"format"`
	Version int    `json:"version"`
	StoreID string `json:"store_id"`
}
type Endpoint struct {
	Name      string `toml:"name" json:"name"`
	Directory string `toml:"directory" json:"directory"`
	StoreID   string `toml:"store_id" json:"store_id"`
	LineageID string `toml:"lineage_id" json:"lineage_id"`
	Relay     bool   `toml:"relay,omitempty" json:"relay"`
}

func (e Endpoint) ID() string { return "folder:" + e.StoreID }

// Create initializes a new endpoint marker only on explicit setup, never during
// sync. Its identity is pinned in each device's local configuration.
func Create(directory string) (Marker, error) {
	var marker Marker
	root, err := recordsync.OpenRoot(directory)
	if err != nil {
		return marker, err
	}
	defer root.Close()
	if _, err := root.Lstat(MarkerFile); !errors.Is(err, os.ErrNotExist) {
		return marker, fmt.Errorf("folder marker already exists or is unavailable")
	}
	id, err := recordsync.NewID()
	if err != nil {
		return marker, err
	}
	marker = Marker{"llewm-history-container", 2, id}
	body, err := json.Marshal(marker)
	if err != nil {
		return marker, err
	}
	_, err = recordsync.WriteFile(root, MarkerFile, body, false)
	return marker, err
}

// Pin is the shared setup path for project and portfolio destinations. Creation
// is explicit; joining requires confirmation of the exact inspected store ID.
func Pin(directory, storeID string, create bool) (Marker, error) {
	if create {
		if storeID != "" {
			return Marker{}, fmt.Errorf("--create does not accept --store-id")
		}
		return Create(directory)
	}
	marker, err := Inspect(directory)
	if err == nil && marker.StoreID != storeID {
		return Marker{}, fmt.Errorf("explicit confirmation required: --store-id %s", marker.StoreID)
	}
	return marker, err
}

func Inspect(directory string) (Marker, error) {
	root, err := recordsync.OpenRoot(directory)
	if err != nil {
		return Marker{}, err
	}
	defer root.Close()
	return readMarker(root)
}
func readMarker(root *os.Root) (Marker, error) {
	var marker Marker
	body, err := recordsync.ReadFile(root, MarkerFile, 4096)
	if err != nil {
		return marker, err
	}
	if err := json.Unmarshal(body, &marker); err != nil {
		return marker, err
	}
	canonical, _ := json.Marshal(marker)
	if string(canonical) != string(body) || marker.Format != "llewm-history-container" || marker.Version != 2 || !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(marker.StoreID) {
		return marker, fmt.Errorf("unsupported or invalid folder container marker")
	}
	return marker, nil
}

type Result struct {
	Endpoint         string `json:"endpoint"`
	Outcome          string `json:"outcome"`
	PublishedRecords int    `json:"published_records"`
	ObservedRecords  int    `json:"observed_records"`
	PendingFiles     int    `json:"pending_files"`
	InvalidFiles     int    `json:"invalid_files"`
	recordsync.ImportResult
	At     time.Time `json:"at"`
	Detail string    `json:"detail"`
	// Shared directory publication is never evidence of another device's receipt.
	PeerReceipt string `json:"peer_receipt"`
}

type Syncer struct {
	DB        *sql.DB
	ProjectID string
	Archive   recordsync.Archive
	Endpoint  Endpoint
}

func (s Syncer) Sync(ctx context.Context) (result Result, err error) {
	result = Result{Endpoint: s.Endpoint.Name, Outcome: "error", At: time.Now().UTC(), PeerReceipt: "unknown"}
	defer func() {
		if err != nil {
			result.Detail = redact.String(err.Error())
		}
		if saveErr := s.Archive.SaveStatus(s.Endpoint.ID(), result); err == nil && saveErr != nil {
			err = saveErr
		}
	}()
	root, err := recordsync.OpenRoot(s.Endpoint.Directory)
	if err != nil {
		result.Outcome = "unavailable"
		return result, err
	}
	defer root.Close()
	marker, err := readMarker(root)
	if err != nil {
		result.Outcome = "unavailable"
		return result, err
	}
	if marker.StoreID != s.Endpoint.StoreID || s.Endpoint.LineageID != s.Archive.Lineage || !regexp.MustCompile(`^lineage:[0-9a-f]{32}$`).MatchString(s.Endpoint.LineageID) {
		return result, fmt.Errorf("folder identity/lineage differs from explicit configuration")
	}
	identity, err := (portable.Service{DB: s.DB}).Get(ctx, s.ProjectID)
	if err != nil {
		return result, err
	}
	if identity == nil || identity.LineageID != s.Endpoint.LineageID {
		return result, fmt.Errorf("folder route differs from selected project's lineage")
	}
	// Read first, retaining every structurally verified envelope before semantic
	// import. A corrupt peer file never replaces a locally verified archive copy.
	scan, err := recordsync.Scan(ctx, root, s.Archive.Lineage)
	if err != nil {
		return result, err
	}
	result.PendingFiles, result.InvalidFiles = scan.Pending, scan.Invalid
	for _, e := range scan.Objects {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		added, err := s.Archive.Put(e, s.Endpoint.ID())
		if err != nil {
			return result, err
		}
		if added {
			result.ObservedRecords++
		}
	}
	// Publish retained local/same-endpoint history, or all authorized relay
	// objects. Deletion by the external tool never becomes semantic deletion.
	outgoing, err := s.Archive.Objects(ctx, s.Endpoint.ID(), s.Endpoint.Relay)
	if err != nil {
		return result, err
	}
	for _, e := range outgoing {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		// Recheck the marker between publication steps. Never recreate a missing
		// folder/marker or silently accept a replacement mount at the same pathname.
		live, liveErr := Inspect(s.Endpoint.Directory)
		if liveErr != nil || live != marker {
			return result, fmt.Errorf("configured folder path became unavailable or changed")
		}
		current, err := readMarker(root)
		if err != nil || current != marker {
			return result, fmt.Errorf("folder marker became unavailable or changed")
		}
		added, err := recordsync.WriteFile(root, portable.MemoryRecordPath(e.Envelope), portable.MemoryFileBytes(e), false)
		if err != nil {
			return result, err
		}
		if added {
			result.PublishedRecords++
		}
	}
	// Import from the durable archive, including records that arrived before a
	// missing dependency or before this device supported their schema.
	all, err := s.Archive.Objects(ctx, "", true)
	if err != nil {
		return result, err
	}
	result.ImportResult, err = (recordsync.Importer{DB: s.DB, ProjectID: s.ProjectID}).Import(ctx, all)
	if err != nil {
		result.Outcome = "pending"
		return result, err
	}
	result.Outcome = "published_locally"
	result.Detail = "validated locally; external delivery and peer receipt are unconfirmed"
	if result.PendingFiles+result.InvalidFiles+result.DeferredRecords+result.Collisions > 0 {
		result.Outcome = "pending"
	}
	return result, nil
}

// Separate refuses overlap with any protected local tree (database/state,
// archive, .git, or a managed memory clone). Resolve existing ancestor symlinks
// before comparing; endpoint setup never changes ignore rules or sync providers.
func Separate(directory string, protected ...string) error {
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return err
	}
	absolute, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		return err
	}
	for _, path := range protected {
		if path == "" {
			continue
		}
		p, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			p = resolved
		}
		for _, pair := range [][2]string{{absolute, p}, {p, absolute}} {
			rel, err := filepath.Rel(pair[0], pair[1])
			if err == nil && (rel == "." || filepath.IsLocal(rel)) {
				return fmt.Errorf("shared folder overlaps protected local storage")
			}
		}
	}
	return nil
}
