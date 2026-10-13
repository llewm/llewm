package portable

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/activity"
)

const redactionProfileDefinition = "llewm-portable-redaction/default/v1:project-root-and-secret-patterns"

const LocalDirectoryTransportKind = "local_directory"

var curatedV1Omissions = []Omission{
	{RecordFamily: "contract_index_entries", Reason: "local_index_of_repository_markdown"},
	{RecordFamily: "raw_events", Reason: "profile_excluded"},
	{RecordFamily: "raw_sessions", Reason: "profile_excluded"},
	{RecordFamily: "usage_samples", Reason: "profile_excluded"},
}

type BundleInspection struct {
	Directory     string         `json:"directory"`
	TransportKind string         `json:"transport_kind"`
	Manifest      Manifest       `json:"manifest"`
	ManifestBytes int            `json:"manifest_bytes"`
	RecordsBytes  int            `json:"records_bytes"`
	RecordCount   int            `json:"record_count"`
	RecordCounts  map[string]int `json:"record_counts"`
	BodiesLoaded  int            `json:"bodies_loaded"`
}

type ExportResult struct {
	Directory           string      `json:"directory"`
	Manifest            Manifest    `json:"manifest"`
	ManifestBytes       int         `json:"manifest_bytes"`
	RecordsBytes        int         `json:"records_bytes"`
	RecordCount         int         `json:"record_count"`
	MissingReferences   []Reference `json:"missing_references"`
	TransportKind       string      `json:"transport_kind"`
	ReviewRequired      bool        `json:"review_required"`
	GitActionsPerformed int         `json:"git_actions_performed"`
}

func DefaultRedactionProfile() RedactionProfile {
	sum := sha256.Sum256([]byte(redactionProfileDefinition))
	return RedactionProfile{ID: "default", Version: 1, SHA256: hex.EncodeToString(sum[:])}
}

// ExportOperationalDirectory takes one operational read snapshot and publishes a
// complete bundle through a same-parent rename. Existing targets are never replaced.
func ExportOperationalDirectory(ctx context.Context, db *sql.DB, projectID, directory string, createdAt time.Time) (ExportResult, error) {
	loaded, err := LoadOperationalRecords(ctx, db, projectID)
	if err != nil {
		return ExportResult{}, err
	}
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	bundle, err := BuildBundle(BundleSpec{
		ExportID: "export:" + activity.NewID(), LineageID: loaded.Identity.LineageID,
		SourceInstanceID: loaded.Identity.InstanceID, CreatedAt: createdAt,
		SourceDatabaseSchema: loaded.DatabaseSchema, SelectionProfile: "curated-v1",
		RedactionProfile: DefaultRedactionProfile(), Omissions: curatedV1Omissions,
	}, loaded.Records)
	if err != nil {
		return ExportResult{}, err
	}
	if err := PublishBundleDirectory(directory, bundle); err != nil {
		return ExportResult{}, err
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return ExportResult{}, err
	}
	return ExportResult{Directory: absolute, Manifest: bundle.Manifest, ManifestBytes: len(bundle.ManifestBytes), RecordsBytes: len(bundle.RecordsBytes), RecordCount: len(bundle.Records), MissingReferences: loaded.MissingReferences, TransportKind: LocalDirectoryTransportKind}, nil
}

func PublishBundleDirectory(directory string, bundle Bundle) error {
	if directory == "" {
		return fmt.Errorf("bundle directory is required")
	}
	if _, err := ValidateBundle(bundle.ManifestBytes, bundle.RecordsBytes); err != nil {
		return fmt.Errorf("validate bundle before publication: %w", err)
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return err
	}
	parent := filepath.Dir(absolute)
	parentInfo, err := os.Lstat(parent)
	if err != nil {
		return fmt.Errorf("bundle parent: %w", err)
	}
	if !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("bundle parent must be a real directory")
	}
	if _, err := os.Lstat(absolute); err == nil {
		return fmt.Errorf("bundle directory already exists")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect bundle target: %w", err)
	}
	temporary, err := os.MkdirTemp(parent, ".llewm-export-*")
	if err != nil {
		return err
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(temporary)
		}
	}()
	if err := os.Chmod(temporary, 0o700); err != nil {
		return err
	}
	for _, file := range []struct {
		name string
		body []byte
	}{{"manifest.json", bundle.ManifestBytes}, {"records.jsonl", bundle.RecordsBytes}} {
		path := filepath.Join(temporary, file.name)
		handle, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		if _, err = handle.Write(file.body); err == nil {
			err = handle.Sync()
		}
		closeErr := handle.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	manifest, records, err := ReadBundleDirectory(temporary)
	if err != nil {
		return err
	}
	if _, err := ValidateBundle(manifest, records); err != nil {
		return fmt.Errorf("verify published bundle: %w", err)
	}
	if err := syncDirectory(temporary); err != nil {
		return err
	}
	if err := renameNoReplace(temporary, absolute); err != nil {
		return fmt.Errorf("publish bundle directory: %w", err)
	}
	published = true
	return syncDirectory(parent)
}

