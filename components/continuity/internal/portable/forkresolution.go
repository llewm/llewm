package portable

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"

	"github.com/llewm/llewm/components/continuity/internal/fork"
)

// Fork resolutions travel as one envelope each. The heads they name normally
// arrive in the same sync; import does not require them, because a resolution
// only ever removes heads from consideration. Checkpoint forks travel as
// fork_resolution, revision forks as revision_fork_resolution, and supersession
// forks as supersession_fork_resolution, so a build that does not know a family
// defers its resolutions instead of refusing the whole sync.

type forkResolutionPayload struct {
	Family                   string   `json:"family"`
	SubjectID                string   `json:"subject_id"`
	AcceptedHead             string   `json:"accepted_head"`
	RejectedHeads            []string `json:"rejected_heads"`
	ContinuationID           string   `json:"continuation_id,omitempty"`
	Resolver                 string   `json:"resolver"`
	OperationalSchemaVersion int      `json:"operational_schema_version"`
}

type portableForkResolutions map[string]fork.Resolution

func forkResolutionRecordType(family string) string {
	switch family {
	case fork.FamilyRevision:
		return "revision_fork_resolution"
	case fork.FamilySupersession:
		return "supersession_fork_resolution"
	}
	return "fork_resolution"
}

func forkResolutionIndex(payload forkResolutionPayload) envelopeIndex {
	if payload.Family == fork.FamilyRevision || payload.Family == fork.FamilySupersession {
		return envelopeIndex{Title: payload.SubjectID}
	}
	return envelopeIndex{Title: payload.Family + ":" + payload.SubjectID, TaskID: payload.SubjectID}
}

func forkResolutionReferences(payload forkResolutionPayload) []Reference {
	if payload.Family == fork.FamilyRevision {
		// Heads are hashes, not record references. Schema 1 used envelope hashes;
		// schema 2 uses payload content hashes.
		return []Reference{}
	}
	if payload.Family == fork.FamilySupersession {
		// Heads are records of the subject's family.
		family, id, _ := fork.ParseSupersessionSubject(payload.SubjectID)
		refs := []Reference{{Kind: family, ID: id}, {Kind: family, ID: payload.AcceptedHead}}
		for _, head := range payload.RejectedHeads {
			refs = append(refs, Reference{Kind: family, ID: head})
		}
		return refs
	}
	refs := []Reference{{Kind: "task", ID: payload.SubjectID}, {Kind: "checkpoint", ID: payload.AcceptedHead}}
	for _, id := range payload.RejectedHeads {
		refs = append(refs, Reference{Kind: "checkpoint", ID: id})
	}
	if payload.ContinuationID != "" {
		refs = append(refs, Reference{Kind: "checkpoint", ID: payload.ContinuationID})
	}
	return refs
}

func loadForkResolutionSpecs(ctx context.Context, source queryer, projectID, projectRoot, lineageID string) ([]EnvelopeSpec, error) {
	values, err := fork.All(ctx, source, projectID)
	if err != nil {
		return nil, err
	}
	specs := make([]EnvelopeSpec, 0, len(values))
	for _, value := range values {
		payload := forkResolutionPayload{Family: value.Family, SubjectID: value.Subject, AcceptedHead: value.AcceptedHead, RejectedHeads: value.RejectedHeads, ContinuationID: value.Continuation, Resolver: portableReviewText(value.Resolver, projectID, projectRoot), OperationalSchemaVersion: value.SchemaVersion}
		body, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		index, err := json.Marshal(forkResolutionIndex(payload))
		if err != nil {
			return nil, err
		}
		spec := EnvelopeSpec{LineageID: lineageID, RecordType: forkResolutionRecordType(value.Family), RecordSchemaVersion: 1, RecordID: value.ID, RecordedAt: value.RecordedAt, Authority: value.Authority, Index: index, Refs: forkResolutionReferences(payload), Payload: body}
		encoded, err := EncodeEnvelope(spec)
		if err != nil {
			return nil, err
		}
		// Refuse to export what this build could not import.
		if _, err := decodeForkResolutionEnvelope(portableForkResolutions{}, encoded.Envelope); err != nil {
			return nil, err
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

// decodeForkResolutionEnvelope decodes a fork resolution envelope, reporting
// false for other types.
func decodeForkResolutionEnvelope(records portableForkResolutions, envelope Envelope) (bool, error) {
	if envelope.RecordType != "fork_resolution" && envelope.RecordType != "revision_fork_resolution" && envelope.RecordType != "supersession_fork_resolution" {
		return false, nil
	}
	var payload forkResolutionPayload
	if err := strictDecode(envelope.Payload, &payload); err != nil {
		return true, fmt.Errorf("decode fork resolution %s: %w", envelope.RecordID, err)
	}
	recorded, timeErr := parsePortableTime(envelope.RecordedAt)
	value := fork.Resolution{ID: envelope.RecordID, Family: payload.Family, Subject: payload.SubjectID, AcceptedHead: payload.AcceptedHead, RejectedHeads: payload.RejectedHeads, Continuation: payload.ContinuationID, Resolver: payload.Resolver, Authority: envelope.Authority, RecordedAt: recorded, SchemaVersion: payload.OperationalSchemaVersion}
	normalized, err := fork.Validate(value)
	if err == nil && forkResolutionRecordType(value.Family) != envelope.RecordType {
		err = fmt.Errorf("family %q does not travel as %s", value.Family, envelope.RecordType)
	}
	if timeErr != nil || envelope.RecordRevision != 0 || !portableIncomingIdentifier(envelope.RecordID) || err != nil {
		return true, fmt.Errorf("fork resolution %s is invalid: %v", envelope.RecordID, firstError(err, timeErr))
	}
	if !reflect.DeepEqual(normalized, value) {
		return true, fmt.Errorf("fork resolution %s is not in canonical form", envelope.RecordID)
	}
	if !sameReferences(envelope.Refs, forkResolutionReferences(payload)) || !checkpointIndexMatches(envelope.Index, forkResolutionIndex(payload)) {
		return true, fmt.Errorf("fork resolution %s metadata does not match payload", envelope.RecordID)
	}
	records[identityKey(envelope.RecordType, envelope.RecordID, 0)] = value
	return true, nil
}

// appendForkResolutions inserts new resolutions in recorded order.
func appendForkResolutions(ctx context.Context, tx *sql.Tx, projectID string, values portableForkResolutions, appendKeys map[string]struct{}) error {
	ordered := make([]fork.Resolution, 0, len(values))
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
		if err := fork.Insert(ctx, tx, value); err != nil {
			return fmt.Errorf("append fork resolution %s: %w", value.ID, err)
		}
	}
	return nil
}
