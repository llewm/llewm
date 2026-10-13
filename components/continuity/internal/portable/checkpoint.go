package portable

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
	"github.com/llewm/llewm/components/continuity/internal/redact"
)

const checkpointOperationalSchemaVersion = 1

type checkpointPayload struct {
	SessionID                string               `json:"session_id,omitempty"`
	TaskID                   string               `json:"task_id,omitempty"`
	Task                     string               `json:"task"`
	Status                   string               `json:"status"`
	Summary                  string               `json:"summary"`
	NextAction               string               `json:"next_action"`
	Completed                []string             `json:"completed"`
	Attempts                 []checkpoint.Attempt `json:"attempts"`
	Changes                  []string             `json:"changes"`
	Verified                 []string             `json:"verified"`
	EvidenceRefs             []string             `json:"evidence_refs"`
	Metadata                 map[string]any       `json:"metadata,omitempty"`
	OperationalSchemaVersion int                  `json:"operational_schema_version"`
}

type checkpointActionPayload struct {
	SourceCheckpointID string `json:"source_checkpoint_id"`
	Description        string `json:"description"`
}

type checkpointTransitionPayload struct {
	ActionID        string `json:"action_id"`
	CheckpointID    string `json:"checkpoint_id,omitempty"`
	Status          string `json:"status"`
	StatusAuthority string `json:"status_authority"`
	Reason          string `json:"reason,omitempty"`
	ReasonAuthority string `json:"reason_authority,omitempty"`
}

type portableCheckpointAction struct {
	ID                 string
	SourceCheckpointID string
	Description        string
	CreatedAt          time.Time
}

type portableCheckpointTransition struct {
	ID           string
	ActionID     string
	CheckpointID string
	Status       string
	Reason       string
	CreatedAt    time.Time
}

