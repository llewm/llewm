package privatestorage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateStorageCreationAndReopen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private", "nested")
	if err := MkdirAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := CheckDir(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "credential")
	f, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckFile(f); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("fixture"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if f, err := Create(path); err == nil {
		f.Close()
		t.Fatal("overwrote existing file")
	}
	f, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := CheckFile(f); err != nil {
		t.Fatal(err)
	}
	if err := CheckDir(path); err == nil {
		t.Fatal("file accepted as directory")
	}
	d, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := CheckFile(d); err == nil {
		t.Fatal("directory accepted as file")
	}
}
