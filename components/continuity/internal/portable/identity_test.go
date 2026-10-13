package portable

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/database"
)

func testIdentityService(t *testing.T) (context.Context, Service, string, string) {
	t.Helper()
	ctx := context.Background()
	db, err := database.Initialize(ctx, filepath.Join(t.TempDir(), "continuity.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store := activity.Store{DB: db}
	first, err := store.EnsureProject(ctx, "/work/first", "first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.EnsureProject(ctx, "/work/second", "second")
	if err != nil {
		t.Fatal(err)
	}
	return ctx, Service{DB: db, Now: func() time.Time { return time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC) }}, first, second
}

func TestInitializeGeneratesStableProjectIdentity(t *testing.T) {
	ctx, service, projectID, _ := testIdentityService(t)
	if value, err := service.Get(ctx, projectID); err != nil || value != nil {
		t.Fatalf("unexpected uninitialized identity: %#v %v", value, err)
	}
	first, err := service.Initialize(ctx, projectID, "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Initialize(ctx, projectID, "")
	if err != nil {
		t.Fatal(err)
	}
	if first != second || !strings.HasPrefix(first.LineageID, "lineage:") || !strings.HasPrefix(first.InstanceID, "instance:") || first.AssociationSource != "generated" || first.Authority != "caller_asserted" {
		t.Fatalf("unexpected generated identity: %#v %#v", first, second)
	}
}

func TestInitializeExplicitAdoptionIsIsolatedAndImmutable(t *testing.T) {
	ctx, service, firstProject, secondProject := testIdentityService(t)
	lineage := "lineage:0123456789abcdef0123456789abcdef"
	first, err := service.Initialize(ctx, firstProject, lineage)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Initialize(ctx, secondProject, lineage)
	if err != nil {
		t.Fatal(err)
	}
	if first.LineageID != second.LineageID || first.InstanceID == second.InstanceID || first.AssociationSource != "explicit_adoption" {
		t.Fatalf("unexpected adopted identities: %#v %#v", first, second)
	}
	if _, err := service.Initialize(ctx, firstProject, "lineage:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err == nil {
		t.Fatal("expected lineage reassociation to fail")
	}
	if _, err := service.Initialize(ctx, "missing-project", lineage); err == nil {
		t.Fatal("expected unknown project foreign key to fail")
	}
}

func TestInitializeRejectsAmbiguousLineage(t *testing.T) {
	ctx, service, projectID, _ := testIdentityService(t)
	for _, value := range []string{"project-name", "lineage:ABCDEF", "lineage:1234"} {
		if _, err := service.Initialize(ctx, projectID, value); err == nil {
			t.Fatalf("accepted invalid lineage %q", value)
		}
	}
}
