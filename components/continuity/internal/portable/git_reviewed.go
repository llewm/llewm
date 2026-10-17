package portable

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/activity"
)

const (
	GitReviewedTransportKind = "git_reviewed_v2"
	gitReviewedFormat        = "llewm-git-reviewed-export"
	gitReviewedVersion       = 2
	maxReviewedRecordBytes   = MaxPayloadBytes + MaxIndexBytes + (64 << 10)
)

type gitReviewedRecord struct {
	Path           string `json:"path"`
	EnvelopeSHA256 string `json:"envelope_sha256"`
}

type gitReviewedDescriptor struct {
	Format         string              `json:"format"`
	FormatVersion  int                 `json:"format_version"`
	ManifestSHA256 string              `json:"manifest_sha256"`
	RecordCount    int                 `json:"record_count"`
	Records        []gitReviewedRecord `json:"records"`
}

// ExportOperationalGitReviewedDirectory publishes one immutable, partitioned
// export beneath root/v3/<lineage>/<record-set-hash>. It never invokes Git.
func ExportOperationalGitReviewedDirectory(ctx context.Context, db *sql.DB, projectID, projectRoot, root string, createdAt time.Time) (ExportResult, error) {
	if err := validateGitReviewedRoot(projectRoot, root); err != nil {
		return ExportResult{}, err
	}
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
	directory, err := PublishGitReviewedDirectory(root, bundle)
	if err != nil {
		return ExportResult{}, err
	}
	return ExportResult{
		Directory: directory, Manifest: bundle.Manifest, ManifestBytes: len(bundle.ManifestBytes),
		RecordsBytes: len(bundle.RecordsBytes), RecordCount: len(bundle.Records),
		MissingReferences: loaded.MissingReferences, TransportKind: GitReviewedTransportKind,
		ReviewRequired: true, GitActionsPerformed: 0,
	}, nil
}

func validateGitReviewedRoot(projectRoot, root string) error {
	projectAbsolute, err := filepath.Abs(projectRoot)
	if err != nil {
		return err
	}
	rootAbsolute, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(projectAbsolute, rootAbsolute)
	if err != nil {
		return err
	}
	if relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("Git-reviewed export root must be a project subdirectory")
	}
	first := strings.Split(filepath.ToSlash(relative), "/")[0]
	if first == ".git" || first == ".llewm" || first == ".continuity" {
		return fmt.Errorf("Git-reviewed export root cannot be inside %s", first)
	}
	current := projectAbsolute
	if err := ensureRealDirectory(current); err != nil {
		return fmt.Errorf("inspect project root: %w", err)
	}
	for _, part := range strings.Split(filepath.Clean(relative), string(filepath.Separator)) {
		current = filepath.Join(current, part)
		if err := ensureRealDirectory(current); err != nil {
			return fmt.Errorf("inspect Git-reviewed export root: %w", err)
		}
	}
	return nil
}

func PublishGitReviewedDirectory(root string, bundle Bundle) (string, error) {
	validated, err := ValidateBundle(bundle.ManifestBytes, bundle.RecordsBytes)
	if err != nil {
		return "", fmt.Errorf("validate bundle before Git-reviewed publication: %w", err)
	}
	rootAbsolute, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if err := ensureRealDirectory(rootAbsolute); err != nil {
		return "", fmt.Errorf("Git-reviewed export root: %w", err)
	}
	lineage := strings.TrimPrefix(validated.Manifest.LineageID, "lineage:")
	parent := filepath.Join(rootAbsolute, MemoryRepositoryRoot, lineage)
	if err := makeRealDirectories(rootAbsolute, filepath.Join(MemoryRepositoryRoot, lineage)); err != nil {
		return "", err
	}
	target := filepath.Join(parent, validated.Manifest.RecordSetSHA256)
	if _, err := os.Lstat(target); err == nil {
		return "", fmt.Errorf("Git-reviewed export already exists for record set %s", validated.Manifest.RecordSetSHA256)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect Git-reviewed export target: %w", err)
	}
	temporary, err := os.MkdirTemp(parent, ".llewm-git-export-*")
	if err != nil {
		return "", err
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(temporary)
		}
	}()
	descriptor := gitReviewedDescriptor{
		Format: gitReviewedFormat, FormatVersion: gitReviewedVersion,
		ManifestSHA256: hashHex(validated.ManifestBytes), RecordCount: len(validated.Records),
		Records: make([]gitReviewedRecord, 0, len(validated.Records)),
	}
	if err := writeExclusiveFile(filepath.Join(temporary, "manifest.json"), validated.ManifestBytes); err != nil {
		return "", err
	}
	for _, record := range validated.Records {
		relative := reviewedRecordPath(record.Envelope)
		if err := os.MkdirAll(filepath.Dir(filepath.Join(temporary, relative)), 0o700); err != nil {
			return "", err
		}
		body := append(append([]byte(nil), record.Bytes...), '\n')
		if err := writeExclusiveFile(filepath.Join(temporary, relative), body); err != nil {
			return "", err
		}
		descriptor.Records = append(descriptor.Records, gitReviewedRecord{Path: filepath.ToSlash(relative), EnvelopeSHA256: record.Envelope.EnvelopeSHA256})
	}
	descriptorBytes, err := canonicalMarshal(descriptor)
	if err != nil {
		return "", err
	}
	if err := writeExclusiveFile(filepath.Join(temporary, "transport.json"), descriptorBytes); err != nil {
		return "", err
	}
	if _, _, err := readGitReviewedDirectory(temporary); err != nil {
		return "", fmt.Errorf("verify Git-reviewed export: %w", err)
	}
	if err := syncTree(temporary); err != nil {
		return "", err
	}
	if err := renameNoReplace(temporary, target); err != nil {
		return "", fmt.Errorf("publish Git-reviewed export: %w", err)
	}
	published = true
	if err := syncDirectory(parent); err != nil {
		return "", err
	}
	return target, nil
}

