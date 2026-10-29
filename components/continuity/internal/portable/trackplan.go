package portable

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/trackplan"
)

const trackPlanTextMaxBytes = 16 << 10

type trackPlanPayload struct {
	Change                   *trackplan.Change `json:"change,omitempty"`
	RoadmapNodeID            string            `json:"roadmap_node_id"`
	TaskID                   string            `json:"task_id,omitempty"`
	Title                    string            `json:"title"`
	Summary                  string            `json:"summary"`
	Status                   string            `json:"status"`
	Creator                  string            `json:"creator"`
	Model                    string            `json:"model,omitempty"`
	WorkItemIDs              []string          `json:"work_item_ids"`
	Items                    []trackplan.Item  `json:"items"`
	OperationalSchemaVersion int               `json:"operational_schema_version"`
}

type portableTrackPlanRecords struct {
	Plans map[string]trackplan.Plan
}

func newPortableTrackPlanRecords() portableTrackPlanRecords {
	return portableTrackPlanRecords{Plans: make(map[string]trackplan.Plan)}
}

func loadTrackPlanSpecs(ctx context.Context, source queryer, projectID, projectRoot, lineageID string) ([]EnvelopeSpec, error) {
	result := make([]EnvelopeSpec, 0)
	// Plan revisions often repeat unchanged items. Reuse only deterministic
	// text transformations within this export; never persist the input cache.
	text := cachedTrackText(projectID, projectRoot)
	planRows, err := exportRows(ctx, source, "track_plan_revisions", `SELECT id,revision FROM track_plan_revisions WHERE project_id=? ORDER BY id,revision`, projectID)
	if err != nil {
		return nil, err
	}
	for planRows.Next() {
		var id string
		var revision int
		if err := planRows.Scan(&id, &revision); err != nil {
			planRows.Close()
			return nil, err
		}
		value, err := loadTrackPlan(ctx, source, projectID, id, revision)
		if err != nil {
			planRows.Close()
			return nil, err
		}
		spec, err := trackPlanSpecWithText(lineageID, projectID, projectRoot, value, text)
		if err != nil {
			planRows.Close()
			return nil, err
		}
		result = append(result, spec)
	}
	if err := planRows.Err(); err != nil {
		planRows.Close()
		return nil, err
	}
	return result, planRows.Close()
}

