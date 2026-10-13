package main

import (
	"archive/zip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestArchiveSafetyPermissionsAndContents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preview.zip")
	if err := writeArchive(path, map[string][]byte{"llewm": []byte("binary"), "INSTALL.txt": []byte("guide")}, map[string]os.FileMode{"llewm": 0o755}, time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
	archive, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	if len(archive.File) != 2 || archive.File[1].Name != "llewm" || archive.File[1].Mode().Perm() != 0o755 {
		t.Fatalf("unexpected archive: %#v", archive.File)
	}
	reader, err := archive.File[1].Open()
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(reader)
	reader.Close()
	if err != nil || string(body) != "binary" {
		t.Fatalf("bad contents: %s %v", body, err)
	}
	if err := writeArchive(path, nil, nil, time.Now()); err == nil {
		t.Fatal("overwrote an archive")
	}
	for _, name := range []string{"../secret", "/absolute", "a\\b", "a/../b"} {
		if err := writeArchive(filepath.Join(t.TempDir(), "unsafe.zip"), map[string][]byte{name: []byte("private")}, nil, time.Now()); err == nil {
			t.Fatalf("unsafe entry accepted: %s", name)
		}
	}
}

func TestPluginPackagingOnlyUsesTrackedFilesAndStampsVersion(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".codex-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	metadata := filepath.Join(root, ".codex-plugin", "plugin.json")
	if err := os.WriteFile(metadata, []byte(`{"name":"llewm","version":"source-version"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "private-token"), []byte("do not ship"), 0o600); err != nil {
		t.Fatal(err)
	}
	files, err := pluginFiles(root, ".codex-plugin/plugin.json", "0.1.0-preview.1", []string{".codex-plugin/plugin.json"})
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]string
	if err := json.Unmarshal(files["llewm/.codex-plugin/plugin.json"], &value); err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || value["version"] != "0.1.0-preview.1" {
		t.Fatalf("bad plugin: %#v", files)
	}
	body, err := os.ReadFile(metadata)
	if err != nil || string(body) != `{"name":"llewm","version":"source-version"}` {
		t.Fatal("changed source metadata")
	}
	if _, err := pluginFiles(root, ".codex-plugin/plugin.json", "v", []string{"../secret"}); err == nil {
		t.Fatal("unsafe source accepted")
	}
}

func TestTargets(t *testing.T) {
	got, err := parseTargets("windows/amd64,linux/arm64")
	if err != nil || !reflect.DeepEqual(got, []string{"linux/arm64", "windows/amd64"}) {
		t.Fatalf("targets: %v %v", got, err)
	}
	for _, value := range []string{"", "linux/amd64,linux/amd64", "unknown/amd64", "linux/amd64;echo"} {
		if _, err := parseTargets(value); err == nil {
			t.Fatalf("invalid targets accepted: %s", value)
		}
	}
}

func TestPreviewVersion(t *testing.T) {
	for _, value := range []string{"0.1.0-preview.1", "1.0.0-alpha-beta.0"} {
		if !validPreviewVersion(value) {
			t.Fatalf("valid version rejected: %s", value)
		}
	}
	for _, value := range []string{"0.1.0", "01.1.0-preview.1", "0.1.0-preview..1", "0.1.0-preview.01", "0.1.0-preview/1", "0.1.0-preview.1;echo"} {
		if validPreviewVersion(value) {
			t.Fatalf("invalid version accepted: %s", value)
		}
	}
}
