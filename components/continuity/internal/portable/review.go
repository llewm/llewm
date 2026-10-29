package portable

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/redact"
	"github.com/llewm/llewm/components/continuity/internal/workitem"
)

const reviewTextMaxBytes = 16 << 10

type workItemPayload struct {
	Kind                     string   `json:"kind"`
	Title                    string   `json:"title"`
	Body                     string   `json:"body"`
	Observed                 string   `json:"observed,omitempty"`
	Reproduction             string   `json:"reproduction,omitempty"`
	Environment              string   `json:"environment,omitempty"`
	Severity                 string   `json:"severity,omitempty"`
	TaskID                   string   `json:"task_id,omitempty"`
	CheckpointID             string   `json:"checkpoint_id,omitempty"`
	RoadmapNodeID            string   `json:"roadmap_node_id,omitempty"`
	Creator                  string   `json:"creator"`
	Model                    string   `json:"model,omitempty"`
	Tags                     []string `json:"tags"`
	OperationalSchemaVersion int      `json:"operational_schema_version"`
}

type workItemDispositionPayload struct {
	ItemID                   string   `json:"item_id"`
	Status                   string   `json:"status"`
	Reason                   string   `json:"reason"`
	Creator                  string   `json:"creator"`
	Model                    string   `json:"model,omitempty"`
	TargetRefs               []string `json:"target_refs"`
	OperationalSchemaVersion int      `json:"operational_schema_version"`
}

type workItemLinkPayload struct {
	ItemID                   string `json:"item_id"`
	Relationship             string `json:"relationship"`
	TargetRef                string `json:"target_ref"`
	Creator                  string `json:"creator"`
	OperationalSchemaVersion int    `json:"operational_schema_version"`
}

type portableReviewRecords struct {
	Items        map[string]workitem.Item
	Dispositions map[string]workitem.Disposition
	Links        map[string]workitem.Link
}

func newPortableReviewRecords() portableReviewRecords {
	return portableReviewRecords{Items: make(map[string]workitem.Item), Dispositions: make(map[string]workitem.Disposition), Links: make(map[string]workitem.Link)}
}

func loadReviewSpecs(ctx context.Context, source queryer, projectID, projectRoot, lineageID string) ([]EnvelopeSpec, error) {
	result := make([]EnvelopeSpec, 0)
	rows, err := exportRows(ctx, source, "work_items", `SELECT id,kind,title,body,COALESCE(observed,''),COALESCE(reproduction,''),COALESCE(environment,''),COALESCE(severity,''),COALESCE(task_id,''),COALESCE(checkpoint_id,''),COALESCE(roadmap_node_id,''),creator,COALESCE(model,''),authority,recorded_at,schema_version FROM work_items WHERE project_id=? ORDER BY recorded_at,id`, projectID)
	if err != nil {
		return nil, err
	}
	items := []workitem.Item{}
	for rows.Next() {
		var value workitem.Item
		var recorded string
		if err := rows.Scan(&value.ID, &value.Kind, &value.Title, &value.Body, &value.Observed, &value.Reproduction, &value.Environment, &value.Severity, &value.TaskID, &value.CheckpointID, &value.RoadmapNodeID, &value.Creator, &value.Model, &value.Authority, &recorded, &value.SchemaVersion); err != nil {
			rows.Close()
			return nil, err
		}
		value.ProjectID = projectID
		if value.RecordedAt, err = time.Parse(time.RFC3339Nano, recorded); err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, value)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, value := range items {
		if value.Tags, err = loadWorkItemTags(ctx, source, projectID, value.ID); err != nil {
			return nil, err
		}
		spec, err := workItemSpec(lineageID, projectID, projectRoot, value)
		if err != nil {
			return nil, err
		}
		result = append(result, spec)
	}

	dispositionRows, err := exportRows(ctx, source, "work_item_dispositions", `SELECT id,item_id,status,reason,target_refs_json,creator,COALESCE(model,''),authority,recorded_at,schema_version FROM work_item_dispositions WHERE project_id=? ORDER BY recorded_at,id`, projectID)
	if err != nil {
		return nil, err
	}
	for dispositionRows.Next() {
		var value workitem.Disposition
		var refs, recorded string
		if err := dispositionRows.Scan(&value.ID, &value.ItemID, &value.Status, &value.Reason, &refs, &value.Creator, &value.Model, &value.Authority, &recorded, &value.SchemaVersion); err != nil {
			dispositionRows.Close()
			return nil, err
		}
		value.ProjectID = projectID
		if err := json.Unmarshal([]byte(refs), &value.TargetRefs); err != nil {
			dispositionRows.Close()
			return nil, fmt.Errorf("decode work item disposition targets %s: %w", value.ID, err)
		}
		if value.RecordedAt, err = time.Parse(time.RFC3339Nano, recorded); err != nil {
			dispositionRows.Close()
			return nil, err
		}
		spec, err := workItemDispositionSpec(lineageID, projectID, projectRoot, value)
		if err != nil {
			dispositionRows.Close()
			return nil, err
		}
		result = append(result, spec)
	}
	dispositionRows.Close()
	if err := dispositionRows.Err(); err != nil {
		return nil, err
	}

	linkRows, err := exportRows(ctx, source, "work_item_links", `SELECT id,item_id,relationship,target_ref,creator,authority,recorded_at,schema_version FROM work_item_links WHERE project_id=? ORDER BY recorded_at,id`, projectID)
	if err != nil {
		return nil, err
	}
	defer linkRows.Close()
	for linkRows.Next() {
		var value workitem.Link
		var recorded string
		if err := linkRows.Scan(&value.ID, &value.ItemID, &value.Relationship, &value.TargetRef, &value.Creator, &value.Authority, &recorded, &value.SchemaVersion); err != nil {
			return nil, err
		}
		value.ProjectID = projectID
		if value.RecordedAt, err = time.Parse(time.RFC3339Nano, recorded); err != nil {
			return nil, err
		}
		spec, err := workItemLinkSpec(lineageID, projectID, projectRoot, value)
		if err != nil {
			return nil, err
		}
		result = append(result, spec)
	}
	return result, linkRows.Err()
}