func loadTrackPlan(ctx context.Context, source queryer, projectID, id string, revision int) (trackplan.Plan, error) {
	var value trackplan.Plan
	var recorded string
	var change []byte
	if err := source.QueryRowContext(ctx, `SELECT id,revision,roadmap_node_id,COALESCE(task_id,''),title,summary,status,creator,COALESCE(model,''),authority,recorded_at,schema_version,change_json FROM track_plan_revisions WHERE project_id=? AND id=? AND revision=?`, projectID, id, revision).Scan(&value.ID, &value.Revision, &value.RoadmapNodeID, &value.TaskID, &value.Title, &value.Summary, &value.Status, &value.Creator, &value.Model, &value.Authority, &recorded, &value.SchemaVersion, &change); err != nil {
		return value, err
	}
	if len(change) > 0 {
		if err := json.Unmarshal(change, &value.Change); err != nil {
			return value, err
		}
	}
	value.ProjectID = projectID
	var err error
	value.RecordedAt, err = time.Parse(time.RFC3339Nano, recorded)
	if err != nil {
		return value, err
	}
	rows, err := source.QueryContext(ctx, `SELECT id,position,title,detail,status,COALESCE(parent_id,''),evidence_refs_json,work_item_refs_json,progress_note,acceptance_criteria FROM track_plan_items WHERE project_id=? AND plan_id=? AND plan_revision=? ORDER BY position,id`, projectID, id, revision)
	if err != nil {
		return value, err
	}
	for rows.Next() {
		var item trackplan.Item
		var evidence, workItems string
		if err := rows.Scan(&item.ID, &item.Position, &item.Title, &item.Detail, &item.Status, &item.ParentID, &evidence, &workItems, &item.ProgressNote, &item.AcceptanceCriteria); err != nil {
			rows.Close()
			return value, err
		}
		if err := json.Unmarshal([]byte(evidence), &item.EvidenceRefs); err != nil {
			rows.Close()
			return value, err
		}
		if err := json.Unmarshal([]byte(workItems), &item.WorkItemIDs); err != nil {
			rows.Close()
			return value, err
		}
		dependencyRows, err := source.QueryContext(ctx, `SELECT depends_on_item_id FROM track_plan_item_dependencies WHERE project_id=? AND plan_id=? AND plan_revision=? AND item_id=? ORDER BY depends_on_item_id`, projectID, id, revision, item.ID)
		if err != nil {
			rows.Close()
			return value, err
		}
		for dependencyRows.Next() {
			var dependency string
			if err := dependencyRows.Scan(&dependency); err != nil {
				dependencyRows.Close()
				rows.Close()
				return value, err
			}
			item.DependsOn = append(item.DependsOn, dependency)
		}
		if err := dependencyRows.Err(); err != nil {
			dependencyRows.Close()
			rows.Close()
			return value, err
		}
		dependencyRows.Close()
		value.Items = append(value.Items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return value, err
	}
	rows.Close()
	itemRows, err := source.QueryContext(ctx, `SELECT item_id FROM track_plan_work_items WHERE project_id=? AND plan_id=? AND plan_revision=? ORDER BY item_id`, projectID, id, revision)
	if err != nil {
		return value, err
	}
	defer itemRows.Close()
	for itemRows.Next() {
		var itemID string
		if err := itemRows.Scan(&itemID); err != nil {
			return value, err
		}
		value.WorkItemIDs = append(value.WorkItemIDs, itemID)
	}
	return value, itemRows.Err()
}

func trackPlanSpec(lineageID, projectID, projectRoot string, value trackplan.Plan) (EnvelopeSpec, error) {
	return trackPlanSpecWithText(lineageID, projectID, projectRoot, value, func(value string) string {
		return portableTrackText(value, projectID, projectRoot)
	})
}

func trackPlanSpecWithText(lineageID, projectID, projectRoot string, value trackplan.Plan, text func(string) string) (EnvelopeSpec, error) {
	stringsWithText := func(values []string) []string {
		result := make([]string, len(values))
		for index, value := range values {
			result[index] = text(value)
		}
		return result
	}
	if !portableCheckpointIdentifier(value.ID, projectID, projectRoot) || !portableCheckpointIdentifier(value.RoadmapNodeID, projectID, projectRoot) {
		return EnvelopeSpec{}, fmt.Errorf("track plan %q contains a non-portable identity", value.ID)
	}
	for _, itemID := range value.WorkItemIDs {
		if !portableCheckpointIdentifier(itemID, projectID, projectRoot) {
			return EnvelopeSpec{}, fmt.Errorf("track plan %q contains a non-portable work item ID", value.ID)
		}
	}
	items := append([]trackplan.Item{}, value.Items...)
	for index := range items {
		if !portableCheckpointIdentifier(items[index].ID, projectID, projectRoot) || (items[index].ParentID != "" && !portableCheckpointIdentifier(items[index].ParentID, projectID, projectRoot)) {
			return EnvelopeSpec{}, fmt.Errorf("track plan %q contains a non-portable item ID", value.ID)
		}
		for _, identifier := range append(append([]string{}, items[index].DependsOn...), items[index].WorkItemIDs...) {
			if !portableCheckpointIdentifier(identifier, projectID, projectRoot) {
				return EnvelopeSpec{}, fmt.Errorf("track plan %q contains a non-portable item reference", value.ID)
			}
		}
		items[index].ProgressNote = text(items[index].ProgressNote)
		items[index].AcceptanceCriteria = text(items[index].AcceptanceCriteria)
		items[index].Title = text(items[index].Title)
		items[index].Detail = text(items[index].Detail)
		items[index].EvidenceRefs = stringsWithText(items[index].EvidenceRefs)
		items[index].WorkItemIDs = stringsWithText(items[index].WorkItemIDs)
	}
	payload := trackPlanPayload{Change: value.Change, RoadmapNodeID: value.RoadmapNodeID, TaskID: text(value.TaskID), Title: text(value.Title), Summary: text(value.Summary), Status: value.Status, Creator: text(value.Creator), Model: text(value.Model), WorkItemIDs: stringsWithText(value.WorkItemIDs), Items: items, OperationalSchemaVersion: value.SchemaVersion}
	body, err := json.Marshal(payload)
	if err != nil {
		return EnvelopeSpec{}, err
	}
	index, err := json.Marshal(envelopeIndex{Title: payload.Title, Status: payload.Status, TaskID: payload.TaskID})
	if err != nil {
		return EnvelopeSpec{}, err
	}
	spec := EnvelopeSpec{LineageID: lineageID, RecordType: "track_plan", RecordSchemaVersion: 1, RecordID: value.ID, RecordRevision: value.Revision, RecordedAt: value.RecordedAt, Authority: value.Authority, Index: index, Refs: trackPlanRefs(payload), Payload: body}
	return spec, validateTrackPlanSpec(spec, payload)
}

func validateTrackPlanSpec(spec EnvelopeSpec, payload any) error {
	encoded, err := EncodeEnvelope(spec)
	if err != nil {
		return err
	}
	switch value := payload.(type) {
	case trackPlanPayload:
		_, err = decodeTrackPlan(encoded.Envelope, value)
	default:
		err = fmt.Errorf("unsupported track-plan payload validation type %T", payload)
	}
	return err
}

func decodeTrackPlan(envelope Envelope, payload trackPlanPayload) (trackplan.Plan, error) {
	if envelope.RecordRevision < 1 || !portableIncomingIdentifier(envelope.RecordID) || payload.OperationalSchemaVersion != trackplan.SchemaVersion || !portableIncomingIdentifier(payload.RoadmapNodeID) || !validTrackPlanText(payload.TaskID, false) || !validTrackPlanText(payload.Title, true) || !validTrackPlanText(payload.Summary, true) || !validTrackPlanText(payload.Creator, true) || !validTrackPlanText(payload.Model, false) || !oneOfValue(payload.Status, "proposed", "accepted", "active", "completed", "superseded") || !oneOfValue(envelope.Authority, "model_suggested", "caller_asserted") || (envelope.Authority == "model_suggested" && (payload.Model == "" || (payload.Status != "proposed" && (payload.Change == nil || !oneOfValue(payload.Change.Kind, "progress", "delegated"))))) || (envelope.Authority == "caller_asserted" && payload.Model != "") || !validTrackPlanStrings(payload.WorkItemIDs) || len(payload.Items) == 0 || len(payload.Items) > 100 {
		return trackplan.Plan{}, fmt.Errorf("track plan %s/%d is invalid", envelope.RecordID, envelope.RecordRevision)
	}
	if !validPortablePlanItems(payload.Items) || !sameReferences(envelope.Refs, trackPlanRefs(payload)) || !checkpointIndexMatches(envelope.Index, envelopeIndex{Title: payload.Title, Status: payload.Status, TaskID: payload.TaskID}) {
		return trackplan.Plan{}, fmt.Errorf("track plan %s/%d metadata or items are invalid", envelope.RecordID, envelope.RecordRevision)
	}
	recorded, err := parsePortableTime(envelope.RecordedAt)
	if (payload.Change != nil && oneOfValue(payload.Change.Kind, "progress", "delegated")) && (payload.Model == "" || !oneOfValue(payload.Status, "accepted", "active", "completed", "superseded")) {
		return trackplan.Plan{}, fmt.Errorf("autonomous plan provenance missing")
	}
	return trackplan.Plan{Change: payload.Change, ID: envelope.RecordID, Revision: envelope.RecordRevision, RoadmapNodeID: payload.RoadmapNodeID, TaskID: payload.TaskID, Title: payload.Title, Summary: payload.Summary, Status: payload.Status, Creator: payload.Creator, Model: payload.Model, Authority: envelope.Authority, WorkItemIDs: append([]string{}, payload.WorkItemIDs...), Items: append([]trackplan.Item{}, payload.Items...), RecordedAt: recorded, SchemaVersion: payload.OperationalSchemaVersion}, err
}

func validPortablePlanItems(items []trackplan.Item) bool {
	ids, positions := map[string]bool{}, map[int]bool{}
	for _, item := range items {
		if !portableIncomingIdentifier(item.ID) || item.Position < 0 || positions[item.Position] || !validTrackPlanText(item.Title, true) || !validTrackPlanText(item.ProgressNote, false) || !validTrackPlanText(item.AcceptanceCriteria, false) || !validTrackPlanText(item.Detail, false) || !oneOfValue(item.Status, "pending", "ready", "active", "blocked", "completed", "superseded") || !validTrackPlanText(item.ParentID, false) || !validTrackPlanStrings(item.DependsOn) || !validTrackPlanStrings(item.EvidenceRefs) || !validTrackPlanStrings(item.WorkItemIDs) || ids[item.ID] {
			return false
		}
		positions[item.Position], ids[item.ID] = true, true
	}
	parents, dependencies := map[string]string{}, map[string][]string{}
	for _, item := range items {
		parents[item.ID], dependencies[item.ID] = item.ParentID, item.DependsOn
		if item.ParentID != "" && (!ids[item.ParentID] || item.ParentID == item.ID) {
			return false
		}
		for _, dependency := range item.DependsOn {
			if !ids[dependency] || dependency == item.ID {
				return false
			}
		}
	}
	visiting, visited := map[string]bool{}, map[string]bool{}
	var visit func(string) bool
	visit = func(id string) bool {
		if visiting[id] {
			return false
		}
		if visited[id] {
			return true
		}
		visiting[id] = true
		for _, dependency := range dependencies[id] {
			if !visit(dependency) {
				return false
			}
		}
		delete(visiting, id)
		visited[id] = true
		return true
	}
	for id := range ids {
		if !visit(id) {
			return false
		}
		seen := map[string]bool{}
		for current := id; current != ""; current = parents[current] {
			if seen[current] {
				return false
			}
			seen[current] = true
		}
	}
	return true
}

func trackPlanRefs(payload trackPlanPayload) []Reference {
	refs := []Reference{{Kind: "roadmap_node", ID: payload.RoadmapNodeID}}
	if payload.TaskID != "" {
		refs = append(refs, Reference{Kind: "task", ID: payload.TaskID})
	}
	seen := map[string]bool{}
	for _, id := range payload.WorkItemIDs {
		seen[id] = true
	}
	for _, item := range payload.Items {
		for _, id := range item.WorkItemIDs {
			seen[id] = true
		}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		refs = append(refs, Reference{Kind: "work_item", ID: id})
	}
	return refs
}

func portableTrackText(value, projectID, projectRoot string) string {
	return portableCheckpointText(value, projectID, projectRoot)
}
func cachedTrackText(projectID, projectRoot string) func(string) string {
	cache := make(map[string]string)
	return func(value string) string {
		if cached, ok := cache[value]; ok {
			return cached
		}
		converted := portableTrackText(value, projectID, projectRoot)
		if len(cache) < 1024 && len(value) <= 16<<10 {
			cache[value] = converted
		}
		return converted
	}
}
func validTrackPlanText(value string, required bool) bool {
	return (!required || value != "") && len(value) <= trackPlanTextMaxBytes && strings.TrimSpace(value) == value && strings.IndexByte(value, 0) < 0 && redactSafe(value)
}
func redactSafe(value string) bool { return portableText(value, "") == value }
func validTrackPlanStrings(values []string) bool {
	if len(values) > 64 {
		return false
	}
	for index, value := range values {
		if !validTrackPlanText(value, true) || (index > 0 && values[index-1] >= value) {
			return false
		}
	}
	return true
}
