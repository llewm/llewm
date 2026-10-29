package portable

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"

	"github.com/llewm/llewm/components/continuity/internal/pin"
)

// Pins travel as one envelope per assertion. A pin's target may arrive after the
// pin through sync, so import does not require it; the bootstrap then shows the
// reference in place of a title.

type pinPayload struct {
	TargetKind               string `json:"target_kind"`
	TargetID                 string `json:"target_id"`
	ScopeKind                string `json:"scope_kind"`
	ScopeID                  string `json:"scope_id"`
	Action                   string `json:"action"`
	Creator                  string `json:"creator"`
	OperationalSchemaVersion int    `json:"operational_schema_version"`
}

type portablePins map[string]pin.Pin

func loadPinSpecs(ctx context.Context, source queryer, projectID, projectRoot, lineageID string) ([]EnvelopeSpec, error) {
	values, err := pin.All(ctx, selectedExportSource(source, "pins"), projectID)
	if err != nil {
		return nil, err
	}
	specs := make([]EnvelopeSpec, 0, len(values))
	for _, value := range values {
		payload := pinPayload{TargetKind: value.TargetKind, TargetID: value.TargetID, ScopeKind: value.ScopeKind, ScopeID: value.ScopeID, Action: value.Action, Creator: portableReviewText(value.Creator, projectID, projectRoot), OperationalSchemaVersion: value.SchemaVersion}
		body, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		index, err := json.Marshal(envelopeIndex{Title: value.TargetKind + ":" + value.TargetID, Status: value.Action})
		if err != nil {
			return nil, err
		}
		spec := EnvelopeSpec{LineageID: lineageID, RecordType: "pin", RecordSchemaVersion: 1, RecordID: value.ID, RecordedAt: value.RecordedAt, Authority: value.Authority, Index: index, Refs: []Reference{}, Payload: body}
		encoded, err := EncodeEnvelope(spec)
		if err != nil {
			return nil, err
		}
		// Refuse to export what this build could not import.
		if _, err := decodePinEnvelope(portablePins{}, encoded.Envelope); err != nil {
			return nil, err
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

// decodePinEnvelope decodes a pin envelope, reporting false for other types.
func decodePinEnvelope(records portablePins, envelope Envelope) (bool, error) {
	if envelope.RecordType != "pin" {
		return false, nil
	}
	var payload pinPayload
	if err := strictDecode(envelope.Payload, &payload); err != nil {
		return true, fmt.Errorf("decode pin %s: %w", envelope.RecordID, err)
	}
	recorded, timeErr := parsePortableTime(envelope.RecordedAt)
	value := pin.Pin{ID: envelope.RecordID, TargetKind: payload.TargetKind, TargetID: payload.TargetID, ScopeKind: payload.ScopeKind, ScopeID: payload.ScopeID, Action: payload.Action, Creator: payload.Creator, Authority: envelope.Authority, RecordedAt: recorded, SchemaVersion: payload.OperationalSchemaVersion}
	normalized, err := pin.Validate(value)
	if timeErr != nil || envelope.RecordRevision != 0 || !portableIncomingIdentifier(envelope.RecordID) || payload.OperationalSchemaVersion != pin.SchemaVersion || err != nil {
		return true, fmt.Errorf("pin %s is invalid: %v", envelope.RecordID, firstError(err, timeErr))
	}
	if !reflect.DeepEqual(normalized, value) {
		return true, fmt.Errorf("pin %s is not in canonical form", envelope.RecordID)
	}
	if !sameReferences(envelope.Refs, []Reference{}) || !checkpointIndexMatches(envelope.Index, envelopeIndex{Title: payload.TargetKind + ":" + payload.TargetID, Status: payload.Action}) {
		return true, fmt.Errorf("pin %s metadata does not match payload", envelope.RecordID)
	}
	records[identityKey("pin", envelope.RecordID, 0)] = value
	return true, nil
}

// appendPins inserts new pin assertions in recorded order.
func appendPins(ctx context.Context, tx *sql.Tx, projectID string, values portablePins, appendKeys map[string]struct{}) error {
	ordered := make([]pin.Pin, 0, len(values))
	for key, value := range values {
		if _, ok := appendKeys[key]; ok {
			ordered = append(ordered, value)
		}
	}
	sort.Slice(ordered, func(i, j int) bool {
		if !ordered[i].RecordedAt.Equal(ordered[j].RecordedAt) {
			return ordered[i].RecordedAt.Before(ordered[j].RecordedAt)
		}
		return ordered[i].ID < ordered[j].ID
	})
	for _, value := range ordered {
		value.ProjectID = projectID
		if err := pin.Insert(ctx, tx, value); err != nil {
			return fmt.Errorf("append pin %s: %w", value.ID, err)
		}
	}
	return nil
}
