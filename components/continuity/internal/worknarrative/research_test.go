package worknarrative

import (
	"strings"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/activity"
)

func TestSourceObservationsAndCitationValidation(t *testing.T) {
	ctx, s, p, _ := testService(t)
	source := Record{ID: "source", ProjectID: p, Title: "Documentation", Trigger: "manual", Creator: "agent", Source: &Source{URL: "https://example.org/docs?v=1#api", AccessedAt: "2026-10-18T10:00:00-05:00", Publisher: "Publisher token=secret-value", Authority: "caller_asserted"}, Claims: []Claim{{Kind: "note", Text: "Selected API description", Authority: "caller_asserted"}}}
	obs, err := s.Append(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if obs.Source.AccessedAt != "2026-10-18T15:00:00Z" || strings.Contains(obs.Source.Publisher, "secret-value") {
		t.Fatalf("normalization/redaction: %+v", obs.Source)
	}
	finding := Record{ID: "finding", ProjectID: p, Title: "Findings", Trigger: "manual", Creator: "agent", Claims: []Claim{
		{Kind: "outcome", Text: "First finding", Authority: "caller_asserted", Citations: []Citation{{Source: "narrative:source", Relation: "supports", Locator: "API section"}}},
		{Kind: "limitation", Text: "Contrary evidence", Authority: "caller_asserted", Citations: []Citation{{Source: "narrative:source", Relation: "contradicts", Locator: "Limits section"}}},
	}}
	if _, err := s.Append(ctx, finding); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM work_narrative_citations WHERE project_id=? AND narrative_id='finding'`, p).Scan(&count); err != nil || count != 2 {
		t.Fatalf("edges=%d %v", count, err)
	}
	shown, err := s.Show(ctx, p, "finding", 4096)
	if err != nil || !strings.Contains(shown.Markdown, "Citation: contradicts narrative:source") {
		t.Fatalf("show: %+v %v", shown, err)
	}
	other, err := (activity.Store{DB: s.DB}).EnsureProject(ctx, t.TempDir(), "other")
	if err != nil {
		t.Fatal(err)
	}
	finding.ID = "foreign"
	finding.ProjectID = other
	if _, err := s.Append(ctx, finding); err == nil {
		t.Fatal("cross-project citation accepted")
	}
	finding.ProjectID = p
	finding.Claims[0].Citations[0].Source = "narrative:missing"
	if _, err := s.Append(ctx, finding); err == nil {
		t.Fatal("missing citation accepted")
	}
	source.ID = "changed-source"
	source.SupersedesID = "source"
	source.Source.Version = "2"
	if _, err := s.Append(ctx, source); err != nil {
		t.Fatal(err)
	}
	old, err := s.Show(ctx, p, "source", 4096)
	if err != nil || old.Record.Source.Version != "" || old.Record.State != "superseded" {
		t.Fatalf("old observation lost: %+v %v", old, err)
	}
}

func TestSourceURLValidationDoesNotPersistCredentialsOrInventIdentity(t *testing.T) {
	for _, raw := range []string{"file:///etc/passwd", "https://u:p@example.org/", "https://example.org/?token=secret", "https://example.org/?X-Amz-Signature=secret", "https://example.org/?%74oken=secret", "https://example.org/?bad=%GG"} {
		if _, _, err := NormalizeResearch(&Source{URL: raw, AccessedAt: "2026-10-18T00:00:00Z", Authority: "caller_asserted"}, nil); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	url := "https://example.org/docs?version=2&lang=en#api"
	got, _, err := NormalizeResearch(&Source{URL: url, AccessedAt: "2026-10-18T00:00:00Z", Authority: "caller_asserted"}, nil)
	if err != nil || got.URL != url {
		t.Fatalf("changed meaningful URL: %+v %v", got, err)
	}
}
