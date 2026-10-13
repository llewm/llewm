package history

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/decision"
	"github.com/llewm/llewm/components/continuity/internal/topic"
	"github.com/llewm/llewm/components/continuity/internal/workitem"
	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
)

type fixture struct {
	ctx       context.Context
	db        *sql.DB
	projectID string
	history   Service
	topics    topic.Service
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	db, err := database.Initialize(ctx, filepath.Join(t.TempDir(), "continuity.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	projectID, err := (activity.Store{DB: db}).EnsureProject(ctx, root, "test")
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { clock = clock.Add(time.Hour); return clock }
	narratives := worknarrative.Service{DB: db, Root: root, Now: now}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	for _, node := range []string{"cards", "cards-child", "other"} {
		exec(`INSERT INTO roadmap_nodes(project_id,node_id,revision,title,status,priority,source_path,authority,recorded_at,schema_version) VALUES(?,?,1,?,'accepted',10,'docs/x.md','caller_asserted','2026-09-14T00:00:00Z',1)`, projectID, node, node)
	}
	exec(`INSERT INTO roadmap_node_links(id,project_id,node_id,node_revision,target_node_id,relationship,authority,recorded_at,schema_version) VALUES('l1',?,'cards',1,'cards-child','parent_of','caller_asserted','2026-09-14T00:00:00Z',1)`, projectID)
	for i, binding := range [][2]string{{"cards", "internal/referencecard"}, {"cards", "cmd/main.go"}, {"other", "cmd/main.go"}} {
		exec(`INSERT INTO roadmap_file_bindings(id,project_id,node_id,node_revision,path,path_kind,relationship,authority,recorded_at,schema_version) VALUES(?,?,?,1,?,?,'implementation','caller_asserted','2026-09-14T00:00:00Z',1)`, "b"+string(rune('0'+i)), projectID, binding[0], binding[1], map[bool]string{true: "directory", false: "file"}[!strings.Contains(binding[1], ".")])
	}
	append := func(id, title string, claims []worknarrative.Claim, anchors []worknarrative.Anchor) {
		t.Helper()
		if _, err := narratives.Append(ctx, worknarrative.Record{ID: id, ProjectID: projectID, Title: title, Trigger: "commit", Creator: "test", Claims: claims, Anchors: anchors}); err != nil {
			t.Fatal(err)
		}
	}
	append("plan-cards", "Plan reference cards", []worknarrative.Claim{
		{Kind: "intent", Label: "Why", Text: "Frequently requested syntax reminders should be retrievable without another model call.", Authority: "caller_asserted"},
		{Kind: "decision", Label: "Design decisions", Text: "Cards snapshot source hashes and report drift.", Authority: "caller_asserted"},
	}, []worknarrative.Anchor{{Kind: "roadmap_node", Target: "cards-child", Authority: "caller_asserted"}})
	append("implement-cards", "Implement reference cards", []worknarrative.Claim{
		{Kind: "outcome", Text: "Added card record, list, and show commands.", Authority: "caller_asserted"},
		{Kind: "limitation", Label: "Known limitations and remaining work", Text: "No card has been recorded in a live project.", Authority: "caller_asserted"},
	}, []worknarrative.Anchor{{Kind: "file", Target: "internal/referencecard/service.go", ContentSHA256: strings.Repeat("ab", 32), Authority: "machine_observed"}})
	append("shared-main", "Touch shared command file", []worknarrative.Claim{
		{Kind: "outcome", Text: "Registered unrelated commands in the shared file.", Authority: "caller_asserted"},
	}, []worknarrative.Anchor{{Kind: "file", Target: "cmd/main.go", ContentSHA256: strings.Repeat("ab", 32), Authority: "machine_observed"}})
	if _, err := (decision.Service{DB: db, Now: now}).Append(ctx, decision.Record{ID: "cards-memory-jog", ProjectID: projectID, Title: "Use cards as memory jogs", Rationale: "Cards stay compact reminders, not instructions.", Disposition: "accepted", Actor: "owner", ActorType: "human", Authority: "caller_asserted", Links: []decision.Link{{Kind: "subject", TargetKind: "roadmap_node", TargetID: "cards", TargetRevision: "1"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := (workitem.Service{DB: db, Now: now}).Capture(ctx, workitem.Capture{Item: workitem.Item{ID: "tui-ideas", ProjectID: projectID, Kind: "idea", Title: "Show ideas in the terminal", Body: "Add an ideas view.", Creator: "test"}}); err != nil {
		t.Fatal(err)
	}
	return fixture{ctx: ctx, db: db, projectID: projectID, history: Service{DB: db}, topics: topic.Service{DB: db, Now: now}}
}

func refs(page SearchPage) []string {
	var values []string
	for _, hit := range page.Hits {
		values = append(values, hit.Ref)
	}
	return values
}

func TestClaimsAndIndexFollowWrites(t *testing.T) {
	f := newFixture(t)
	var label string
	if err := f.db.QueryRowContext(f.ctx, `SELECT label FROM work_narrative_claims WHERE narrative_id='implement-cards' AND ordinal=1`).Scan(&label); err != nil || label != "Known limitations and remaining work" {
		t.Fatalf("claim label = %q %v", label, err)
	}
	counts, err := f.history.Check(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, count := range counts {
		if count.Canonical != count.Indexed {
			t.Fatalf("family %s canonical %d indexed %d", count.Family, count.Canonical, count.Indexed)
		}
	}
	before, err := f.history.Search(f.ctx, f.projectID, SearchOptions{Query: "reminder*"})
	if err != nil {
		t.Fatal(err)
	}
	rebuilt, err := f.history.Reindex(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := f.history.Search(f.ctx, f.projectID, SearchOptions{Query: "reminder*"})
	if strings.Join(refs(before), ",") != strings.Join(refs(after), ",") || len(rebuilt) != len(Families) || rebuilt[0].Indexed != 5 {
		t.Fatalf("reindex changed results: %v %v %+v", refs(before), refs(after), rebuilt)
	}
}

func TestSearchRanksTextAndSanitizesSyntax(t *testing.T) {
	f := newFixture(t)
	// Terms are quoted and ANDed, so FTS operators are literal words, not syntax errors.
	literal, err := f.history.Search(f.ctx, f.projectID, SearchOptions{Query: `card* OR NEAR( "`, Limit: 10})
	if err != nil || literal.Returned != 0 {
		t.Fatalf("literal operators = %+v %v", literal, err)
	}
	page, err := f.history.Search(f.ctx, f.projectID, SearchOptions{Query: `syntax" reminder*`, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.Order != "relevance" || strings.Join(refs(page), ",") != "narrative:plan-cards#0" || page.BodiesLoaded != 0 || !strings.Contains(page.Hits[0].Snippet, "[reminders]") {
		t.Fatalf("page = %+v", page)
	}
	if _, err := f.history.Search(f.ctx, f.projectID, SearchOptions{Query: `"*"`}); err == nil {
		t.Fatal("empty term query accepted")
	}
	kinds, err := f.history.Search(f.ctx, f.projectID, SearchOptions{Kinds: []string{"limitation"}})
	if err != nil || strings.Join(refs(kinds), ",") != "narrative:implement-cards#1" {
		t.Fatalf("kind filter = %v %v", refs(kinds), err)
	}
	if _, err := f.history.Search(f.ctx, f.projectID, SearchOptions{}); err == nil {
		t.Fatal("unbounded search accepted")
	}
	small, err := f.history.Search(f.ctx, f.projectID, SearchOptions{Families: []string{"narrative_claim"}, MaxBytes: 256})
	if err != nil || !small.Truncated || small.ContentBytes > 256 || small.Returned >= small.Candidates {
		t.Fatalf("byte budget = %+v %v", small, err)
	}
}

func TestRoadmapTopicHistoryIsChronologicalWithDerivations(t *testing.T) {
	f := newFixture(t)
	page, err := f.history.Search(f.ctx, f.projectID, SearchOptions{Topic: "cards"})
	if err != nil {
		t.Fatal(err)
	}
	want := "narrative:plan-cards#0,narrative:plan-cards#1,narrative:implement-cards#0,narrative:implement-cards#1,decision:cards-memory-jog"
	if got := strings.Join(refs(page), ","); got != want || page.Order != "chronological" {
		t.Fatalf("topic history = %s", got)
	}
	if page.Topic.Kind != "roadmap_node" || strings.Join(page.Topic.Nodes, ",") != "cards,cards-child" {
		t.Fatalf("scope = %+v", page.Topic)
	}
	if page.Hits[0].Derivations[0] != "node_anchor" || page.Hits[2].Derivations[0] != "file_binding" {
		t.Fatalf("derivations = %v %v", page.Hits[0].Derivations, page.Hits[2].Derivations)
	}
	intent, _ := f.history.Search(f.ctx, f.projectID, SearchOptions{Topic: "cards", Kinds: []string{"intent", "decision"}, Families: []string{"narrative_claim"}})
	if strings.Join(refs(intent), ",") != "narrative:plan-cards#0,narrative:plan-cards#1" {
		t.Fatalf("intent/decision history = %v", refs(intent))
	}
	second, err := f.history.Search(f.ctx, f.projectID, SearchOptions{Topic: "cards", Limit: 2, Offset: 2})
	if err != nil || strings.Join(refs(second), ",") != "narrative:implement-cards#0,narrative:implement-cards#1" || !second.Truncated {
		t.Fatalf("offset page = %v %+v %v", refs(second), second, err)
	}
	last, _ := f.history.Search(f.ctx, f.projectID, SearchOptions{Topic: "cards", Limit: 2, Offset: 4})
	if strings.Join(refs(last), ",") != "decision:cards-memory-jog" || last.Truncated {
		t.Fatalf("last page = %v %+v", refs(last), last)
	}
	if _, err := f.history.Search(f.ctx, f.projectID, SearchOptions{Topic: "missing"}); err == nil {
		t.Fatal("unknown topic accepted")
	}
}

func TestLinkedFindsAnchoredClaimsAndLinkingDecisions(t *testing.T) {
	f := newFixture(t)
	for linked, want := range map[string]string{
		"cards-child":                    "narrative:plan-cards#0,narrative:plan-cards#1",
		"roadmap_node:cards":             "decision:cards-memory-jog",
		"file:cmd/main.go":               "narrative:shared-main#0",
		"internal/referencecard/missing": "",
	} {
		page, err := f.history.Search(f.ctx, f.projectID, SearchOptions{Linked: linked})
		if err != nil || strings.Join(refs(page), ",") != want {
			t.Fatalf("linked %s = %v %v", linked, refs(page), err)
		}
	}
}

func TestCuratedTopicUsesAcceptedLinksAndAliases(t *testing.T) {
	f := newFixture(t)
	if _, err := f.topics.Record(f.ctx, topic.Topic{ProjectID: f.projectID, ID: "reference-cards", Title: "Reference cards", Creator: "owner"}); err != nil {
		t.Fatal(err)
	}
	if err := f.topics.Alias(f.ctx, f.projectID, "reference-cards", "Cards-Topic", "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.topics.AddLink(f.ctx, topic.Link{ProjectID: f.projectID, TopicID: "reference-cards", TargetKind: "roadmap_node", TargetID: "cards", Creator: "owner"}); err != nil {
		t.Fatal(err)
	}
	proposed, err := f.topics.AddLink(f.ctx, topic.Link{ProjectID: f.projectID, TopicID: "reference-cards", TargetKind: "work_item", TargetID: "tui-ideas", Creator: "agent", Model: "model-x"})
	if err != nil || proposed.Status != "proposed" || proposed.Authority != "model_suggested" {
		t.Fatalf("model link = %+v %v", proposed, err)
	}
	if _, err := f.topics.AddLink(f.ctx, topic.Link{ProjectID: f.projectID, TopicID: "reference-cards", TargetKind: "work_item", TargetID: "x", Creator: "agent", Model: "model-x", Status: "accepted"}); err == nil {
		t.Fatal("model link was accepted directly")
	}
	page, err := f.history.Search(f.ctx, f.projectID, SearchOptions{Topic: "cards-topic"})
	if err != nil || page.Topic.Kind != "curated" || strings.Contains(strings.Join(refs(page), ","), "work_item:") || page.Returned != 5 {
		t.Fatalf("curated scope before acceptance = %v %+v %v", refs(page), page.Topic, err)
	}
	if _, err := f.topics.AcceptLink(f.ctx, f.projectID, proposed.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	page, _ = f.history.Search(f.ctx, f.projectID, SearchOptions{Topic: "reference-cards", Families: []string{"work_item"}})
	if strings.Join(refs(page), ",") != "work_item:tui-ideas" || page.Hits[0].Derivations[0] != "explicit_link" {
		t.Fatalf("accepted idea link = %v", refs(page))
	}
	detail, err := f.topics.Show(f.ctx, f.projectID, "cards-topic")
	if err != nil || len(detail.Links) != 2 || detail.Aliases[0] != "cards-topic" {
		t.Fatalf("detail = %+v %v", detail, err)
	}
}

func TestShowExpandsExactReferencesWithinBudget(t *testing.T) {
	f := newFixture(t)
	page, err := f.history.Show(f.ctx, f.projectID, []string{"narrative:plan-cards#0", "narrative:implement-cards", "decision:cards-memory-jog", "work_item:missing", "bogus"}, 4096)
	if err != nil {
		t.Fatal(err)
	}
	items := page.Items
	if len(items) != 5 || items[0].Kind != "intent" || !strings.Contains(items[0].Text, "Why") || !strings.Contains(items[0].Text, "Anchors: roadmap_node:cards-child") {
		t.Fatalf("claim item = %+v", items[0])
	}
	if !strings.Contains(items[1].Text, "#1 [limitation — Known limitations") || strings.Contains(items[1].Text, "Anchors") {
		t.Fatalf("narrative item = %+v", items[1])
	}
	if !strings.Contains(items[2].Text, "Links: subject roadmap_node:cards@1") || items[3].Error == "" || items[4].Error == "" {
		t.Fatalf("decision/missing items = %+v", items[2:])
	}
	small, _ := f.history.Show(f.ctx, f.projectID, []string{"narrative:implement-cards", "narrative:plan-cards", "decision:cards-memory-jog"}, 256)
	if !small.Truncated || small.ContentBytes > 256+len("…") {
		t.Fatalf("budget = %+v", small)
	}
}