func loadWorkItemTags(ctx context.Context, source queryer, projectID, itemID string) ([]string, error) {
	rows, err := source.QueryContext(ctx, `SELECT tag FROM work_item_tags WHERE project_id=? AND item_id=? ORDER BY tag`, projectID, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]string, 0)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func workItemSpec(lineageID, projectID, projectRoot string, value workitem.Item) (EnvelopeSpec, error) {
	text := func(v string) string { return portableReviewText(v, projectID, projectRoot) }
	payload := workItemPayload{
		Kind: value.Kind, Title: text(value.Title), Body: text(value.Body), Observed: text(value.Observed),
		Reproduction: text(value.Reproduction), Environment: text(value.Environment), Severity: value.Severity,
		TaskID: text(value.TaskID), CheckpointID: text(value.CheckpointID), RoadmapNodeID: text(value.RoadmapNodeID),
		Creator: text(value.Creator), Model: text(value.Model), Tags: portableReviewStrings(value.Tags, projectID, projectRoot),
		OperationalSchemaVersion: value.SchemaVersion,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return EnvelopeSpec{}, err
	}
	index, err := json.Marshal(workItemIndex(payload))
	if err != nil {
		return EnvelopeSpec{}, err
	}
	spec := EnvelopeSpec{LineageID: lineageID, RecordType: "work_item", RecordSchemaVersion: 1, RecordID: value.ID, RecordedAt: value.RecordedAt, Authority: value.Authority, Index: index, Refs: workItemRefs(payload), Payload: body}
	return spec, validateReviewSpec(spec, payload)
}

func workItemDispositionSpec(lineageID, projectID, projectRoot string, value workitem.Disposition) (EnvelopeSpec, error) {
	payload := workItemDispositionPayload{ItemID: value.ItemID, Status: value.Status, Reason: portableReviewText(value.Reason, projectID, projectRoot), Creator: portableReviewText(value.Creator, projectID, projectRoot), Model: portableReviewText(value.Model, projectID, projectRoot), TargetRefs: portableReviewStringsUnsorted(value.TargetRefs, projectID, projectRoot), OperationalSchemaVersion: value.SchemaVersion}
	body, err := json.Marshal(payload)
	if err != nil {
		return EnvelopeSpec{}, err
	}
	index, err := json.Marshal(envelopeIndex{Title: payload.ItemID, Status: payload.Status})
	if err != nil {
		return EnvelopeSpec{}, err
	}
	spec := EnvelopeSpec{LineageID: lineageID, RecordType: "work_item_disposition", RecordSchemaVersion: 1, RecordID: value.ID, RecordedAt: value.RecordedAt, Authority: value.Authority, Index: index, Refs: workItemDispositionRefs(payload), Payload: body}
	return spec, validateReviewSpec(spec, payload)
}