func loadCheckpointSpecs(ctx context.Context, source queryer, projectID, projectRoot, lineageID string) ([]EnvelopeSpec, error) {
	result := make([]EnvelopeSpec, 0)
	rows, err := source.QueryContext(ctx, `SELECT id,COALESCE(session_id,''),COALESCE(task_id,''),task_title,status,summary,next_action,payload_json,created_at FROM checkpoints WHERE project_id=? ORDER BY created_at,id`, projectID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, sessionID, taskID, task, status, summary, nextAction, raw, created string
		if err := rows.Scan(&id, &sessionID, &taskID, &task, &status, &summary, &nextAction, &raw, &created); err != nil {
			rows.Close()
			return nil, err
		}
		var value checkpoint.Checkpoint
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			rows.Close()
			return nil, fmt.Errorf("decode checkpoint %s: %w", id, err)
		}
		recordedAt, err := time.Parse(time.RFC3339Nano, created)
		if err != nil || value.ID != id || value.ProjectID != projectID || value.SessionID != sessionID || value.TaskID != taskID || value.Task != task || value.Status != status || value.Summary != summary || value.NextAction != nextAction || !value.CreatedAt.Equal(recordedAt) {
			rows.Close()
			return nil, fmt.Errorf("checkpoint %s column or payload mismatch", id)
		}
		spec, err := checkpointSpec(lineageID, projectID, projectRoot, value)
		if err != nil {
			rows.Close()
			return nil, err
		}
		result = append(result, spec)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	actionRows, err := source.QueryContext(ctx, `SELECT id,source_checkpoint_id,description,created_at FROM checkpoint_actions WHERE project_id=? ORDER BY created_at,id`, projectID)
	if err != nil {
		return nil, err
	}
	for actionRows.Next() {
		var value portableCheckpointAction
		var created string
		if err := actionRows.Scan(&value.ID, &value.SourceCheckpointID, &value.Description, &created); err != nil {
			actionRows.Close()
			return nil, err
		}
		value.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			actionRows.Close()
			return nil, err
		}
		if !portableCheckpointIdentifier(value.ID, projectID, projectRoot) || !portableCheckpointIdentifier(value.SourceCheckpointID, projectID, projectRoot) {
			actionRows.Close()
			return nil, fmt.Errorf("checkpoint action %s has a non-portable identifier", value.ID)
		}
		payload, err := json.Marshal(checkpointActionPayload{SourceCheckpointID: value.SourceCheckpointID, Description: portableCheckpointText(value.Description, projectID, projectRoot)})
		if err != nil {
			actionRows.Close()
			return nil, err
		}
		index, err := json.Marshal(envelopeIndex{Title: portableCheckpointText(value.Description, projectID, projectRoot)})
		if err != nil {
			actionRows.Close()
			return nil, err
		}
		result = append(result, EnvelopeSpec{LineageID: lineageID, RecordType: "checkpoint_action", RecordSchemaVersion: 1, RecordID: value.ID, RecordedAt: value.CreatedAt, Authority: "caller_asserted", Index: index, Refs: []Reference{{Kind: "checkpoint", ID: value.SourceCheckpointID}}, Payload: payload})
	}
	if err := actionRows.Err(); err != nil {
		actionRows.Close()
		return nil, err
	}
	if err := actionRows.Close(); err != nil {
		return nil, err
	}

	transitionRows, err := source.QueryContext(ctx, `SELECT t.id,t.action_id,COALESCE(t.checkpoint_id,''),t.status,COALESCE(t.reason,''),t.created_at FROM checkpoint_action_transitions t JOIN checkpoint_actions a ON a.id=t.action_id WHERE a.project_id=? ORDER BY t.created_at,t.id`, projectID)
	if err != nil {
		return nil, err
	}
	defer transitionRows.Close()
	for transitionRows.Next() {
		var value portableCheckpointTransition
		var created string
		if err := transitionRows.Scan(&value.ID, &value.ActionID, &value.CheckpointID, &value.Status, &value.Reason, &created); err != nil {
			return nil, err
		}
		value.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, err
		}
		if !portableCheckpointIdentifier(value.ID, projectID, projectRoot) || !portableCheckpointIdentifier(value.ActionID, projectID, projectRoot) || value.CheckpointID != "" && !portableCheckpointIdentifier(value.CheckpointID, projectID, projectRoot) {
			return nil, fmt.Errorf("checkpoint action transition %s has a non-portable identifier", value.ID)
		}
		value.Reason = portableCheckpointText(value.Reason, projectID, projectRoot)
		reasonAuthority := ""
		if value.Reason != "" {
			reasonAuthority = "caller_asserted"
		}
		payload, err := json.Marshal(checkpointTransitionPayload{ActionID: value.ActionID, CheckpointID: value.CheckpointID, Status: value.Status, StatusAuthority: "machine_observed", Reason: value.Reason, ReasonAuthority: reasonAuthority})
		if err != nil {
			return nil, err
		}
		refs := []Reference{{Kind: "checkpoint_action", ID: value.ActionID}}
		if value.CheckpointID != "" {
			refs = append(refs, Reference{Kind: "checkpoint", ID: value.CheckpointID})
		}
		index, err := json.Marshal(envelopeIndex{Status: value.Status})
		if err != nil {
			return nil, err
		}
		result = append(result, EnvelopeSpec{LineageID: lineageID, RecordType: "checkpoint_action_transition", RecordSchemaVersion: 1, RecordID: value.ID, RecordedAt: value.CreatedAt, Authority: "machine_observed", Index: index, Refs: refs, Payload: payload})
	}
	return result, transitionRows.Err()
}

