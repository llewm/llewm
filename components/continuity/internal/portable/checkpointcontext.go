package portable

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"

	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
)

// A checkpoint context travels as its own record type, not inside the checkpoint
// payload: checkpoint envelopes keep their bytes, and a build that predates
// contexts defers them instead of refusing the checkpoint.

type checkpointContextPayload struct {
	CheckpointID             string                `json:"checkpoint_id"`
	BasedOnCheckpointID      string                `json:"based_on_checkpoint_id,omitempty"`
	CodeHeads                []checkpoint.CodeHead `json:"code_heads"`
	OperationalSchemaVersion int                   `json:"operational_schema_version"`
}

type portableCheckpointContexts map[string]checkpoint.Context

func checkpointContextReferences(payload checkpointContextPayload) []Reference {
	refs := []Reference{{Kind: "checkpoint", ID: payload.CheckpointID}}
	if payload.BasedOnCheckpointID != "" {
		refs = append(refs, Reference{Kind: "checkpoint", ID: payload.BasedOnCheckpointID})
	}
	return refs
}

func loadCheckpointContextSpecs(ctx context.Context, source queryer, projectID, lineageID string) ([]EnvelopeSpec, error) {
	values, err := checkpoint.AllContexts(ctx, selectedExportSource(source, "checkpoint_contexts"), projectID)
	if err != nil {
		return nil, err
	}
	specs := make([]EnvelopeSpec, 0, len(values))
	for _, value := range values {
		payload := checkpointContextPayload{CheckpointID: value.CheckpointID, BasedOnCheckpointID: value.BasedOnCheckpoint, CodeHeads: value.CodeHeads, OperationalSchemaVersion: value.SchemaVersion}
		body, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		index, err := json.Marshal(envelopeIndex{Title: value.CheckpointID})
		if err != nil {
			return nil, err
		}
		spec := EnvelopeSpec{LineageID: lineageID, RecordType: "checkpoint_context", RecordSchemaVersion: 1, RecordID: value.CheckpointID, RecordedAt: value.ObservedAt, Authority: value.Authority, Index: index, Refs: checkpointContextReferences(payload), Payload: body}
		encoded, err := EncodeEnvelope(spec)
		if err != nil {
			return nil, err
		}
		// Refuse to export what this build could not import.
		if _, err := decodeCheckpointContextEnvelope(portableCheckpointContexts{}, encoded.Envelope); err != nil {
			return nil, err
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

// decodeCheckpointContextEnvelope decodes a checkpoint context envelope,
// reporting false for other types.
func decodeCheckpointContextEnvelope(records portableCheckpointContexts, envelope Envelope) (bool, error) {
	if envelope.RecordType != "checkpoint_context" {
		return false, nil
	}
	var payload checkpointContextPayload
	if err := strictDecode(envelope.Payload, &payload); err != nil {
		return true, fmt.Errorf("decode checkpoint context %s: %w", envelope.RecordID, err)
	}
	observed, timeErr := parsePortableTime(envelope.RecordedAt)
	heads := payload.CodeHeads
	if heads == nil {
		heads = []checkpoint.CodeHead{}
	}
	value := checkpoint.Context{CheckpointID: payload.CheckpointID, BasedOnCheckpoint: payload.BasedOnCheckpointID, CodeHeads: heads, Authority: envelope.Authority, ObservedAt: observed, SchemaVersion: payload.OperationalSchemaVersion}
	normalized, err := checkpoint.ValidateContext(value)
	if timeErr != nil || envelope.RecordRevision != 0 || envelope.RecordID != payload.CheckpointID || !portableIncomingIdentifier(envelope.RecordID) || (payload.BasedOnCheckpointID != "" && !portableIncomingIdentifier(payload.BasedOnCheckpointID)) || err != nil {
		return true, fmt.Errorf("checkpoint context %s is invalid: %v", envelope.RecordID, firstError(err, timeErr))
	}
	if !reflect.DeepEqual(normalized, value) {
		return true, fmt.Errorf("checkpoint context %s is not in canonical form", envelope.RecordID)
	}
	if !sameReferences(envelope.Refs, checkpointContextReferences(payload)) || !checkpointIndexMatches(envelope.Index, envelopeIndex{Title: payload.CheckpointID}) {
		return true, fmt.Errorf("checkpoint context %s metadata does not match payload", envelope.RecordID)
	}
	records[identityKey("checkpoint_context", envelope.RecordID, 0)] = value
	return true, nil
}

// appendCheckpointContexts inserts new contexts after their checkpoints.
func appendCheckpointContexts(ctx context.Context, tx *sql.Tx, projectID string, values portableCheckpointContexts, appendKeys map[string]struct{}) error {
	ordered := make([]checkpoint.Context, 0, len(values))
	for key, value := range values {
		if _, ok := appendKeys[key]; ok {
			ordered = append(ordered, value)
		}
	}
	sort.Slice(ordered, func(i, j int) bool {
		if !ordered[i].ObservedAt.Equal(ordered[j].ObservedAt) {
			return ordered[i].ObservedAt.Before(ordered[j].ObservedAt)
		}
		return ordered[i].CheckpointID < ordered[j].CheckpointID
	})
	for _, value := range ordered {
		value.ProjectID = projectID
		if err := checkpoint.InsertContext(ctx, tx, value); err != nil {
			return fmt.Errorf("append checkpoint context %s: %w", value.CheckpointID, err)
		}
	}
	return nil
}