func workItemLinkSpec(lineageID, projectID, projectRoot string, value workitem.Link) (EnvelopeSpec, error) {
	payload := workItemLinkPayload{ItemID: value.ItemID, Relationship: value.Relationship, TargetRef: portableReviewText(value.TargetRef, projectID, projectRoot), Creator: portableReviewText(value.Creator, projectID, projectRoot), OperationalSchemaVersion: value.SchemaVersion}
	body, err := json.Marshal(payload)
	if err != nil {
		return EnvelopeSpec{}, err
	}
	index, err := json.Marshal(envelopeIndex{Title: payload.ItemID, Status: payload.Relationship})
	if err != nil {
		return EnvelopeSpec{}, err
	}
	spec := EnvelopeSpec{LineageID: lineageID, RecordType: "work_item_link", RecordSchemaVersion: 1, RecordID: value.ID, RecordedAt: value.RecordedAt, Authority: value.Authority, Index: index, Refs: workItemLinkRefs(payload), Payload: body}
	return spec, validateReviewSpec(spec, payload)
}

func validateReviewSpec(spec EnvelopeSpec, payload any) error {
	encoded, err := EncodeEnvelope(spec)
	if err != nil {
		return err
	}
	switch value := payload.(type) {
	case workItemPayload:
		_, err = decodeWorkItem(encoded.Envelope, value)
	case workItemDispositionPayload:
		_, err = decodeWorkItemDisposition(encoded.Envelope, value)
	case workItemLinkPayload:
		_, err = decodeWorkItemLink(encoded.Envelope, value)
	default:
		err = fmt.Errorf("unsupported review payload validation type %T", payload)
	}
	return err
}

func workItemIndex(payload workItemPayload) envelopeIndex {
	tags := payload.Tags
	if len(tags) == 0 {
		tags = nil
	}
	return envelopeIndex{Title: payload.Title, Status: payload.Kind, Tags: tags, TaskID: payload.TaskID}
}

func decodeWorkItem(envelope Envelope, payload workItemPayload) (workitem.Item, error) {
	invalid := fmt.Errorf("work item %s is invalid", envelope.RecordID)
	if envelope.RecordRevision != 0 || !portableIncomingIdentifier(envelope.RecordID) || payload.OperationalSchemaVersion != workitem.SchemaVersion {
		return workitem.Item{}, invalid
	}
	for _, field := range []struct {
		value    string
		required bool
	}{{payload.Title, true}, {payload.Body, true}, {payload.Observed, false}, {payload.Reproduction, false}, {payload.Environment, false}, {payload.TaskID, false}, {payload.CheckpointID, false}, {payload.RoadmapNodeID, false}, {payload.Creator, true}, {payload.Model, false}} {
		if !validReviewText(field.value, field.required) {
			return workitem.Item{}, invalid
		}
	}
	for index, tag := range payload.Tags {
		if !validReviewText(tag, true) || index > 0 && payload.Tags[index-1] >= tag {
			return workitem.Item{}, fmt.Errorf("work item %s has invalid or unsorted tags", envelope.RecordID)
		}
	}
	value := workitem.Item{ID: envelope.RecordID, Kind: payload.Kind, Title: payload.Title, Body: payload.Body, Observed: payload.Observed, Reproduction: payload.Reproduction, Environment: payload.Environment, Severity: payload.Severity, TaskID: payload.TaskID, CheckpointID: payload.CheckpointID, RoadmapNodeID: payload.RoadmapNodeID, Creator: payload.Creator, Model: payload.Model, Authority: envelope.Authority, Tags: append([]string{}, payload.Tags...), ProjectID: "portable"}
	if _, err := workitem.Validate(value); err != nil {
		return workitem.Item{}, fmt.Errorf("work item %s is invalid: %w", envelope.RecordID, err)
	}
	if !sameReferences(envelope.Refs, workItemRefs(payload)) || !checkpointIndexMatches(envelope.Index, workItemIndex(payload)) {
		return workitem.Item{}, fmt.Errorf("work item %s metadata does not match payload", envelope.RecordID)
	}
	recorded, err := parsePortableTime(envelope.RecordedAt)
	value.ProjectID, value.Status, value.RecordedAt, value.SchemaVersion = "", "pending", recorded, payload.OperationalSchemaVersion
	return value, err
}

