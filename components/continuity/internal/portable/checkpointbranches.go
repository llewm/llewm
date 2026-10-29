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

// Checkpoint branches travel as their own record type, beside the checkpoint's
// context rather than inside it: context envelopes keep their bytes, and a
// build that predates branches defers them instead of refusing the context.

type checkpointBranchesPayload struct {
	CheckpointID             string                  `json:"checkpoint_id"`
	Branches                 []checkpoint.CodeBranch `json:"branches"`
	OperationalSchemaVersion int                     `json:"operational_schema_version"`
}

type portableCheckpointBranches map[string]checkpoint.Branches

func checkpointBranchesReferences(payload checkpointBranchesPayload) []Reference {
	return []Reference{{Kind: "checkpoint", ID: payload.CheckpointID}}
}

func loadCheckpointBranchesSpecs(ctx context.Context, source queryer, projectID, lineageID string) ([]EnvelopeSpec, error) {
	values, err := checkpoint.AllBranches(ctx, selectedExportSource(source, "checkpoint_branches"), projectID)
	if err != nil {
		return nil, err
	}
	specs := make([]EnvelopeSpec, 0, len(values))
	for _, value := range values {
		payload := checkpointBranchesPayload{CheckpointID: value.CheckpointID, Branches: value.Branches, OperationalSchemaVersion: value.SchemaVersion}
		body, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		index, err := json.Marshal(envelopeIndex{Title: value.CheckpointID})
		if err != nil {
			return nil, err
		}
		spec := EnvelopeSpec{LineageID: lineageID, RecordType: "checkpoint_branches", RecordSchemaVersion: 1, RecordID: value.CheckpointID, RecordedAt: value.ObservedAt, Authority: value.Authority, Index: index, Refs: checkpointBranchesReferences(payload), Payload: body}
		encoded, err := EncodeEnvelope(spec)
		if err != nil {
			return nil, err
		}
		// Refuse to export what this build could not import.
		if _, err := decodeCheckpointBranchesEnvelope(portableCheckpointBranches{}, encoded.Envelope); err != nil {
			return nil, err
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

// decodeCheckpointBranchesEnvelope decodes a checkpoint branches envelope,
// reporting false for other types.
func decodeCheckpointBranchesEnvelope(records portableCheckpointBranches, envelope Envelope) (bool, error) {
	if envelope.RecordType != "checkpoint_branches" {
		return false, nil
	}
	var payload checkpointBranchesPayload
	if err := strictDecode(envelope.Payload, &payload); err != nil {
		return true, fmt.Errorf("decode checkpoint branches %s: %w", envelope.RecordID, err)
	}
	observed, timeErr := parsePortableTime(envelope.RecordedAt)
	value := checkpoint.Branches{CheckpointID: payload.CheckpointID, Branches: payload.Branches, Authority: envelope.Authority, ObservedAt: observed, SchemaVersion: payload.OperationalSchemaVersion}
	normalized, err := checkpoint.ValidateBranches(value)
	if timeErr != nil || envelope.RecordRevision != 0 || envelope.RecordID != payload.CheckpointID || !portableIncomingIdentifier(envelope.RecordID) || err != nil {
		return true, fmt.Errorf("checkpoint branches %s are invalid: %v", envelope.RecordID, firstError(err, timeErr))
	}
	if !reflect.DeepEqual(normalized, value) {
		return true, fmt.Errorf("checkpoint branches %s are not in canonical form", envelope.RecordID)
	}
	if !sameReferences(envelope.Refs, checkpointBranchesReferences(payload)) || !checkpointIndexMatches(envelope.Index, envelopeIndex{Title: payload.CheckpointID}) {
		return true, fmt.Errorf("checkpoint branches %s metadata does not match payload", envelope.RecordID)
	}
	records[identityKey("checkpoint_branches", envelope.RecordID, 0)] = value
	return true, nil
}

// appendCheckpointBranches inserts new checkpoint branches after their checkpoints.
func appendCheckpointBranches(ctx context.Context, tx *sql.Tx, projectID string, values portableCheckpointBranches, appendKeys map[string]struct{}) error {
	ordered := make([]checkpoint.Branches, 0, len(values))
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
		if err := checkpoint.InsertBranches(ctx, tx, value); err != nil {
			return fmt.Errorf("append checkpoint branches %s: %w", value.CheckpointID, err)
		}
	}
	return nil
}
