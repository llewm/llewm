package attribution

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/database"
)

func testService(t *testing.T) (context.Context, Service, string, string) {
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
	return ctx, Service{DB: db}, first, second
}

func TestBoundedReadsAreRecentAndProjectScoped(t *testing.T) {
	ctx, service, firstProject, secondProject := testService(t)
	base := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	for index, projectID := range []string{firstProject, firstProject, firstProject, secondProject} {
		if _, err := service.AppendInvocation(ctx, Invocation{
			ID: "invocation-" + string(rune('a'+index)), ProjectID: projectID,
			SessionID: "session", OccurredAt: base.Add(time.Duration(index) * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}
	records, err := service.RecentInvocations(ctx, firstProject, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].ID != "invocation-c" || records[1].ID != "invocation-b" {
		t.Fatalf("unexpected bounded records: %#v", records)
	}
	if _, err := service.RecentInvocations(ctx, firstProject, MaxReadLimit+1); err == nil {
		t.Fatal("expected oversized read to fail")
	}
}

func TestUnknownInvocationIdentityIsExplicit(t *testing.T) {
	ctx, service, projectID, _ := testService(t)
	record, err := service.AppendInvocation(ctx, Invocation{ProjectID: projectID})
	if err != nil {
		t.Fatal(err)
	}
	if record.ActorType != "unknown" || record.IdentitySource != "unknown" {
		t.Fatalf("unknown identity was invented: %#v", record)
	}
}

func TestRecordsAreAppendOnly(t *testing.T) {
	ctx, service, projectID, _ := testService(t)
	record := Invocation{ID: "invocation-fixed", ProjectID: projectID, ActorType: "agent", IdentitySource: "observed"}
	if _, err := service.AppendInvocation(ctx, record); err != nil {
		t.Fatal(err)
	}
	record.Model = "replacement"
	if _, err := service.AppendInvocation(ctx, record); err == nil {
		t.Fatal("expected duplicate append to fail rather than overwrite history")
	}
	var model *string
	if err := service.DB.QueryRowContext(ctx, `SELECT model FROM actor_invocations WHERE id = ?`, record.ID).Scan(&model); err != nil {
		t.Fatal(err)
	}
	if model != nil {
		t.Fatalf("duplicate append changed stored record: %q", *model)
	}
}

func TestInvocationEffortRoundTripsWithoutInference(t *testing.T) {
	ctx, service, projectID, _ := testService(t)
	created, err := service.AppendInvocation(ctx, Invocation{
		ID: "invocation-effort", ProjectID: projectID, ActorType: "agent",
		Client: "codex", Model: "model-a", IdentitySource: "client_hook_claim",
		EffortRequested: &Effort{Raw: "HIGH", Normalized: "high", Source: "request_metadata", Authority: "client_hook_claim"},
		EffortEffective: &Effort{Raw: "provider-high-v2", Source: "provider_response", Authority: "provider_reported"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.SchemaVersion != InvocationSchemaVersion {
		t.Fatalf("unexpected invocation schema: %#v", created)
	}
	values, err := service.RecentInvocations(ctx, projectID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 || values[0].EffortRequested == nil || values[0].EffortEffective == nil || values[0].EffortRequested.Raw != "HIGH" || values[0].EffortRequested.Normalized != "high" || values[0].EffortEffective.Raw != "provider-high-v2" || values[0].EffortEffective.Normalized != "" {
		t.Fatalf("effort did not round trip: %#v", values)
	}
	if _, err := service.AppendInvocation(ctx, Invocation{ProjectID: projectID, EffortRequested: &Effort{Raw: "new-provider-setting", Source: "request_metadata", Authority: "client_hook_claim"}}); err != nil {
		t.Fatalf("unknown raw effort should be retained: %v", err)
	}
	if _, err := service.AppendInvocation(ctx, Invocation{ProjectID: projectID, EffortRequested: &Effort{Raw: "high", Normalized: "turbo", Source: "request_metadata", Authority: "client_hook_claim"}}); err == nil {
		t.Fatal("expected invented normalized effort to fail")
	}
	if _, err := service.AppendInvocation(ctx, Invocation{ProjectID: projectID, EffortRequested: &Effort{Raw: "", Source: "request_metadata", Authority: "client_hook_claim"}}); err == nil {
		t.Fatal("expected missing raw effort to fail")
	}
}
