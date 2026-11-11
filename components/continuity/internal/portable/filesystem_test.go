package portable

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBundleDirectoryPublicationIsAtomicAndNeverOverwrites(t *testing.T) {
	recorded := time.Date(2026, 9, 19, 1, 0, 0, 0, time.UTC)
	bundle := fixtureBundle(t, "lineage:cccccccccccccccccccccccccccccccc", "instance:33333333333333333333333333333333", "portable body", recorded)
	target := filepath.Join(t.TempDir(), "export")
	if err := PublishBundleDirectory(target, bundle); err != nil {
		t.Fatal(err)
	}
	inspection, err := InspectBundleDirectory(target)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.RecordCount != 1 || inspection.BodiesLoaded != 0 || inspection.Manifest.LineageID != bundle.Manifest.LineageID {
		t.Fatalf("unexpected inspection: %#v", inspection)
	}
	if err := PublishBundleDirectory(target, bundle); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("existing target was replaced: %v", err)
	}
	if body, err := os.ReadFile(filepath.Join(target, "records.jsonl")); err != nil || string(body) != string(bundle.RecordsBytes) {
		t.Fatalf("published records changed: %v", err)
	}
}

func TestBundleDirectoryReaderRejectsSymlinkedFiles(t *testing.T) {
	recorded := time.Date(2026, 9, 19, 1, 30, 0, 0, time.UTC)
	bundle := fixtureBundle(t, "lineage:dddddddddddddddddddddddddddddddd", "instance:44444444444444444444444444444444", "portable body", recorded)
	root := t.TempDir()
	directory := filepath.Join(root, "bundle")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	manifestSource := filepath.Join(root, "manifest-source.json")
	if err := os.WriteFile(manifestSource, bundle.ManifestBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(manifestSource, filepath.Join(directory, "manifest.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "records.jsonl"), bundle.RecordsBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadBundleDirectory(directory); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("symlinked manifest was accepted: %v", err)
	}
}

func TestSyncDirectoryAcceptsDirectoriesAndRejectsInvalidPaths(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "nested", "bundle")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(directory, "records.jsonl")
	const contents = "flushed records"
	if err := os.WriteFile(file, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, valid := range []string{root, directory} {
		if err := syncDirectory(valid); err != nil {
			t.Fatalf("directory %s: %v", valid, err)
		}
	}
	if body, err := os.ReadFile(file); err != nil || string(body) != contents {
		t.Fatalf("directory flush changed contents: %q %v", body, err)
	}
	for _, invalid := range []string{file, filepath.Join(directory, "missing"), filepath.Join(file, "child")} {
		if err := syncDirectory(invalid); err == nil {
			t.Fatalf("accepted invalid directory path %s", invalid)
		}
	}
}

func TestBundleDirectoryPublicationPropagatesParentErrors(t *testing.T) {
	recorded := time.Date(2026, 9, 19, 2, 0, 0, 0, time.UTC)
	bundle := fixtureBundle(t, "lineage:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", "instance:55555555555555555555555555555555", "portable body", recorded)
	root := t.TempDir()
	if err := PublishBundleDirectory(filepath.Join(root, "missing", "export"), bundle); err == nil {
		t.Fatal("published beneath a missing parent")
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PublishBundleDirectory(filepath.Join(file, "export"), bundle); err == nil {
		t.Fatal("published beneath a regular file")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 {
		t.Fatalf("failed publication left entries: %v %v", entries, err)
	}
}
