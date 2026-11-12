package dirsync

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPathAcceptsDirectoriesAndRejectsInvalidPaths(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "nested", "store")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(directory, "records.jsonl")
	const contents = "flushed records"
	if err := os.WriteFile(file, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, valid := range []string{root, directory} {
		if err := Path(valid); err != nil {
			t.Fatalf("directory %s: %v", valid, err)
		}
	}
	if body, err := os.ReadFile(file); err != nil || string(body) != contents {
		t.Fatalf("directory flush changed contents: %q %v", body, err)
	}
	for _, invalid := range []string{file, filepath.Join(directory, "missing"), filepath.Join(file, "child")} {
		if err := Path(invalid); err == nil {
			t.Fatalf("accepted invalid directory path %s", invalid)
		}
	}
}

func TestHandleFlushesRootDirectoriesAndRejectsFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "entry"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	directory, err := root.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	if err := Handle(directory); err != nil {
		t.Fatalf("root directory flush: %v", err)
	}
	entry, err := root.Open("entry")
	if err != nil {
		t.Fatal(err)
	}
	defer entry.Close()
	if err := Handle(entry); err == nil {
		t.Fatal("accepted a regular file as a directory")
	}
}