func checkpointSpec(lineageID, projectID, projectRoot string, value checkpoint.Checkpoint) (EnvelopeSpec, error) {
	if !portableCheckpointIdentifier(value.ID, projectID, projectRoot) {
		return EnvelopeSpec{}, fmt.Errorf("checkpoint %s has a non-portable identifier", value.ID)
	}
	authority := value.Authority
	if authority == "" {
		authority = "caller_asserted"
	}
	if authority != "caller_asserted" {
		return EnvelopeSpec{}, fmt.Errorf("checkpoint %s has unsupported authority %q", value.ID, authority)
	}
	attempts := make([]checkpoint.Attempt, len(value.Attempts))
	for index, attempt := range value.Attempts {
		attempts[index] = checkpoint.Attempt{Description: portableCheckpointText(attempt.Description, projectID, projectRoot), Outcome: portableCheckpointText(attempt.Outcome, projectID, projectRoot), Reason: portableCheckpointText(attempt.Reason, projectID, projectRoot)}
	}
	payload := checkpointPayload{
		SessionID: portableCheckpointText(value.SessionID, projectID, projectRoot), TaskID: portableCheckpointText(value.TaskID, projectID, projectRoot),
		Task: portableCheckpointText(value.Task, projectID, projectRoot), Status: value.Status,
		Summary: portableCheckpointText(value.Summary, projectID, projectRoot), NextAction: portableCheckpointText(value.NextAction, projectID, projectRoot),
		Completed: portableCheckpointStrings(value.Completed, projectID, projectRoot), Attempts: attempts,
		Changes: portableCheckpointStrings(value.Changes, projectID, projectRoot), Verified: portableCheckpointStrings(value.Verified, projectID, projectRoot),
		EvidenceRefs:             portableCheckpointStrings(value.EvidenceRefs, projectID, projectRoot),
		OperationalSchemaVersion: checkpointOperationalSchemaVersion,
	}
	if value.Metadata != nil {
		clean, ok := portableCheckpointValue(value.Metadata, projectID, projectRoot).(map[string]any)
		if !ok {
			return EnvelopeSpec{}, fmt.Errorf("checkpoint %s metadata is not an object", value.ID)
		}
		payload.Metadata = clean
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return EnvelopeSpec{}, err
	}
	index, err := json.Marshal(envelopeIndex{Title: payload.Task, Status: payload.Status, TaskID: payload.TaskID})
	if err != nil {
		return EnvelopeSpec{}, err
	}
	refs := make([]Reference, 0, 2+len(payload.EvidenceRefs))
	if payload.SessionID != "" {
		refs = append(refs, Reference{Kind: "session", ID: payload.SessionID})
	}
	if payload.TaskID != "" {
		refs = append(refs, Reference{Kind: "task", ID: payload.TaskID})
	}
	seen := make(map[Reference]struct{})
	for _, reference := range refs {
		seen[reference] = struct{}{}
	}
	for _, evidenceRef := range payload.EvidenceRefs {
		reference := Reference{Kind: "evidence_ref", ID: evidenceRef}
		if strings.HasPrefix(evidenceRef, "evidence:") {
			reference = Reference{Kind: "evidence", ID: strings.TrimPrefix(evidenceRef, "evidence:")}
		}
		if _, exists := seen[reference]; !exists {
			seen[reference] = struct{}{}
			refs = append(refs, reference)
		}
	}
	return EnvelopeSpec{LineageID: lineageID, RecordType: "checkpoint", RecordSchemaVersion: 1, RecordID: value.ID, RecordedAt: value.CreatedAt, Authority: authority, Index: index, Refs: refs, Payload: body}, nil
}