func readGitReviewedDirectory(directory string) ([]byte, []byte, error) {
	manifest, descriptor, err := readGitReviewedMetadata(directory)
	if err != nil {
		return nil, nil, err
	}
	allowed, err := reviewedAllowedPaths(descriptor)
	if err != nil {
		return nil, nil, err
	}
	var records bytes.Buffer
	for _, entry := range descriptor.Records {
		body, err := readReviewedRecordFile(directory, entry.Path)
		if err != nil {
			return nil, nil, err
		}
		line, err := oneReviewedEnvelope(entry.Path, body)
		if err != nil {
			return nil, nil, err
		}
		var envelope Envelope
		if err := strictDecode(line, &envelope); err != nil {
			return nil, nil, fmt.Errorf("decode Git-reviewed record %s: %w", entry.Path, err)
		}
		if entry.Path != filepath.ToSlash(reviewedRecordPath(envelope)) || entry.EnvelopeSHA256 != envelope.EnvelopeSHA256 {
			return nil, nil, fmt.Errorf("Git-reviewed record path or hash mismatch for %s", entry.Path)
		}
		if records.Len()+len(body) > MaxRecordsBytes {
			return nil, nil, fmt.Errorf("records exceed %d bytes", MaxRecordsBytes)
		}
		records.Write(body)
	}
	if err := rejectUnexpectedReviewedFiles(directory, allowed); err != nil {
		return nil, nil, err
	}
	if _, err := ValidateBundle(manifest, records.Bytes()); err != nil {
		return nil, nil, err
	}
	return manifest, records.Bytes(), nil
}

func readGitReviewedMetadata(directory string) ([]byte, gitReviewedDescriptor, error) {
	manifest, err := readBoundedRegularFile(filepath.Join(directory, "manifest.json"), MaxManifestBytes)
	if err != nil {
		return nil, gitReviewedDescriptor{}, err
	}
	descriptorBytes, err := readBoundedRegularFile(filepath.Join(directory, "transport.json"), MaxManifestBytes)
	if err != nil {
		return nil, gitReviewedDescriptor{}, err
	}
	canonical, err := CanonicalizeJSON(descriptorBytes)
	if err != nil || !bytes.Equal(canonical, descriptorBytes) {
		return nil, gitReviewedDescriptor{}, fmt.Errorf("Git-reviewed transport descriptor is not canonical")
	}
	var descriptor gitReviewedDescriptor
	if err := strictDecode(descriptorBytes, &descriptor); err != nil {
		return nil, gitReviewedDescriptor{}, fmt.Errorf("decode Git-reviewed transport descriptor: %w", err)
	}
	if descriptor.Format != gitReviewedFormat || descriptor.FormatVersion != gitReviewedVersion || descriptor.ManifestSHA256 != hashHex(manifest) || descriptor.RecordCount != len(descriptor.Records) || descriptor.RecordCount > MaxRecordCount || descriptor.Records == nil {
		return nil, gitReviewedDescriptor{}, fmt.Errorf("invalid Git-reviewed transport descriptor")
	}
	return manifest, descriptor, nil
}

