package mcpserver

import (
	"encoding/json"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
	"github.com/llewm/llewm/components/continuity/internal/history"
	"github.com/llewm/llewm/components/continuity/internal/lease"
	"github.com/llewm/llewm/components/continuity/internal/presence"
	"github.com/llewm/llewm/components/continuity/internal/procedures"
	"github.com/llewm/llewm/components/continuity/internal/readiness"
	"github.com/llewm/llewm/components/continuity/internal/release"
	"github.com/llewm/llewm/components/continuity/internal/resource"
	"github.com/llewm/llewm/components/continuity/internal/roadmap"
	"github.com/llewm/llewm/components/continuity/internal/usage"
	"github.com/llewm/llewm/components/continuity/internal/workitem"
	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
	"github.com/llewm/llewm/components/continuity/internal/workstate"
)

// inputSchema augments SDK inference with constraints owned by the services.
// Permissions, byte budgets after normalization/redaction, record relationships,
// and state-dependent requirements remain service checks. In particular JSON
// Schema maxLength counts Unicode characters, not the services' UTF-8 bytes.
func inputSchema[T any]() *jsonschema.Schema {
	s, err := jsonschema.For[T](nil)
	if err != nil {
		panic(err)
	}
	p := s.Properties
	switch any(*new(T)).(type) {
	case PinInput:
		p["target"].Pattern = `^($|(resource|procedure|decision|topic):.+)$`
		p["scope"].Pattern = `^($|project|(topic|roadmap_node|task):.+)$`
		p["target"].Description, p["scope"].Description = "", ""
		enum(p["authority"], "", "caller_asserted", "user_reported")
	case UsageInput:
		enum(p["by"], append([]string{""}, usage.Dimensions...)...)
		p["by"].Default = json.RawMessage(`"model"`)
		for _, name := range []string{"since", "until"} {
			p[name].AnyOf = []*jsonschema.Schema{{Enum: []any{""}}, {Format: "date"}, {Format: "date-time"}}
		}
	case InfoInput:
		array(p["probes"], 0, readiness.MaxProbes)
		array(p["source_verifications"], 0, readiness.MaxVerifications)
	case TrackPlanShowInput:
		number(p["limit"], 0, 100, 20)
	case CheckpointInput:
		enum(p["status"], "", "in_progress", "completed", "blocked", "paused")
		for _, name := range []string{"completed", "changes", "verified", "attempts", "evidence_refs", "action_updates", "carry_forward_action_ids"} {
			array(p[name], 0, 64)
		}
		enum(p["action_updates"].Items.Properties["status"], "completed", "superseded")
	case CheckpointActionInput:
		enum(p["status"], "completed", "superseded")
	case CheckpointGetInput:
		number(p["limit"], 0, checkpoint.MaxHistoryLimit, checkpoint.DefaultHistoryLimit)
		byteBudget(p["max_bytes"], 512, checkpoint.MaxBytes, checkpoint.DefaultExportBytes)
	case ProcedureShowInput:
		number(p["limit"], 0, procedures.MaxListLimit, 50)
		enum(p["kind"], "", "check", "completion_check", "runbook")
		enum(p["status"], "", "proposed", "accepted", "superseded")
	case ResourceShowInput:
		enum(p["kind"], append([]string{""}, resource.Kinds...)...)
		enum(p["status"], "", "proposed", "accepted", "retired")
		number(p["limit"], 0, resource.MaxListLimit, 50)
	case LeaseInput:
		enum(p["action"], "acquire", "renew", "release")
		p["scope"].Pattern = `^(task|roadmap_node|track_plan):.+$`
		byteBudget(p["ttl_seconds"], int(lease.MinTTL.Seconds()), int(lease.MaxTTL.Seconds()), int(lease.DefaultTTL.Seconds()))
		p["ttl_seconds"].Description = ""
	case RoadmapNodeInput:
		enum(p["kind"], "", "initiative", "milestone", "work_item", "research")
		enum(p["status"], "proposed", "accepted", "active", "blocked", "completed", "deferred", "superseded")
		number(p["priority"], 0, 100, 50)
		array(p["evidence_refs"], 0, 64)
		p["target_date"].Format = "date"
	case RoadmapViewInput:
		enum(p["view"], "", "status", "overview", "tree", "coverage", "history", "render", "check", "releases", "unreleased", "changelog", "changelog_check")
		// Put bounds inside view conditions so ignored fields keep their semantics.
		viewBound(s, []string{"overview", "coverage", "history"}, roadmap.MaxCoverageLimit, roadmap.DefaultCoverageLimit)
		viewBound(s, []string{"tree"}, roadmap.MaxTreeLimit, roadmap.DefaultTreeLimit)
		viewBound(s, []string{"releases"}, release.MaxLimit, release.DefaultLimit)
	case ReleaseRecordInput:
		enum(p["status"], "draft", "reviewed", "published", "superseded")
		array(p["entries"], 1, release.MaxEntries)
		e := p["entries"].Items.Properties
		enum(e["category"], "feature", "fix", "migration", "operational", "breaking")
		e["node_revision"].Minimum = ptr(float64(1))
		array(e["narrative_ids"], 1, release.MaxNarrativesPerEntry)
		p["published_at"].Format = "date-time"
	case WorkInput:
		number(p["limit"], 0, workstate.MaxLimit, workstate.DefaultLimit)
		number(p["ref_limit"], 0, roadmap.MaxTrackInventoryRefLimit, roadmap.DefaultTrackInventoryRefLimit)
		p["offset"].Minimum = ptr(float64(0))
		p["remind_after_seconds"].Minimum = ptr(float64(0))
		p["remind_after_events"].Minimum, p["remind_after_events"].Maximum = ptr(float64(0)), ptr(float64(checkpoint.MaxReminderEvents))
		enum(p["view"], "", "pulse", "summary", "list", "item", "check", "freshness", "inventory")
		enum(p["list"], "", "current", "open", "active", "next", "blocked", "stale", "actions", "all", "everything")
		enum(p["scope"], "", "current", "all")
		enum(p["filter"], "", "open", "all", "everything", "current_focus", "active", "resumable", "blocked", "anomalous", "stale", "actions")
	case PresenceInput:
		// Nonpositive limits intentionally select the default in the service.
		p["limit"].Maximum = ptr(float64(presence.MaxLimit))
		p["limit"].Default = json.RawMessage(`50`)
		enum(p["state"], "", "planning", "active", "yielded", "completed", "cancelled", "expired")
	case DecisionRecordInput:
		enum(p["disposition"], "accepted", "deferred", "rejected", "superseded")
		enum(p["actor_type"], "human")
		enum(p["authority"], "caller_asserted", "user_reported")
		array(p["links"], 0, 16)
		l := p["links"].Items.Properties
		enum(l["kind"], "subject", "supports", "opposes", "resulted_in", "supersedes")
		enum(l["target_kind"], "task", "checkpoint", "roadmap_node", "track_plan", "work_item", "narrative", "commit", "decision")
		enum(l["authority"], "", "caller_asserted")
	case ItemCaptureInput:
		enum(p["kind"], workitem.Kinds...)
		enum(p["severity"], append([]string{""}, workitem.Severities...)...)
		array(p["tags"], 0, 32)
	case ItemInput:
		number(p["limit"], 0, 100, 20)
		number(p["min_count"], 0, 1000, 2)
		byteBudget(p["max_bytes"], 512, workitem.MaxRenderBytes, workitem.DefaultShowBytes)
		enum(p["kind"], append([]string{""}, workitem.Kinds...)...)
		enum(p["status"], append([]string{"", "pending", "open"}, workitem.Statuses...)...)
	case ItemDispositionInput:
		enum(p["status"], workitem.Statuses...)
		array(p["target_refs"], 0, 32)
	case TrackPlanRecordInput:
		enum(p["status"], "proposed", "accepted", "active", "completed", "superseded")
		p["items"].MinItems, p["items"].MaxItems = ptr(1), ptr(100)
		// null (or omission) selects human review, an empty array is an invalid plan.
		array(p["work_item_ids"], 0, 64)
		it := p["items"].Items.Properties
		enum(it["status"], "pending", "ready", "active", "blocked", "completed", "superseded")
		it["position"].Minimum = ptr(float64(0))
		for _, name := range []string{"depends_on", "evidence_refs", "work_item_ids"} {
			array(it[name], 0, 64)
		}

	case HistorySearchInput:
		enum(s.Properties["state"], "", "current", "superseded", "all")
		s.Properties["state"].Default = json.RawMessage(`"current"`)
		enum(s.Properties["families"].Items, history.Families...)
		number(s.Properties["limit"], 0, history.MaxSearchLimit, history.DefaultSearchLimit)
		number(s.Properties["offset"], 0, history.MaxOffset, 0)
		byteBudget(s.Properties["max_bytes"], history.MinMaxBytes, history.MaxMaxBytes, history.DefaultMaxBytes)
		s.Properties["limit"].Description = "Advance offset to paginate."
	case HistoryShowInput:
		array(s.Properties["refs"], 1, history.MaxShowRefs)
		byteBudget(s.Properties["max_bytes"], history.MinMaxBytes, history.MaxMaxBytes, history.DefaultMaxBytes)
	case NarrativeRecordInput:
		enum(s.Properties["trigger"], worknarrative.Triggers()...)
		array(s.Properties["claims"], 1, worknarrative.MaxClaims)
		array(s.Properties["anchors"], 0, worknarrative.MaxAnchors)
		claim := s.Properties["claims"].Items
		enum(claim.Properties["kind"], worknarrative.ClaimKinds()...)
		enum(claim.Properties["authority"], worknarrative.Authorities()...)
		claim.Properties["label"].MaxLength = ptr(worknarrative.MaxClaimLabelBytes)
		array(claim.Properties["citations"], 0, worknarrative.MaxCitations)
		citation := claim.Properties["citations"].Items
		enum(citation.Properties["relation"], worknarrative.CitationRelations()...)
		citation.Properties["source"].Pattern = `^narrative:[^#\r\n\x00]+$`
		citation.Properties["source"].MaxLength = ptr(len("narrative:") + worknarrative.MaxCitationIDBytes)
		citation.Properties["locator"].MaxLength = ptr(worknarrative.MaxCitationLocatorBytes)
		anchor := s.Properties["anchors"].Items
		enum(anchor.Properties["kind"], worknarrative.AnchorKinds()...)
		enum(anchor.Properties["authority"], worknarrative.Authorities()...)
		hash(anchor.Properties["content_sha256"])
		source := s.Properties["source"]
		enum(source.Properties["authority"], worknarrative.Authorities()...)
		source.Properties["url"].Pattern = `^https?://`
		source.Properties["url"].MaxLength = ptr(worknarrative.MaxSourceURLBytes)
		hash(source.Properties["content_sha256"])
		source.Properties["accessed_at"].Format = "date-time"
		source.Properties["published_at"].AnyOf = []*jsonschema.Schema{{Enum: []any{""}}, {Format: "date"}, {Format: "date-time"}}
		for _, name := range []string{"occurred_from", "occurred_to"} {
			s.Properties[name].Format = "date-time"
		}
		for _, name := range []string{"publisher", "published_at", "version"} {
			source.Properties[name].MaxLength = ptr(worknarrative.MaxSourceMetadataBytes)
		}
		// Trigger is required by Append; RecordCommit supplies it for commit records.
		s.AnyOf = []*jsonschema.Schema{{Required: []string{"trigger"}}, {Required: []string{"commit"}, Properties: map[string]*jsonschema.Schema{"commit": {Type: "string", MinLength: ptr(1)}}}}
		textBounds(s, worknarrative.MaxRecordBytes)
	}
	// Omitted fields and explicit zero values retain service defaults. Inert
	// selectors and views with different bounds are deliberately not generalized.
	return s
}