func decodeCheckpoint(envelope Envelope, payload checkpointPayload) (checkpoint.Checkpoint, error) {
	if envelope.Authority != "caller_asserted" || !portableIncomingIdentifier(envelope.RecordID) || payload.OperationalSchemaVersion != checkpointOperationalSchemaVersion || strings.TrimSpace(payload.Task) != payload.Task || payload.Task == "" || strings.TrimSpace(payload.NextAction) != payload.NextAction || payload.NextAction == "" || strings.TrimSpace(payload.SessionID) != payload.SessionID || strings.TrimSpace(payload.TaskID) != payload.TaskID || !oneOfValue(payload.Status, "in_progress", "completed", "blocked", "paused") {
		return checkpoint.Checkpoint{}, fmt.Errorf("checkpoint %s has invalid required fields, authority, or schema", envelope.RecordID)
	}
	if len(payload.Completed) > 64 || len(payload.Attempts) > 64 || len(payload.Changes) > 64 || len(payload.Verified) > 64 || len(payload.EvidenceRefs) > 64 {
		return checkpoint.Checkpoint{}, fmt.Errorf("checkpoint %s exceeds list bounds", envelope.RecordID)
	}
	for _, values := range [][]string{payload.Completed, payload.Changes, payload.Verified, payload.EvidenceRefs} {
		for _, value := range values {
			if strings.TrimSpace(value) == "" {
				return checkpoint.Checkpoint{}, fmt.Errorf("checkpoint %s contains a blank list item", envelope.RecordID)
			}
		}
	}
	for _, attempt := range payload.Attempts {
		if strings.TrimSpace(attempt.Description) == "" || strings.TrimSpace(attempt.Outcome) == "" {
			return checkpoint.Checkpoint{}, fmt.Errorf("checkpoint %s contains an invalid attempt", envelope.RecordID)
		}
	}
	if !checkpointPayloadIsRedacted(payload) {
		return checkpoint.Checkpoint{}, fmt.Errorf("checkpoint %s payload is not redacted", envelope.RecordID)
	}
	recordedAt, err := parsePortableTime(envelope.RecordedAt)
	if err != nil {
		return checkpoint.Checkpoint{}, err
	}
	if !sameReferences(envelope.Refs, checkpointReferences(payload)) {
		return checkpoint.Checkpoint{}, fmt.Errorf("checkpoint %s references do not match payload", envelope.RecordID)
	}
	if !checkpointIndexMatches(envelope.Index, envelopeIndex{Title: payload.Task, Status: payload.Status, TaskID: payload.TaskID}) {
		return checkpoint.Checkpoint{}, fmt.Errorf("checkpoint %s index does not match payload", envelope.RecordID)
	}
	value := checkpoint.Checkpoint{ID: envelope.RecordID, SessionID: payload.SessionID, TaskID: payload.TaskID, Task: payload.Task, Status: payload.Status, Summary: payload.Summary, NextAction: payload.NextAction, Completed: payload.Completed, Attempts: payload.Attempts, Changes: payload.Changes, Verified: payload.Verified, EvidenceRefs: payload.EvidenceRefs, Authority: envelope.Authority, CreatedAt: recordedAt, Metadata: payload.Metadata}
	if _, err := encodeImportedCheckpoint(value); err != nil {
		return checkpoint.Checkpoint{}, fmt.Errorf("checkpoint %s cannot fit local storage: %w", envelope.RecordID, err)
	}
	return value, nil
}

func decodeCheckpointAction(envelope Envelope, payload checkpointActionPayload) (portableCheckpointAction, error) {
	if envelope.Authority != "caller_asserted" || !portableIncomingIdentifier(envelope.RecordID) || !portableIncomingIdentifier(payload.SourceCheckpointID) || strings.TrimSpace(payload.Description) != payload.Description || payload.Description == "" || redact.String(payload.Description) != payload.Description || !sameReferences(envelope.Refs, []Reference{{Kind: "checkpoint", ID: payload.SourceCheckpointID}}) {
		return portableCheckpointAction{}, fmt.Errorf("checkpoint action %s is invalid", envelope.RecordID)
	}
	if !checkpointIndexMatches(envelope.Index, envelopeIndex{Title: payload.Description}) {
		return portableCheckpointAction{}, fmt.Errorf("checkpoint action %s index does not match payload", envelope.RecordID)
	}
	recordedAt, err := parsePortableTime(envelope.RecordedAt)
	if err != nil {
		return portableCheckpointAction{}, err
	}
	return portableCheckpointAction{ID: envelope.RecordID, SourceCheckpointID: payload.SourceCheckpointID, Description: payload.Description, CreatedAt: recordedAt}, nil
}

func decodeCheckpointTransition(envelope Envelope, payload checkpointTransitionPayload) (portableCheckpointTransition, error) {
	if envelope.Authority != "machine_observed" || !portableIncomingIdentifier(envelope.RecordID) || !portableIncomingIdentifier(payload.ActionID) || payload.CheckpointID != "" && !portableIncomingIdentifier(payload.CheckpointID) || payload.StatusAuthority != "machine_observed" || redact.String(payload.Reason) != payload.Reason || !oneOfValue(payload.Status, "open", "carried_forward", "completed", "superseded") {
		return portableCheckpointTransition{}, fmt.Errorf("checkpoint action transition %s is invalid", envelope.RecordID)
	}
	if (payload.Reason == "") != (payload.ReasonAuthority == "") || payload.Reason != "" && payload.ReasonAuthority != "caller_asserted" {
		return portableCheckpointTransition{}, fmt.Errorf("checkpoint action transition %s has invalid reason authority", envelope.RecordID)
	}
	if (payload.Status == "open" || payload.Status == "carried_forward") && payload.CheckpointID == "" {
		return portableCheckpointTransition{}, fmt.Errorf("checkpoint action transition %s has an invalid checkpoint scope", envelope.RecordID)
	}
	refs := []Reference{{Kind: "checkpoint_action", ID: payload.ActionID}}
	if payload.CheckpointID != "" {
		refs = append(refs, Reference{Kind: "checkpoint", ID: payload.CheckpointID})
	}
	if !sameReferences(envelope.Refs, refs) {
		return portableCheckpointTransition{}, fmt.Errorf("checkpoint action transition %s references do not match payload", envelope.RecordID)
	}
	if !checkpointIndexMatches(envelope.Index, envelopeIndex{Status: payload.Status}) {
		return portableCheckpointTransition{}, fmt.Errorf("checkpoint action transition %s index does not match payload", envelope.RecordID)
	}
	recordedAt, err := parsePortableTime(envelope.RecordedAt)
	if err != nil {
		return portableCheckpointTransition{}, err
	}
	return portableCheckpointTransition{ID: envelope.RecordID, ActionID: payload.ActionID, CheckpointID: payload.CheckpointID, Status: payload.Status, Reason: payload.Reason, CreatedAt: recordedAt}, nil
}

