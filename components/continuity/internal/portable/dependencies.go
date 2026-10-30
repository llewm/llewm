package portable

import (
	"sort"
	"strings"
)

// ImportDependency names records whose presence/content can affect canonical
// import validation. It is a selector, not an assertion that the target exists.
// Exact includes revision zero; Any selects all revisions; Prior selects every
// revision below Revision so a newly arriving prior head invalidates the query.
type ImportDependency struct {
	RecordType string
	RecordID   string
	Mode       string
	Revision   int
}

const (
	DependencyExact = "exact"
	DependencyAny   = "any"
	DependencyPrior = "prior"
)

// ImportDependencies uses the canonical payload decoders, not informational
// wire references. In particular, plan predecessor/comparison revisions are
// dependencies even though they are absent from the envelope's reference list.
// The boolean is false for unknown, malformed or recovery-sensitive records;
// callers must not interpret that case as an independent record. This describes
// the inputs to validateApplyDependencies, which remains the semantic authority.
func ImportDependencies(e Envelope) ([]ImportDependency, bool) {
	if !SupportedEnvelope(e) || RetiredRecordType(e.RecordType) {
		return nil, false
	}
	var result []ImportDependency
	add := func(kind, id, mode string, revision int) {
		if id != "" {
			result = append(result, ImportDependency{kind, id, mode, revision})
		}
	}
	exact := func(kind, id string, revision int) { add(kind, id, DependencyExact, revision) }
	any := func(kind, id string) { add(kind, id, DependencyAny, 0) }
	// Supersession is decoded jointly with its successor by the importer. Keep
	// both sides in the dependency graph without requiring them in this block.
	if e.RecordType == "work_narrative_supersession" {
		var p supersessionPayload
		if strictDecode(e.Payload, &p) != nil || p.SuccessorID != e.RecordID || p.SupersedesID == "" || p.SuccessorID == p.SupersedesID || e.RecordRevision != 0 || e.Authority != "mixed" || !sameReferences(e.Refs, []Reference{{Kind: "narrative", ID: p.SuccessorID}, {Kind: "narrative", ID: p.SupersedesID}}) {
			return nil, false
		}
		exact("work_narrative", p.SuccessorID, 0)
		exact("work_narrative", p.SupersedesID, 0)
	} else {
		d, err := decodeOperationalBundle(Bundle{Records: []EncodedEnvelope{{Envelope: e}}})
		if err != nil {
			return nil, false
		}
		switch e.RecordType {
		case "checkpoint", "roadmap_node", "roadmap_review_routine", "work_item", "topic_revision", "resource", "procedure", "pin", "coverage_cell", "lease_stamp", "stale_write_resolution", "usage_session_summary":
			// Informational targets may arrive later and do not gate import.
		case "decision":
			exact("decision", d.ledger.Decisions[e.RecordID].SupersedesID, 0)
		case "topic_alias":
			any("topic_revision", d.ledger.TopicAliases[e.RecordID].TopicID)
		case "topic_link":
			v := d.ledger.TopicLinks[e.RecordID]
			any("topic_revision", v.TopicID)
			exact("topic_link", v.AcceptedFrom, 0)
		case "resource_note":
			for _, v := range d.resources.Notes {
				any("resource", v.ResourceID)
			}
		case "checkpoint_action":
			exact("checkpoint", d.actions[e.RecordID].SourceCheckpointID, 0)
		case "checkpoint_action_transition":
			v := d.transitions[e.RecordID]
			exact("checkpoint_action", v.ActionID, 0)
			exact("checkpoint", v.CheckpointID, 0)
		case "checkpoint_context":
			for _, v := range d.contexts {
				exact("checkpoint", v.CheckpointID, 0)
			}
		case "checkpoint_branches":
			for _, v := range d.branches {
				exact("checkpoint", v.CheckpointID, 0)
			}
		case "roadmap_binding":
			v := d.roadmap.Bindings[e.RecordID]
			exact("roadmap_node", v.NodeID, v.NodeRevision)
		case "roadmap_link":
			v := d.roadmap.Links[e.RecordID]
			exact("roadmap_node", v.NodeID, v.NodeRevision)
			any("roadmap_node", v.TargetNodeID)
		case "roadmap_sync":
			v := d.roadmap.Syncs[e.RecordID]
			for _, id := range v.ReviewedNodes {
				any("roadmap_node", id)
			}
			exact("actor_invocation", v.InvocationID, 0)
		case "roadmap_release":
			for _, v := range d.releases {
				for _, entry := range v.Entries {
					exact("roadmap_node", entry.NodeID, entry.NodeRevision)
					for _, id := range entry.NarrativeIDs {
						exact("work_narrative", id, 0)
					}
				}
				if v.Status == "published" {
					add("roadmap_release", v.ID, DependencyPrior, v.Revision)
				}
			}
		case "work_item_disposition":
			exact("work_item", d.reviews.Dispositions[e.RecordID].ItemID, 0)
		case "work_item_link":
			exact("work_item", d.reviews.Links[e.RecordID].ItemID, 0)
		case "track_plan":
			for _, v := range d.trackPlans.Plans {
				if v.Revision > 1 {
					exact("track_plan", v.ID, v.Revision-1)
				}
				if v.Change != nil && v.Change.ComparisonRevision > 0 {
					exact("track_plan", v.ID, v.Change.ComparisonRevision)
				}
				for _, id := range v.WorkItemIDs {
					exact("work_item", id, 0)
				}
			}
		case "actor_invocation":
			exact("actor_invocation", d.invocations[e.RecordID].ParentInvocationID, 0)
		case "work_narrative":
			v := d.workNarratives[e.RecordID]
			exact("actor_invocation", v.CreatorInvocationID, 0)
			for _, claim := range v.Claims {
				for _, citation := range claim.Citations {
					exact("work_narrative", strings.TrimPrefix(citation.Source, "narrative:"), 0)
				}
			}
			// If an edge exists it must be interpreted with its successor. An
			// absent edge is not an error; selectors do not assert existence.
			exact("work_narrative_supersession", e.RecordID, 0)
		default:
			return nil, false
		}
	}
	sort.Slice(result, func(i, j int) bool {
		a, b := result[i], result[j]
		if a.RecordType != b.RecordType {
			return a.RecordType < b.RecordType
		}
		if a.RecordID != b.RecordID {
			return a.RecordID < b.RecordID
		}
		if a.Mode != b.Mode {
			return a.Mode < b.Mode
		}
		return a.Revision < b.Revision
	})
	unique := result[:0]
	for _, v := range result {
		if len(unique) == 0 || unique[len(unique)-1] != v {
			unique = append(unique, v)
		}
	}
	return unique, true
}
