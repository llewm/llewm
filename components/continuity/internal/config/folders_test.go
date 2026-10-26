package config

import (
	"github.com/llewm/llewm/components/continuity/internal/foldersync"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFolderConfigurationRoundTripsWithoutEnablingLocalOnlyInstalls(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CONTINUITY_HOME", filepath.Join(root, "state"))
	path, err := WriteDefault(root)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(body), "[sync]") {
		t.Fatalf("default install manufactured sync config: %v\n%s", err, body)
	}
	settings := Sync{Manual: true, Folders: []foldersync.Endpoint{{Name: "personal", Directory: t.TempDir(), StoreID: strings.Repeat("a", 32), LineageID: "lineage:" + strings.Repeat("b", 32), Relay: true}}}
	if _, err := SetSync(root, settings); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Sync.Configured() || loaded.Sync.Automatic() || len(loaded.Sync.Folders) != 1 || !loaded.Sync.Folders[0].Relay {
		t.Fatalf("lost folder settings: %+v", loaded.Sync)
	}
	if _, err := SetSync(root, Sync{}); err != nil {
		t.Fatal(err)
	}
	body, err = os.ReadFile(path)
	if err != nil || strings.Contains(string(body), "[sync]") {
		t.Fatalf("empty sync did not return to local-only config: %v\n%s", err, body)
	}
}
func TestFolderConfigurationRejectsAmbiguousOrUnboundEndpoints(t *testing.T) {
	endpoint := foldersync.Endpoint{Name: "personal", Directory: t.TempDir(), StoreID: strings.Repeat("a", 32), LineageID: "lineage:" + strings.Repeat("b", 32)}
	for _, settings := range []Sync{{RelayToGit: true}, {Folders: []foldersync.Endpoint{endpoint, endpoint}}, {Folders: []foldersync.Endpoint{{Name: "git", Directory: endpoint.Directory, StoreID: endpoint.StoreID}}}, {Folders: []foldersync.Endpoint{{Name: "personal", Directory: "relative", StoreID: endpoint.StoreID}}}, {Folders: []foldersync.Endpoint{{Name: "personal", Directory: endpoint.Directory, StoreID: "guess"}}}} {
		if settings.Validate() == nil {
			t.Fatalf("accepted invalid endpoint config: %+v", settings)
		}
	}
}