func checkpointReferences(payload checkpointPayload) []Reference {
	refs := make([]Reference, 0, 2+len(payload.EvidenceRefs))
	if payload.SessionID != "" {
		refs = append(refs, Reference{Kind: "session", ID: payload.SessionID})
	}
	if payload.TaskID != "" {
		refs = append(refs, Reference{Kind: "task", ID: payload.TaskID})
	}
	seen := make(map[Reference]struct{})
	for _, reference := range refs {
		seen[reference] = struct{}{}
	}
	for _, evidenceRef := range payload.EvidenceRefs {
		reference := Reference{Kind: "evidence_ref", ID: evidenceRef}
		if strings.HasPrefix(evidenceRef, "evidence:") {
			reference = Reference{Kind: "evidence", ID: strings.TrimPrefix(evidenceRef, "evidence:")}
		}
		if _, exists := seen[reference]; !exists {
			seen[reference] = struct{}{}
			refs = append(refs, reference)
		}
	}
	return refs
}

func checkpointPayloadIsRedacted(payload checkpointPayload) bool {
	body, err := json.Marshal(payload)
	if err != nil {
		return false
	}
	var value any
	if json.Unmarshal(body, &value) != nil {
		return false
	}
	redacted := redact.Value(value)
	return reflect.DeepEqual(value, redacted)
}

func checkpointIndexMatches(raw json.RawMessage, expected envelopeIndex) bool {
	var actual envelopeIndex
	return len(raw) > 0 && strictDecode(raw, &actual) == nil && reflect.DeepEqual(actual, expected)
}

func encodeImportedCheckpoint(value checkpoint.Checkpoint) (string, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	if len(body) > checkpoint.MaxBytes {
		return "", fmt.Errorf("checkpoint exceeds %d bytes", checkpoint.MaxBytes)
	}
	return string(body), nil
}

func portableCheckpointStrings(values []string, projectID, projectRoot string) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = portableCheckpointText(value, projectID, projectRoot)
	}
	return result
}

func portableCheckpointText(value, projectID, projectRoot string) string {
	value = portableText(value, projectRoot)
	return strings.ReplaceAll(value, projectID, "[PROJECT_ID]")
}

func portableCheckpointIdentifier(value, projectID, projectRoot string) bool {
	return strings.TrimSpace(value) != "" && portableCheckpointText(value, projectID, projectRoot) == value
}

func portableIncomingIdentifier(value string) bool {
	return strings.TrimSpace(value) != "" && strings.TrimSpace(value) == value && redact.String(value) == value
}

func portableCheckpointValue(value any, projectID, projectRoot string) any {
	value = redact.Value(value)
	switch typed := value.(type) {
	case string:
		return portableCheckpointText(typed, projectID, projectRoot)
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = portableCheckpointValue(item, projectID, projectRoot)
		}
		return result
	case map[string]any:
		result := make(map[string]any, len(typed))
		keys := sortedCheckpointKeys(typed)
		for _, key := range keys {
			item := typed[key]
			result[portableCheckpointText(key, projectID, projectRoot)] = portableCheckpointValue(item, projectID, projectRoot)
		}
		return result
	default:
		return value
	}
}

func sortedCheckpointKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