func viewBound(s *jsonschema.Schema, views []string, max, def int) {
	selector := &jsonschema.Schema{}
	enum(selector, views...)
	limit := &jsonschema.Schema{Type: "integer"}
	number(limit, 0, max, def)
	s.AllOf = append(s.AllOf, &jsonschema.Schema{If: &jsonschema.Schema{Required: []string{"view"}, Properties: map[string]*jsonschema.Schema{"view": selector}}, Then: &jsonschema.Schema{Properties: map[string]*jsonschema.Schema{"limit": limit, "offset": {Minimum: ptr(float64(0))}}}})
}
func ptr[T any](v T) *T { return &v }
func enum(s *jsonschema.Schema, values ...string) {
	s.Type, s.Types, s.Description = "", nil, ""
	for _, v := range values {
		s.Enum = append(s.Enum, v)
	}
}
func number(s *jsonschema.Schema, min, max, def int) {
	s.Minimum, s.Maximum = ptr(float64(min)), ptr(float64(max))
	s.Default, _ = json.Marshal(def)
}
func byteBudget(s *jsonschema.Schema, min, max, def int) {
	s.AnyOf = []*jsonschema.Schema{{Enum: []any{0}}, {Minimum: ptr(float64(min)), Maximum: ptr(float64(max))}}
	s.Default, _ = json.Marshal(def)
}
func array(s *jsonschema.Schema, min, max int) {
	s.MaxItems = ptr(max)
	if min > 0 {
		s.MinItems = ptr(min)
	}
	if min > 0 {
		s.Types, s.Type = nil, "array"
	}
}
func hash(s *jsonschema.Schema) { s.Pattern = `^([0-9a-f]{64})?$` }
func textBounds(s *jsonschema.Schema, max int) {
	for _, name := range s.Required {
		if p := s.Properties[name]; p != nil && p.Type == "string" {
			p.MinLength = ptr(1)
			if p.MaxLength == nil {
				p.MaxLength = ptr(max)
			}
		}
	}
	for _, v := range s.Properties {
		textBounds(v, max)
	}
	if s.Items != nil {
		textBounds(s.Items, max)
	}
}