func ReadBundleDirectory(directory string) ([]byte, []byte, error) {
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return nil, nil, err
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return nil, nil, fmt.Errorf("inspect bundle directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, nil, fmt.Errorf("bundle path must be a real directory")
	}
	transportKind, err := bundleDirectoryTransportKind(absolute)
	if err != nil {
		return nil, nil, err
	}
	if transportKind == GitReviewedTransportKind {
		return readGitReviewedDirectory(absolute)
	}
	manifest, err := readBoundedRegularFile(filepath.Join(absolute, "manifest.json"), MaxManifestBytes)
	if err != nil {
		return nil, nil, err
	}
	records, err := readBoundedRegularFile(filepath.Join(absolute, "records.jsonl"), MaxRecordsBytes)
	if err != nil {
		return nil, nil, err
	}
	return manifest, records, nil
}

func BundleDirectoryTransportKind(directory string) (string, error) {
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return "", err
	}
	return bundleDirectoryTransportKind(absolute)
}

func bundleDirectoryTransportKind(absolute string) (string, error) {
	recordsFound, err := pathExists(filepath.Join(absolute, "records.jsonl"))
	if err != nil {
		return "", err
	}
	transportFound, err := pathExists(filepath.Join(absolute, "transport.json"))
	if err != nil {
		return "", err
	}
	if recordsFound && transportFound {
		return "", fmt.Errorf("bundle directory mixes local and Git-reviewed transport layouts")
	}
	if transportFound {
		return GitReviewedTransportKind, nil
	}
	if recordsFound {
		return LocalDirectoryTransportKind, nil
	}
	return "", fmt.Errorf("bundle directory contains no recognized transport layout")
}

func pathExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func InspectBundleDirectory(directory string) (BundleInspection, error) {
	transportKind, err := BundleDirectoryTransportKind(directory)
	if err != nil {
		return BundleInspection{}, err
	}
	manifest, records, err := ReadBundleDirectory(directory)
	if err != nil {
		return BundleInspection{}, err
	}
	bundle, err := ValidateBundle(manifest, records)
	if err != nil {
		return BundleInspection{}, err
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return BundleInspection{}, err
	}
	counts := make(map[string]int, len(bundle.Manifest.RecordCounts))
	for key, value := range bundle.Manifest.RecordCounts {
		counts[key] = value
	}
	return BundleInspection{Directory: absolute, TransportKind: transportKind, Manifest: bundle.Manifest, ManifestBytes: len(manifest), RecordsBytes: len(records), RecordCount: len(bundle.Records), RecordCounts: counts, BodiesLoaded: 0}, nil
}

func readBoundedRegularFile(path string, limit int) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect bundle file %s: %w", filepath.Base(path), err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("bundle file %s must be a regular file", filepath.Base(path))
	}
	if info.Size() > int64(limit) {
		return nil, fmt.Errorf("bundle file %s exceeds %d bytes", filepath.Base(path), limit)
	}
	handle, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer handle.Close()
	body, err := io.ReadAll(io.LimitReader(handle, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(body) > limit {
		return nil, fmt.Errorf("bundle file %s exceeds %d bytes", filepath.Base(path), limit)
	}
	return body, nil
}

func syncDirectory(path string) error {
	handle, err := os.Open(path)
	if err != nil {
		return err
	}
	defer handle.Close()
	return handle.Sync()
}

func CuratedV1Omissions() []Omission {
	result := append([]Omission(nil), curatedV1Omissions...)
	sort.Slice(result, func(i, j int) bool { return omissionLess(result[i], result[j]) })
	return result
}
