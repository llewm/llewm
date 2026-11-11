package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/llewm/llewm/components/continuity/internal/app"
	"github.com/llewm/llewm/components/continuity/internal/history"
	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Exercise tools/list, not a parallel schema fixture: the SDK resolves and
// validates exactly these definitions before dispatching to the services.
func TestMCPInputSchemasMatchServiceConstraints(t *testing.T) {
	ctx, session, root := toolSchemaSession(t)
	application, err := app.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	list, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	schemas := map[string]*jsonschema.Resolved{}
	for _, tool := range list.Tools {
		b, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var s jsonschema.Schema
		if err = json.Unmarshal(b, &s); err != nil {
			t.Fatal(err)
		}
		r, err := s.Resolve(&jsonschema.ResolveOptions{ValidateDefaults: true})
		if err != nil {
			t.Fatal(err)
		}
		schemas[tool.Name] = r
	}
	check := func(t *testing.T, tool string, input any, want bool) {
		t.Helper()
		b, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		var value any
		if err = json.Unmarshal(b, &value); err != nil {
			t.Fatal(err)
		}
		if err = schemas[tool].Validate(value); (err == nil) != want {
			t.Fatalf("%s: valid=%v, want %v: %s: %v", tool, err == nil, want, b, err)
		}
	}
	t.Run("publication selectors", func(t *testing.T) {
		check(t, "continuity_sync_publication", map[string]any{"refs": []string{"checkpoint:one", "narrative:two"}}, true)
		check(t, "continuity_sync_publication", map[string]any{"refs": []string{}}, false)
		check(t, "continuity_sync_publication", map[string]any{"refs": []string{"decision:one"}}, false)
		check(t, "continuity_sync_publication", map[string]any{"refs": []string{"checkpoint:one"}, "envelope_sha256": "wrong"}, false)
	})
	t.Run("history bounds and defaults", func(t *testing.T) {
		for _, limit := range []int{-1, 0, 1, history.MaxSearchLimit, history.MaxSearchLimit + 1} {
			for _, max := range []int{-1, 0, history.MinMaxBytes - 1, history.MinMaxBytes, history.MaxMaxBytes, history.MaxMaxBytes + 1} {
				for _, offset := range []int{-1, 0, history.MaxOffset, history.MaxOffset + 1} {
					input := history.SearchOptions{Query: "schema", Limit: limit, MaxBytes: max, Offset: offset}
					_, err := application.History.Search(ctx, application.ProjectID, input)
					check(t, "continuity_history_search", input, err == nil)
				}
			}
		}
		for _, state := range []string{"", "current", "superseded", "all", "invalid"} {
			input := history.SearchOptions{Query: "schema", State: state}
			_, err := application.History.Search(ctx, application.ProjectID, input)
			check(t, "continuity_history_search", input, err == nil)
		}
		for _, family := range append(append([]string{}, history.Families...), "invalid") {
			input := history.SearchOptions{Families: []string{family}}
			_, err := application.History.Search(ctx, application.ProjectID, input)
			check(t, "continuity_history_search", input, err == nil)
		}
		for _, n := range []int{0, 1, history.MaxShowRefs, history.MaxShowRefs + 1} {
			refs := make([]string, n)
			for i := range refs {
				refs[i] = "narrative:absent"
			}
			_, err := application.History.Show(ctx, application.ProjectID, refs, 0)
			check(t, "continuity_history_show", map[string]any{"refs": refs}, err == nil)
		}
	})
	t.Run("nested narrative vocabulary", func(t *testing.T) {
		base := func() worknarrative.Record {
			return worknarrative.Record{ProjectID: application.ProjectID, Title: "Schema", Trigger: "manual", Creator: "test", Claims: []worknarrative.Claim{{Kind: "note", Text: "Observed", Authority: "caller_asserted"}}}
		}
		compare := func(r worknarrative.Record) {
			t.Helper()
			_, err := application.Narratives.SemanticHash(ctx, r)
			input := map[string]any{"title": r.Title, "trigger": r.Trigger, "creator": r.Creator, "claims": r.Claims, "anchors": r.Anchors, "source": r.Source}
			check(t, "continuity_narrative_record", input, err == nil)
		}
		for _, kind := range append(worknarrative.ClaimKinds(), "invalid") {
			r := base()
			r.Claims[0].Kind = kind
			compare(r)
		}
		for _, authority := range append(worknarrative.Authorities(), "invalid") {
			r := base()
			r.Claims[0].Authority = authority
			compare(r)
		}
		for _, trigger := range append(worknarrative.Triggers(), "invalid") {
			r := base()
			r.Trigger = trigger
			compare(r)
		}
		for _, kind := range append(worknarrative.AnchorKinds(), "decision") {
			r := base()
			r.Anchors = []worknarrative.Anchor{{Kind: kind, Target: "AGENTS.md", ContentSHA256: strings.Repeat("a", 64), Authority: "caller_asserted"}}
			compare(r)
		}
		for _, n := range []int{0, 1, worknarrative.MaxClaims, worknarrative.MaxClaims + 1} {
			r := base()
			r.Claims = make([]worknarrative.Claim, n)
			for i := range r.Claims {
				r.Claims[i] = worknarrative.Claim{Kind: "note", Text: "x", Authority: "caller_asserted"}
			}
			compare(r)
		}
		for _, n := range []int{worknarrative.MaxClaimLabelBytes, worknarrative.MaxClaimLabelBytes + 1} {
			r := base()
			r.Claims[0].Label = strings.Repeat("x", n)
			compare(r)
		}
		for _, n := range []int{worknarrative.MaxAnchors, worknarrative.MaxAnchors + 1} {
			r := base()
			for i := 0; i < n; i++ {
				r.Anchors = append(r.Anchors, worknarrative.Anchor{Kind: "task", Target: "x", Authority: "caller_asserted"})
			}
			compare(r)
		}
		for _, relation := range append(worknarrative.CitationRelations(), "invalid") {
			r := base()
			r.Claims[0].Citations = []worknarrative.Citation{{Source: "narrative:observation", Relation: relation}}
			compare(r)
		}
		for _, n := range []int{worknarrative.MaxCitations, worknarrative.MaxCitations + 1} {
			r := base()
			for i := 0; i < n; i++ {
				r.Claims[0].Citations = append(r.Claims[0].Citations, worknarrative.Citation{Source: "narrative:observation", Relation: "context", Locator: strings.Repeat("x", i)})
			}
			compare(r)
		}
		for _, title := range []string{"", "Observed"} {
			r := base()
			r.Title = title
			compare(r)
		}
		for _, authority := range append(worknarrative.Authorities(), "invalid") {
			r := base()
			r.Source = &worknarrative.Source{URL: "https://example.org/source", AccessedAt: "2026-11-01T00:00:00Z", Authority: authority}
			compare(r)
		}

	})
	t.Run("conditional and nullable inputs", func(t *testing.T) {
		for _, v := range []struct {
			view  string
			limit int
			valid bool
		}{
			{"tree", 64, true}, {"tree", 65, false}, {"coverage", 64, true}, {"coverage", 65, false},
			{"history", 0, true}, {"history", 65, false}, {"releases", 100, true}, {"releases", 101, false},
			{"status", 101, true}, // ignored field: no invented universal maximum
		} {
			check(t, "continuity_roadmap_view", map[string]any{"view": v.view, "limit": v.limit}, v.valid)
		}
		review := map[string]any{"id": "p", "status": "accepted", "creator": "human", "items": nil}
		check(t, "continuity_track_plan_record", review, true)
		review["items"] = []any{}
		check(t, "continuity_track_plan_record", review, false)
		check(t, "continuity_narrative_record", map[string]any{"title": "commit", "creator": "test", "commit": "HEAD", "claims": []worknarrative.Claim{{Kind: "note", Text: "x", Authority: "caller_asserted"}}}, true)
		check(t, "continuity_info", map[string]any{"probes": make([]string, 17)}, false)
	})

	t.Run("error response precedes success decoding", func(t *testing.T) {
		for _, input := range []map[string]any{{"query": "schema", "limit": 100}, {"query": "!!!"}} {
			result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "continuity_history_search", Arguments: input})
			if err != nil {
				t.Fatal(err)
			}
			if !result.IsError || result.StructuredContent != nil {
				t.Fatalf("error surfaced as success: %+v", result)
			}
		}
	})
}
