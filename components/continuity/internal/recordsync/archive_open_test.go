package recordsync

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

func TestDurableArchiveOpenFreshIdentityWithoutLegacyFiles(t *testing.T) {
	ctx, store := archiveStoreFixture(t)
	legacy := filepath.Join(t.TempDir(), "history")
	if err := openArchiveStore(ctx, store, legacy); err != nil {
		t.Fatal(err)
	}
	measured, work := syncwork.WithWorkMetrics(ctx)
	if err := openArchiveStore(measured, store, legacy); err != nil {
		t.Fatal(err)
	}
	if work.Counts()["archive_audit_envelopes"] != 0 {
		t.Fatal("normal ready open audited old history")
	}
	if _, err := os.Lstat(legacy); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fresh open created a filesystem archive: %v", err)
	}
	archiveTransaction(t, store, func(tx *sql.Tx) error {
		stamp, err := store.stampWithin(ctx, tx, "ready")
		if err == nil && stamp.Records != 0 {
			t.Fatal("new identity has old observations")
		}
		return err
	})
}

func TestDurableArchiveOpenNeverInfersEmptyLegacyArchive(t *testing.T) {
	ctx, store := archiveStoreFixture(t)
	legacy := t.TempDir()
	if err := openArchiveStore(ctx, store, legacy); err == nil {
		t.Fatal("existing filesystem route bypassed migration")
	}
	// Simulate an identity that predates091: no fresh-identity trigger ran.
	if _, err := store.DB.Exec(`DROP TRIGGER history_archive_new_identity`); err != nil {
		t.Fatal(err)
	}
	project, err := (activity.Store{DB: store.DB}).EnsureProject(ctx, t.TempDir(), "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (portable.Service{DB: store.DB}).Initialize(ctx, project, lineage); err != nil {
		t.Fatal(err)
	}
	old := archiveStore{DB: store.DB, ProjectID: project, Lineage: lineage}
	absent := filepath.Join(t.TempDir(), "missing-history")
	if err := openArchiveStore(ctx, old, absent); !errors.Is(err, errArchiveNotReady) {
		t.Fatalf("missing legacy tracking initialized as empty: %v", err)
	}
	archiveTransaction(t, old, func(tx *sql.Tx) error { return old.initializeWithin(ctx, tx) })
	if _, err := old.DB.Exec(`UPDATE history_archive_state SET fresh_identity=1 WHERE project_id=?`, old.ProjectID); err == nil {
		t.Fatal("legacy state was relabeled as a fresh identity")
	}
	if err := openArchiveStore(ctx, old, absent); !errors.Is(err, errArchiveNotReady) {
		t.Fatalf("legacy staging initialized as empty: %v", err)
	}
}

func TestDurableArchiveOpenRequiresCompletedAndIntactFence(t *testing.T) {
	ctx, store, paths := archiveCutoverFixture(t)
	if _, err := prepareArchiveCutover(ctx, store, paths); err != nil {
		t.Fatal(err)
	}
	if err := openArchiveStore(ctx, store, paths.Source); !errors.Is(err, errArchiveNotReady) {
		t.Fatalf("incomplete cutover opened: %v", err)
	}
	if err := completeArchiveCutover(ctx, store, paths); err != nil {
		t.Fatal(err)
	}
	measured, work := syncwork.WithWorkMetrics(ctx)
	if err := openArchiveStore(measured, store, paths.Source); err != nil {
		t.Fatal(err)
	}
	if work.Counts()["archive_audit_envelopes"] != 0 {
		t.Fatal("normal migrated open audited old history")
	}
	if err := os.WriteFile(paths.Source, []byte("unexpected fence"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := openArchiveStore(ctx, store, paths.Source); err == nil {
		t.Fatal("damaged migration fence accepted")
	}
}
