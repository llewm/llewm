package portable

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/usage"
)

// Usage session summaries travel so usage reports cover every machine; raw
// per-request usage stays where the session ran. This machine's summaries are
// derived from its usage tables at export, for finished sessions only, and
// summaries imported from others are exported again unchanged. A summary is
// named by its content hash, so a resumed session adds a newer summary
// instead of colliding with the old one.

type usageSummaryPayload struct {
	Client                   string             `json:"client"`
	SessionID                string             `json:"session_id"`
	InstanceID               string             `json:"instance_id"`
	FirstAt                  string             `json:"first_at"`
	LastAt                   string             `json:"last_at"`
	TaskID                   string             `json:"task_id,omitempty"`
	Overhead                 int64              `json:"overhead_tokens"`
	Models                   []usage.ModelUsage `json:"models"`
	Tools                    []usage.ToolUsage  `json:"tools"`
	OperationalSchemaVersion int                `json:"operational_schema_version"`
}

type portableUsageSummaries map[string]usage.Summary

func usageSummaryIndex(payload usageSummaryPayload) envelopeIndex {
	return envelopeIndex{Title: payload.Client + " session " + payload.SessionID, TaskID: payload.TaskID}
}

func loadUsageSummarySpecs(ctx context.Context, source queryer, projectID, lineageID string) ([]EnvelopeSpec, error) {
	local, err := usage.LocalSummaries(ctx, source, projectID, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	stored, err := usage.StoredSummaries(ctx, source, projectID)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var specs []EnvelopeSpec
	for _, value := range append(local, stored...) {
		if seen[value.ID] {
			continue
		}
		seen[value.ID] = true
		payload := usageSummaryPayload{Client: value.Client, SessionID: value.SessionID, InstanceID: value.InstanceID, FirstAt: value.FirstAt.UTC().Format(time.RFC3339Nano), LastAt: value.LastAt.UTC().Format(time.RFC3339Nano), TaskID: value.TaskID, Overhead: value.Overhead, Models: value.Models, Tools: value.Tools, OperationalSchemaVersion: value.SchemaVersion}
		body, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		index, err := json.Marshal(usageSummaryIndex(payload))
		if err != nil {
			return nil, err
		}
		spec := EnvelopeSpec{LineageID: lineageID, RecordType: "usage_session_summary", RecordSchemaVersion: 1, RecordID: value.ID, RecordedAt: value.LastAt, Authority: "machine_observed", Index: index, Refs: []Reference{}, Payload: body}
		encoded, err := EncodeEnvelope(spec)
		if err != nil {
			return nil, err
		}
		// Refuse to export what this build could not import.
		if _, err := decodeUsageSummaryEnvelope(portableUsageSummaries{}, encoded.Envelope); err != nil {
			return nil, err
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

// decodeUsageSummaryEnvelope decodes a usage summary envelope, reporting false
// for other types.
func decodeUsageSummaryEnvelope(records portableUsageSummaries, envelope Envelope) (bool, error) {
	if envelope.RecordType != "usage_session_summary" {
		return false, nil
	}
	var payload usageSummaryPayload
	if err := strictDecode(envelope.Payload, &payload); err != nil {
		return true, fmt.Errorf("decode usage summary %s: %w", envelope.RecordID, err)
	}
	first, firstErr := parsePortableTime(payload.FirstAt)
	last, lastErr := parsePortableTime(payload.LastAt)
	recorded, recordedErr := parsePortableTime(envelope.RecordedAt)
	value := usage.Summary{ID: envelope.RecordID, SchemaVersion: payload.OperationalSchemaVersion, Client: payload.Client, SessionID: payload.SessionID, InstanceID: payload.InstanceID, FirstAt: first, LastAt: last, TaskID: payload.TaskID, Overhead: payload.Overhead, Models: payload.Models, Tools: payload.Tools}
	if value.Models == nil {
		value.Models = []usage.ModelUsage{}
	}
	if value.Tools == nil {
		value.Tools = []usage.ToolUsage{}
	}
	err := firstError(firstError(firstErr, lastErr), recordedErr)
	if err == nil {
		err = usage.ValidateSummary(value)
	}
	if err != nil || envelope.RecordRevision != 0 || envelope.Authority != "machine_observed" || (payload.OperationalSchemaVersion != 1 && payload.OperationalSchemaVersion != 2) || !recorded.Equal(last) {
		return true, fmt.Errorf("usage summary %s is invalid: %v", envelope.RecordID, err)
	}
	if !sameReferences(envelope.Refs, []Reference{}) || !checkpointIndexMatches(envelope.Index, usageSummaryIndex(payload)) {
		return true, fmt.Errorf("usage summary %s metadata does not match payload", envelope.RecordID)
	}
	records[identityKey(envelope.RecordType, envelope.RecordID, 0)] = value
	return true, nil
}

// appendUsageSummaries stores new summaries in recorded order.
func appendUsageSummaries(ctx context.Context, tx *sql.Tx, projectID string, values portableUsageSummaries, appendKeys map[string]struct{}) error {
	ordered := make([]usage.Summary, 0, len(values))
	for key, value := range values {
		if _, ok := appendKeys[key]; ok {
			ordered = append(ordered, value)
		}
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	for _, value := range ordered {
		if err := usage.InsertSummary(ctx, tx, projectID, value); err != nil {
			return fmt.Errorf("append usage summary %s: %w", value.ID, err)
		}
	}
	return nil
}