func decodeWorkItemDisposition(envelope Envelope, payload workItemDispositionPayload) (workitem.Disposition, error) {
	if envelope.RecordRevision != 0 || envelope.Authority != "caller_asserted" || !portableIncomingIdentifier(envelope.RecordID) || !portableIncomingIdentifier(payload.ItemID) || !oneOfValue(payload.Status, workitem.Statuses...) || !validReviewText(payload.Reason, true) || !validReviewText(payload.Creator, true) || !validReviewText(payload.Model, false) || len(payload.TargetRefs) > 32 || payload.OperationalSchemaVersion != workitem.SchemaVersion || workitem.NeedsTargets(payload.Status) && len(payload.TargetRefs) == 0 {
		return workitem.Disposition{}, fmt.Errorf("work item disposition %s is invalid", envelope.RecordID)
	}
	for _, target := range payload.TargetRefs {
		if !validReviewText(target, true) {
			return workitem.Disposition{}, fmt.Errorf("work item disposition %s has an invalid target", envelope.RecordID)
		}
	}
	if !sameReferences(envelope.Refs, workItemDispositionRefs(payload)) || !checkpointIndexMatches(envelope.Index, envelopeIndex{Title: payload.ItemID, Status: payload.Status}) {
		return workitem.Disposition{}, fmt.Errorf("work item disposition %s metadata does not match payload", envelope.RecordID)
	}
	recorded, err := parsePortableTime(envelope.RecordedAt)
	return workitem.Disposition{ID: envelope.RecordID, ItemID: payload.ItemID, Status: payload.Status, Reason: payload.Reason, Creator: payload.Creator, Model: payload.Model, Authority: envelope.Authority, TargetRefs: append([]string{}, payload.TargetRefs...), RecordedAt: recorded, SchemaVersion: payload.OperationalSchemaVersion}, err
}

func decodeWorkItemLink(envelope Envelope, payload workItemLinkPayload) (workitem.Link, error) {
	if envelope.RecordRevision != 0 || envelope.Authority != "caller_asserted" || !portableIncomingIdentifier(envelope.RecordID) || !portableIncomingIdentifier(payload.ItemID) || !oneOfValue(payload.Relationship, workitem.Relationships...) || !validReviewText(payload.TargetRef, true) || !validReviewText(payload.Creator, true) || payload.OperationalSchemaVersion != workitem.SchemaVersion {
		return workitem.Link{}, fmt.Errorf("work item link %s is invalid", envelope.RecordID)
	}
	if !sameReferences(envelope.Refs, workItemLinkRefs(payload)) || !checkpointIndexMatches(envelope.Index, envelopeIndex{Title: payload.ItemID, Status: payload.Relationship}) {
		return workitem.Link{}, fmt.Errorf("work item link %s metadata does not match payload", envelope.RecordID)
	}
	recorded, err := parsePortableTime(envelope.RecordedAt)
	return workitem.Link{ID: envelope.RecordID, ItemID: payload.ItemID, Relationship: payload.Relationship, TargetRef: payload.TargetRef, Creator: payload.Creator, Authority: envelope.Authority, RecordedAt: recorded, SchemaVersion: payload.OperationalSchemaVersion}, err
}

func workItemRefs(payload workItemPayload) []Reference {
	refs := make([]Reference, 0, 3)
	if payload.TaskID != "" {
		refs = append(refs, Reference{Kind: "task", ID: payload.TaskID})
	}
	if payload.CheckpointID != "" {
		refs = append(refs, Reference{Kind: "checkpoint", ID: payload.CheckpointID})
	}
	if payload.RoadmapNodeID != "" {
		refs = append(refs, Reference{Kind: "roadmap_node", ID: payload.RoadmapNodeID})
	}
	return refs
}

func workItemDispositionRefs(payload workItemDispositionPayload) []Reference {
	refs := []Reference{{Kind: "work_item", ID: payload.ItemID}}
	for _, target := range payload.TargetRefs {
		refs = appendUniqueReference(refs, Reference{Kind: "target_ref", ID: target})
	}
	return refs
}

func workItemLinkRefs(payload workItemLinkPayload) []Reference {
	return []Reference{{Kind: "work_item", ID: payload.ItemID}, {Kind: "target_ref", ID: payload.TargetRef}}
}

func portableReviewText(value, projectID, projectRoot string) string {
	return strings.ReplaceAll(portableText(value, projectRoot), projectID, "[PROJECT_ID]")
}

func portableReviewStrings(values []string, projectID, projectRoot string) []string {
	result := portableReviewStringsUnsorted(values, projectID, projectRoot)
	sort.Strings(result)
	return result
}

func portableReviewStringsUnsorted(values []string, projectID, projectRoot string) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = portableReviewText(value, projectID, projectRoot)
	}
	return result
}

func validReviewText(value string, required bool) bool {
	return strings.TrimSpace(value) == value && (!required || value != "") && len(value) <= reviewTextMaxBytes && strings.IndexByte(value, 0) < 0 && redact.String(value) == value
}
