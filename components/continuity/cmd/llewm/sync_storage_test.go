package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/app"
	"github.com/llewm/llewm/components/continuity/internal/config"
	"github.com/llewm/llewm/components/continuity/internal/historystorage"
	"github.com/llewm/llewm/components/continuity/internal/memsync"
	"github.com/llewm/llewm/components/continuity/internal/portable"
)

func TestExplicitConversionRelaysForeignArchiveWithoutLegacyDirectory(t *testing.T) {
	ctx := context.Background()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	root := testProject(t)
	runCLI(t, "", "init")
	remote := filepath.Join(t.TempDir(), "memory.git")
	if output, err := exec.Command("git", "init", "--quiet", "--bare", remote).CombinedOutput(); err != nil {
		t.Fatalf("remote fixture: %v %s", err, output)
	}
	runCLI(t, "", "sync", "init", "--remote", remote)
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	settings := cfg.Sync
	settings.RelayToGit, settings.Manual = true, true
	if _, err := config.SetSync(root, settings); err != nil {
		t.Fatal(err)
	}
	application, err := app.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := projectArchive(application)
	if err != nil {
		application.Close()
		t.Fatal(err)
	}
	object, err := portable.EncodeEnvelope(portable.EnvelopeSpec{LineageID: archive.Lineage, RecordType: "decision", RecordSchemaVersion: 1, RecordID: "foreign-relay", RecordedAt: time.Now().UTC(), Authority: "caller_asserted", Index: []byte(`{"title":"Relay observed history","status":"accepted"}`), Payload: []byte(`{"title":"Relay observed history","rationale":"Explicit destination authorization","disposition":"accepted","actor":"owner","actor_type":"human","review_source":"","links":[],"operational_schema_version":1}`)})
	if err != nil {
		application.Close()
		t.Fatal(err)
	}
	if _, err := archive.Put(object, "folder:foreign-fixture"); err != nil {
		application.Close()
		t.Fatal(err)
	}
	application.Close()
	if _, err := os.Stat(filepath.Join(config.StateDir(root), "history", "lineage")); !os.IsNotExist(err) {
		t.Fatalf("fixture unexpectedly has a legacy archive route: %v", err)
	}
	var result memsync.Result
	decodeCLI(t, runCLI(t, "", "sync", "convert", "--writers-stopped", "--output", "json"), &result)
	if result.Outcome != memsync.OutcomeSynced || result.ExportedRecords != 1 || result.ImportedRecords != 1 || result.UnpushedCommits != 0 {
		t.Fatalf("explicit relay lost SQLite observations: %+v", result)
	}
}

func TestStorageCommandsInitializeOnlyFreshIdentityAndAudit(t *testing.T) {
	root := testProject(t)
	runCLI(t, "", "init")
	application, err := app.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = (portable.Service{DB: application.DB}).Initialize(context.Background(), application.ProjectID, "")
	application.Close()
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "storage-plan.json")
	var sealed historystorage.Seal
	decodeCLI(t, runCLI(t, "", "sync", "storage", "plan", "--save", file), &sealed)
	if sealed.Plan.Root != root || sealed.Plan.ArchiveMode != "initialize" {
		t.Fatalf("fresh plan: %+v", sealed)
	}
	for _, operation := range []string{"prepare", "complete", "complete", "audit"} {
		args := []string{"sync", "storage", operation}
		if operation != "audit" {
			args = append(args, "--plan", file, "--writers-stopped")
		}
		var result historystorage.Result
		decodeCLI(t, runCLI(t, "", args...), &result)
		if result.Archive.Envelopes != 0 || result.Archive.State != "ready" {
			t.Fatalf("%s: %+v", operation, result)
		}
	}
}
