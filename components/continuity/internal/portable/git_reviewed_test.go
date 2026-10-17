package portable

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGitReviewedDirectoryPartitionsAndRoundTripsBundle(t *testing.T) {
	recorded := time.Date(2026, 9, 19, 17, 0, 0, 0, time.UTC)
	bundle := fixtureBundle(t, "lineage:abababababababababababababababab", "instance:cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd", "reviewed body", recorded)
	root := t.TempDir()
	directory, err := PublishGitReviewedDirectory(root, bundle)
	if err != nil {
		t.Fatal(err)
	}
	wantSuffix := filepath.Join("v3", strings.Repeat("ab", 16), bundle.Manifest.RecordSetSHA256)
	if !strings.HasSuffix(directory, wantSuffix) {
		t.Fatalf("published directory %q does not end with %q", directory, wantSuffix)
	}
	if _, err := os.Stat(filepath.Join(directory, "records.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("Git-reviewed profile wrote an aggregate record stream: %v", err)
	}
	transportKind, err := BundleDirectoryTransportKind(directory)
	if err != nil || transportKind != GitReviewedTransportKind {
		t.Fatalf("transport kind=%q err=%v", transportKind, err)
	}
	manifest, records, err := ReadBundleDirectory(directory)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(manifest, bundle.ManifestBytes) || !bytes.Equal(records, bundle.RecordsBytes) {
		t.Fatal("partitioned Git-reviewed export did not reconstruct the canonical bundle")
	}
	secondDirectory, err := PublishGitReviewedDirectory(t.TempDir(), bundle)
	if err != nil {
		t.Fatal(err)
	}
	firstDescriptor, err := os.ReadFile(filepath.Join(directory, "transport.json"))
	if err != nil {
		t.Fatal(err)
	}
	secondDescriptor, err := os.ReadFile(filepath.Join(secondDirectory, "transport.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstDescriptor, secondDescriptor) {
		t.Fatal("identical bundles produced different Git-reviewed descriptors")
	}
	if _, err := PublishGitReviewedDirectory(root, bundle); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("same immutable record set was overwritten: %v", err)
	}

	var recordPath string
	err = filepath.WalkDir(filepath.Join(directory, "records"), func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			recordPath = path
		}
		return err
	})
	if err != nil || recordPath == "" {
		t.Fatalf("find partitioned record: path=%q err=%v", recordPath, err)
	}
	body, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	body[len(body)-2] ^= 1
	if err := os.WriteFile(recordPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadBundleDirectory(directory); err == nil {
		t.Fatal("tampered partitioned record was accepted")
	}
}

func TestGitReviewedRootMustBeRealProjectSubdirectory(t *testing.T) {
	project := t.TempDir()
	valid := filepath.Join(project, "reviewed")
	if err := os.Mkdir(valid, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := validateGitReviewedRoot(project, valid); err != nil {
		t.Fatalf("valid root rejected: %v", err)
	}
	if err := validateGitReviewedRoot(project, t.TempDir()); err == nil || !strings.Contains(err.Error(), "project subdirectory") {
		t.Fatalf("outside root accepted: %v", err)
	}
	if err := validateGitReviewedRoot(project, filepath.Join(project, ".llewm/continuity")); err == nil || !strings.Contains(err.Error(), ".llewm") {
		t.Fatalf("private store root accepted: %v", err)
	}
	real := filepath.Join(project, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(project, "linked")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := validateGitReviewedRoot(project, link); err == nil || !strings.Contains(err.Error(), "real directory") {
		t.Fatalf("symlinked root accepted: %v", err)
	}
}

func TestGitReviewedDirectoryRejectsMixedAndUnexpectedFiles(t *testing.T) {
	recorded := time.Date(2026, 9, 19, 17, 30, 0, 0, time.UTC)
	bundle := fixtureBundle(t, "lineage:edededededededededededededededed", "instance:fefefefefefefefefefefefefefefefe", "reviewed body", recorded)
	directory, err := PublishGitReviewedDirectory(t.TempDir(), bundle)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "unexpected.txt"), []byte("not declared"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadBundleDirectory(directory); err == nil || !strings.Contains(err.Error(), "unexpected file") {
		t.Fatalf("unexpected Git-reviewed file was accepted: %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, "records.jsonl"), bundle.RecordsBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadBundleDirectory(directory); err == nil || !strings.Contains(err.Error(), "mixes") {
		t.Fatalf("mixed transport layout was accepted: %v", err)
	}
}