func reviewedAllowedPaths(descriptor gitReviewedDescriptor) (map[string]bool, error) {
	allowed := map[string]bool{"manifest.json": true, "transport.json": true}
	for index, entry := range descriptor.Records {
		if !isHash(entry.EnvelopeSHA256) || !validReviewedRelativePath(entry.Path) || allowed[entry.Path] {
			return nil, fmt.Errorf("invalid Git-reviewed record entry %d", index+1)
		}
		allowed[entry.Path] = true
	}
	return allowed, nil
}

func oneReviewedEnvelope(path string, body []byte) ([]byte, error) {
	if len(body) < 2 || body[len(body)-1] != '\n' || bytes.Contains(body[:len(body)-1], []byte{'\n'}) {
		return nil, fmt.Errorf("Git-reviewed record %s must contain one canonical envelope line", path)
	}
	return body[:len(body)-1], nil
}

func readExactGitReviewedRecord(directory, recordType, recordID string, revision int, envelopeHash string) ([]byte, error) {
	_, descriptor, err := readGitReviewedMetadata(directory)
	if err != nil {
		return nil, err
	}
	allowed, err := reviewedAllowedPaths(descriptor)
	if err != nil {
		return nil, err
	}
	if err := rejectUnexpectedReviewedFiles(directory, allowed); err != nil {
		return nil, err
	}
	wantPath := filepath.ToSlash(reviewedRecordPath(Envelope{RecordType: recordType, RecordID: recordID, RecordRevision: revision}))
	var match *gitReviewedRecord
	for index := range descriptor.Records {
		entry := &descriptor.Records[index]
		if entry.Path == wantPath {
			match = entry
			break
		}
	}
	if match == nil || match.EnvelopeSHA256 != envelopeHash {
		return nil, fmt.Errorf("Git-reviewed descriptor does not contain the indexed record")
	}
	body, err := readReviewedRecordFile(directory, match.Path)
	if err != nil {
		return nil, err
	}
	return oneReviewedEnvelope(match.Path, body)
}

func reviewedRecordPath(envelope Envelope) string {
	identity := envelope.RecordType + "\x00" + envelope.RecordID + "\x00" + strconv.Itoa(envelope.RecordRevision)
	sum := sha256.Sum256([]byte(identity))
	return filepath.Join("records", envelope.RecordType, hex.EncodeToString(sum[:])+".json")
}

func validReviewedRelativePath(path string) bool {
	clean := filepath.ToSlash(filepath.Clean(path))
	return path == clean && !filepath.IsAbs(path) && !strings.HasPrefix(path, "../") && strings.HasPrefix(path, "records/")
}

func readReviewedRecordFile(root, relative string) ([]byte, error) {
	parts := strings.Split(filepath.FromSlash(relative), string(filepath.Separator))
	current := root
	for _, part := range parts[:len(parts)-1] {
		current = filepath.Join(current, part)
		if err := ensureRealDirectory(current); err != nil {
			return nil, fmt.Errorf("Git-reviewed record parent: %w", err)
		}
	}
	return readBoundedRegularFile(filepath.Join(root, filepath.FromSlash(relative)), maxReviewedRecordBytes)
}

func rejectUnexpectedReviewedFiles(root string, allowed map[string]bool) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root || entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if entry.Type()&os.ModeSymlink != 0 || !allowed[relative] {
			return fmt.Errorf("unexpected file in Git-reviewed export: %s", relative)
		}
		return nil
	})
}

func ensureRealDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s must be a real directory", path)
	}
	return nil
}

func makeRealDirectories(root, relative string) error {
	current := root
	for _, part := range strings.Split(filepath.Clean(relative), string(filepath.Separator)) {
		current = filepath.Join(current, part)
		created := false
		if err := os.Mkdir(current, 0o700); err != nil {
			if !os.IsExist(err) {
				return err
			}
		} else {
			created = true
		}
		if err := ensureRealDirectory(current); err != nil {
			return err
		}
		if created {
			if err := syncDirectory(filepath.Dir(current)); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeExclusiveFile(path string, body []byte) error {
	handle, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err = handle.Write(body); err == nil {
		err = handle.Sync()
	}
	closeErr := handle.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func syncTree(root string) error {
	var directories []string
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			directories = append(directories, path)
		}
		return nil
	}); err != nil {
		return err
	}
	for index := len(directories) - 1; index >= 0; index-- {
		if err := syncDirectory(directories[index]); err != nil {
			return err
		}
	}
	return nil
}
