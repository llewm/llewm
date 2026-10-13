package portable

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"

	"github.com/llewm/llewm/components/continuity/internal/coverage"
)

// Coverage cells travel as one envelope per cell, naming the instance that
// observed it. A cell's requirement lives in the code repository and may not be
// in this checkout yet, so import does not require it; the matrix then lists
// the cell as orphaned until the requirements file catches up.

type coverageCellPayload struct {
	RequirementID            string   `json:"requirement_id"`
	Client                   string   `json:"client"`
	Platform                 string   `json:"platform"`
	ClientVersion            string   `json:"client_version,omitempty"`
	Status                   string   `json:"status"`
	Verification             string   `json:"verification"`
	Implementation           string   `json:"implementation,omitempty"`
	Refs                     []string `json:"refs"`
	Creator                  string   `json:"creator"`
	Model                    string   `json:"model,omitempty"`
	InstanceID               string   `json:"instance_id,omitempty"`
	OperationalSchemaVersion int      `json:"operational_schema_version"`
}

type portableCoverageCells map[string]coverage.Cell

func coverageCellIndex(payload coverageCellPayload) envelopeIndex {
	return envelopeIndex{Title: payload.RequirementID + " " + payload.Client + "/" + payload.Platform, Status: payload.Status}
}

func loadCoverageCellSpecs(ctx context.Context, source queryer, projectID, projectRoot, lineageID string) ([]EnvelopeSpec, error) {
	values, err := coverage.All(ctx, source, projectID)
	if err != nil {
		return nil, err
	}
	text := func(value string) string { return portableReviewText(value, projectID, projectRoot) }
	specs := make([]EnvelopeSpec, 0, len(values))
	for _, value := range values {
		payload := coverageCellPayload{RequirementID: text(value.RequirementID), Client: text(value.Client), Platform: text(value.Platform), ClientVersion: text(value.ClientVersion),
			Status: value.Status, Verification: value.Verification, Implementation: text(value.Implementation), Refs: portableReviewStrings(value.Refs, projectID, projectRoot),
			Creator: text(value.Creator), Model: value.Model, InstanceID: value.InstanceID, OperationalSchemaVersion: value.SchemaVersion}
		body, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		index, err := json.Marshal(coverageCellIndex(payload))
		if err != nil {
			return nil, err
		}
		spec := EnvelopeSpec{LineageID: lineageID, RecordType: "coverage_cell", RecordSchemaVersion: 1, RecordID: value.ID, RecordedAt: value.RecordedAt, Authority: value.Authority, Index: index, Refs: []Reference{}, Payload: body}
		encoded, err := EncodeEnvelope(spec)
		if err != nil {
			return nil, err
		}
		// Refuse to export what this build could not import.
		if _, err := decodeCoverageCellEnvelope(portableCoverageCells{}, encoded.Envelope); err != nil {
			return nil, err
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

// decodeCoverageCellEnvelope decodes a coverage cell envelope, reporting false
// for other types.
func decodeCoverageCellEnvelope(records portableCoverageCells, envelope Envelope) (bool, error) {
	if envelope.RecordType != "coverage_cell" {
		return false, nil
	}
	var payload coverageCellPayload
	if err := strictDecode(envelope.Payload, &payload); err != nil {
		return true, fmt.Errorf("decode coverage cell %s: %w", envelope.RecordID, err)
	}
	recorded, timeErr := parsePortableTime(envelope.RecordedAt)
	value := coverage.Cell{ID: envelope.RecordID, RequirementID: payload.RequirementID, Client: payload.Client, Platform: payload.Platform, ClientVersion: payload.ClientVersion,
		Status: payload.Status, Verification: payload.Verification, Implementation: payload.Implementation, Refs: payload.Refs, Creator: payload.Creator, Model: payload.Model,
		Authority: envelope.Authority, InstanceID: payload.InstanceID, RecordedAt: recorded, SchemaVersion: payload.OperationalSchemaVersion}
	if value.Refs == nil {
		value.Refs = []string{}
	}
	normalized, err := coverage.Validate(value)
	if timeErr != nil || envelope.RecordRevision != 0 || !portableIncomingIdentifier(envelope.RecordID) || payload.OperationalSchemaVersion != coverage.SchemaVersion || err != nil {
		return true, fmt.Errorf("coverage cell %s is invalid: %v", envelope.RecordID, firstError(err, timeErr))
	}
	if payload.InstanceID != "" && !portableIncomingIdentifier(payload.InstanceID) {
		return true, fmt.Errorf("coverage cell %s names an invalid instance", envelope.RecordID)
	}
	if !reflect.DeepEqual(normalized, value) {
		return true, fmt.Errorf("coverage cell %s is not in canonical form", envelope.RecordID)
	}
	if !sameReferences(envelope.Refs, []Reference{}) || !checkpointIndexMatches(envelope.Index, coverageCellIndex(payload)) {
		return true, fmt.Errorf("coverage cell %s metadata does not match payload", envelope.RecordID)
	}
	records[identityKey("coverage_cell", envelope.RecordID, 0)] = value
	return true, nil
}

// appendCoverageCells inserts new cells in recorded order.
func appendCoverageCells(ctx context.Context, tx *sql.Tx, projectID string, values portableCoverageCells, appendKeys map[string]struct{}) error {
	ordered := make([]coverage.Cell, 0, len(values))
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
		if err := coverage.Insert(ctx, tx, value); err != nil {
			return fmt.Errorf("append coverage cell %s: %w", value.ID, err)
		}
	}
	return nil
}
