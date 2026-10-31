package recordsync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"

	"github.com/llewm/llewm/components/continuity/internal/portable"
)

// archiveMigrationSource is a private, verified snapshot of the authoritative
// legacy files, never a reconstruction from the disposable history index.
// The migration coordinator must stop writers before collecting it, retain a
// verified backup, and fence the old route before activating staged storage.
type archiveMigrationSource struct {
	objects   []portable.EncodedEnvelope
	origins   map[string][]string
	inventory archiveInventory
}

// collectArchiveMigration does not create a missing source or infer provenance.
// Two full reads detect unstable contents without relying on platform-specific
// inode timestamps. They supplement, rather than replace, writer coordination.
func collectArchiveMigration(ctx context.Context, directory, lineage string) (archiveMigrationSource, error) {
	first, err := readArchiveMigration(ctx, directory, lineage)
	if err != nil {
		return archiveMigrationSource{}, err
	}
	second, err := readArchiveMigration(ctx, directory, lineage)
	if err != nil {
		return archiveMigrationSource{}, err
	}
	if first.inventory != second.inventory {
		return archiveMigrationSource{}, fmt.Errorf("legacy archive changed during migration inventory")
	}
	return second, nil
}

func readArchiveMigration(ctx context.Context, directory, lineage string) (archiveMigrationSource, error) {
	var source archiveMigrationSource
	root, err := OpenRoot(directory)
	if err != nil {
		return source, err
	}
	defer root.Close()
	marker, err := ReadFile(root, "lineage", 128)
	if err != nil || !bytes.Equal(marker, []byte(lineage+"\n")) {
		return source, fmt.Errorf("legacy archive lineage marker differs or is missing")
	}
	scan, err := Scan(ctx, root, lineage)
	if err != nil {
		return source, err
	}
	if scan.Pending != 0 || scan.Invalid != 0 {
		return source, fmt.Errorf("legacy archive contains incomplete or invalid records")
	}
	source.objects = scan.Objects
	source.origins = make(map[string][]string, len(scan.Objects))
	allowed := map[string]bool{".": true, "lineage": false}
	allow := func(path string) {
		allowed[filepath.ToSlash(path)] = false
		for parent := filepath.Dir(path); parent != "."; parent = filepath.Dir(parent) {
			allowed[filepath.ToSlash(parent)] = true
		}
	}
	for _, object := range source.objects {
		if err := ctx.Err(); err != nil {
			return source, err
		}
		path := portable.MemoryRecordPath(object.Envelope)
		body, err := ReadFile(root, path, MaxObjectBytes+1)
		if err != nil || !bytes.Equal(body, portable.MemoryFileBytes(object)) {
			return source, fmt.Errorf("legacy envelope bytes changed or have noncanonical framing")
		}
		allow(path)
		hash := object.Envelope.EnvelopeSHA256
		originPath := filepath.Join("origins", hash)
		info, err := root.Lstat(originPath)
		if err != nil || !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
			return source, fmt.Errorf("legacy envelope provenance is missing or invalid")
		}
		directory, err := openRead(root, originPath)
		if err != nil {
			return source, err
		}
		// Bound even the directory enumeration before allocating its entries.
		entries, readErr := directory.ReadDir(MaxScanEntries + 1)
		directory.Close()
		if readErr != nil || len(entries) == 0 || len(entries) > MaxScanEntries {
			return source, fmt.Errorf("legacy envelope provenance is incomplete or exceeds bounds")
		}
		for _, entry := range entries {
			if !hex64.MatchString(entry.Name()) {
				return source, fmt.Errorf("legacy provenance name is invalid")
			}
			path := filepath.Join(originPath, entry.Name())
			body, err := ReadFile(root, path, len("observed\n"))
			if err != nil || !bytes.Equal(body, []byte("observed\n")) {
				return source, fmt.Errorf("legacy provenance marker is invalid")
			}
			source.origins[hash] = append(source.origins[hash], entry.Name())
			source.inventory.Origins++
			if source.inventory.Origins > MaxScanEntries {
				return source, fmt.Errorf("legacy provenance exceeds bounds")
			}
			allow(path)
		}
		sort.Strings(source.origins[hash])
		source.inventory.Records++
		source.inventory.Bytes += int64(len(object.Bytes) + 1)
		if source.inventory.Bytes > portable.MaxRecordsBytes {
			return source, fmt.Errorf("legacy archive exceeds durable storage bounds")
		}
	}
	// Check the entire authoritative namespace, including orphan origins and
	// abandoned staging files. Endpoint bookkeeping is explicitly disposable.
	count := 0
	err = fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		count++
		if count > MaxScanEntries {
			return fmt.Errorf("legacy archive namespace exceeds bounds")
		}
		if path == "status" && entry.IsDir() {
			return fs.SkipDir
		}
		isDir, ok := allowed[path]
		if !ok || entry.IsDir() != isDir || entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("legacy archive contains an unaccounted entry")
		}
		return nil
	})
	if err != nil {
		return source, err
	}
	var hashes []string
	for hash := range source.origins {
		hashes = append(hashes, hash)
	}
	sort.Strings(hashes)
	bodies, origins := sha256.New(), sha256.New()
	for _, hash := range hashes {
		bodies.Write([]byte(hash + "\x00"))
		for _, origin := range source.origins[hash] {
			origins.Write([]byte(hash + origin + "\x00"))
		}
	}
	source.inventory.EnvelopesSHA256 = hex.EncodeToString(bodies.Sum(nil))
	source.inventory.OriginsSHA256 = hex.EncodeToString(origins.Sum(nil))
	return source, nil
}

// stageWithin is idempotent after interruption and deliberately never activates
// the archive. A contaminated or incomplete staging store fails its full audit;
// it is not silently cleared or repaired. The caller rolls back on any error.
func (source archiveMigrationSource) stageWithin(ctx context.Context, tx *sql.Tx, store archiveStore) error {
	if err := store.initializeWithin(ctx, tx); err != nil {
		return err
	}
	for _, object := range source.objects {
		for _, origin := range source.origins[object.Envelope.EnvelopeSHA256] {
			if _, err := store.putWithin(ctx, tx, "staging", origin, []portable.EncodedEnvelope{object}); err != nil {
				return err
			}
		}
	}
	audit, err := store.auditWithin(ctx, tx)
	if err != nil {
		return err
	}
	if audit.Inventory != source.inventory {
		return fmt.Errorf("staged archive differs from verified legacy source")
	}
	return nil
}
