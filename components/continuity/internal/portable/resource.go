package portable

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/resource"
)

// Resources travel as one envelope per revision and one per note. They hold no
// credentials; importing one grants no access.

type resourcePayload struct {
	Kind                     string            `json:"kind"`
	Title                    string            `json:"title"`
	Summary                  string            `json:"summary"`
	Attributes               map[string]string `json:"attributes"`
	Status                   string            `json:"status"`
	Creator                  string            `json:"creator"`
	Model                    string            `json:"model,omitempty"`
	OperationalSchemaVersion int               `json:"operational_schema_version"`
}

type resourceNotePayload struct {
	ResourceID               string `json:"resource_id"`
	Text                     string `json:"text"`
	Creator                  string `json:"creator"`
	ObservedAt               string `json:"observed_at"`
	OperationalSchemaVersion int    `json:"operational_schema_version"`
}

type portableResources struct {
	Resources map[string]resource.Resource
	Notes     map[string]resource.Note
}

func newPortableResources() portableResources {
	return portableResources{Resources: map[string]resource.Resource{}, Notes: map[string]resource.Note{}}
}

func loadResourceSpecs(ctx context.Context, source queryer, projectID, projectRoot, lineageID string) ([]EnvelopeSpec, error) {
	resources, notes, err := resource.All(ctx, source, projectID)
	if err != nil {
		return nil, err
	}
	text := func(value string) string { return portableReviewText(value, projectID, projectRoot) }
	var specs []EnvelopeSpec
	add := func(recordType, id string, revision int, value resource.Resource, note resource.Note, authority string, index envelopeIndex, refs []Reference, payload any) error {
		body, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		indexBody, err := json.Marshal(index)
		if err != nil {
			return err
		}
		recorded := value.RecordedAt
		if recordType == "resource_note" {
			recorded = note.RecordedAt
		}
		spec := EnvelopeSpec{LineageID: lineageID, RecordType: recordType, RecordSchemaVersion: 1, RecordID: id, RecordRevision: revision, RecordedAt: recorded, Authority: authority, Index: indexBody, Refs: refs, Payload: body}
		encoded, err := EncodeEnvelope(spec)
		if err != nil {
			return err
		}
		// Refuse to export what this build could not import.
		if _, err := decodeResourceEnvelope(newPortableResources(), encoded.Envelope); err != nil {
			return err
		}
		specs = append(specs, spec)
		return nil
	}
	for _, value := range resources {
		attributes := make(map[string]string, len(value.Attributes))
		for key, attribute := range value.Attributes {
			attributes[key] = text(attribute)
		}
		payload := resourcePayload{Kind: value.Kind, Title: text(value.Title), Summary: text(value.Summary), Attributes: attributes, Status: value.Status, Creator: text(value.Creator), Model: value.Model, OperationalSchemaVersion: value.SchemaVersion}
		if err := add("resource", value.ID, value.Revision, value, resource.Note{}, value.Authority, envelopeIndex{Title: payload.Title, Status: payload.Status}, []Reference{}, payload); err != nil {
			return nil, err
		}
	}
	for _, note := range notes {
		payload := resourceNotePayload{ResourceID: note.ResourceID, Text: text(note.Text), Creator: text(note.Creator), ObservedAt: note.ObservedAt.UTC().Format(time.RFC3339Nano), OperationalSchemaVersion: note.SchemaVersion}
		if err := add("resource_note", note.ID, 0, resource.Resource{}, note, note.Authority, envelopeIndex{Title: note.ResourceID}, []Reference{{Kind: "resource", ID: note.ResourceID}}, payload); err != nil {
			return nil, err
		}
	}
	return specs, nil
}

// decodeResourceEnvelope decodes resource and resource-note envelopes, reporting
// false for other record types. Payloads must already be in stored form.
func decodeResourceEnvelope(records portableResources, envelope Envelope) (bool, error) {
	recorded, timeErr := parsePortableTime(envelope.RecordedAt)
	switch envelope.RecordType {
	case "resource":
		var payload resourcePayload
		if err := strictDecode(envelope.Payload, &payload); err != nil {
			return true, fmt.Errorf("decode resource %s: %w", envelope.RecordID, err)
		}
		value := resource.Resource{ID: envelope.RecordID, Revision: envelope.RecordRevision, Kind: payload.Kind, Title: payload.Title, Summary: payload.Summary, Attributes: payload.Attributes, Status: payload.Status, Creator: payload.Creator, Model: payload.Model, Authority: envelope.Authority, RecordedAt: recorded, SchemaVersion: payload.OperationalSchemaVersion}
		if value.Attributes == nil {
			value.Attributes = map[string]string{}
		}
		normalized, err := resource.Validate(value)
		if timeErr != nil || envelope.RecordRevision < 1 || !portableIncomingIdentifier(envelope.RecordID) || payload.OperationalSchemaVersion != resource.SchemaVersion || err != nil {
			return true, fmt.Errorf("resource %s is invalid: %v", envelope.RecordID, firstError(err, timeErr))
		}
		if !reflect.DeepEqual(normalized, value) {
			return true, fmt.Errorf("resource %s is not in canonical form", envelope.RecordID)
		}
		if !sameReferences(envelope.Refs, []Reference{}) || !checkpointIndexMatches(envelope.Index, envelopeIndex{Title: payload.Title, Status: payload.Status}) {
			return true, fmt.Errorf("resource %s metadata does not match payload", envelope.RecordID)
		}
		records.Resources[identityKey("resource", envelope.RecordID, envelope.RecordRevision)] = value
	case "resource_note":
		var payload resourceNotePayload
		if err := strictDecode(envelope.Payload, &payload); err != nil {
			return true, fmt.Errorf("decode resource note %s: %w", envelope.RecordID, err)
		}
		observed, observedErr := parsePortableTime(payload.ObservedAt)
		note := resource.Note{ID: envelope.RecordID, ResourceID: payload.ResourceID, Text: payload.Text, Authority: envelope.Authority, Creator: payload.Creator, ObservedAt: observed, RecordedAt: recorded, SchemaVersion: payload.OperationalSchemaVersion}
		normalized, err := resource.ValidateNote(note)
		if timeErr != nil || observedErr != nil || envelope.RecordRevision != 0 || !portableIncomingIdentifier(envelope.RecordID) || payload.OperationalSchemaVersion != resource.SchemaVersion || err != nil {
			return true, fmt.Errorf("resource note %s is invalid: %v", envelope.RecordID, firstError(err, firstError(timeErr, observedErr)))
		}
		if !reflect.DeepEqual(normalized, note) {
			return true, fmt.Errorf("resource note %s is not in canonical form", envelope.RecordID)
		}
		if !sameReferences(envelope.Refs, []Reference{{Kind: "resource", ID: payload.ResourceID}}) || !checkpointIndexMatches(envelope.Index, envelopeIndex{Title: payload.ResourceID}) {
			return true, fmt.Errorf("resource note %s metadata does not match payload", envelope.RecordID)
		}
		records.Notes[identityKey("resource_note", envelope.RecordID, 0)] = note
	default:
		return false, nil
	}
	return true, nil
}

// validateResourceDependencies requires each new note's resource to be in the
// bundle or already in the project.
func validateResourceDependencies(ctx context.Context, tx *sql.Tx, projectID string, values portableResources, appendKeys map[string]struct{}) error {
	bundled := map[string]bool{}
	for _, value := range values.Resources {
		bundled[value.ID] = true
	}
	for key, note := range values.Notes {
		if _, ok := appendKeys[key]; !ok || bundled[note.ResourceID] {
			continue
		}
		if ok, err := rowExists(ctx, tx, `SELECT 1 FROM resources WHERE project_id=? AND id=?`, projectID, note.ResourceID); err != nil || !ok {
			return firstError(err, fmt.Errorf("resource note %s names a missing resource %s", note.ID, note.ResourceID))
		}
	}
	return nil
}

// appendResources inserts new revisions in revision order, then new notes.
func appendResources(ctx context.Context, tx *sql.Tx, projectID string, values portableResources, appendKeys map[string]struct{}) error {
	resources := make([]resource.Resource, 0, len(values.Resources))
	for key, value := range values.Resources {
		if _, ok := appendKeys[key]; ok {
			resources = append(resources, value)
		}
	}
	sort.Slice(resources, func(i, j int) bool {
		if resources[i].ID != resources[j].ID {
			return resources[i].ID < resources[j].ID
		}
		return resources[i].Revision < resources[j].Revision
	})
	for _, value := range resources {
		value.ProjectID = projectID
		if err := resource.Insert(ctx, tx, value); err != nil {
			return fmt.Errorf("append resource %s@%d: %w", value.ID, value.Revision, err)
		}
	}
	notes := make([]resource.Note, 0, len(values.Notes))
	for key, note := range values.Notes {
		if _, ok := appendKeys[key]; ok {
			notes = append(notes, note)
		}
	}
	sort.Slice(notes, func(i, j int) bool {
		if !notes[i].RecordedAt.Equal(notes[j].RecordedAt) {
			return notes[i].RecordedAt.Before(notes[j].RecordedAt)
		}
		return notes[i].ID < notes[j].ID
	})
	for _, note := range notes {
		note.ProjectID = projectID
		if err := resource.InsertNote(ctx, tx, note); err != nil {
			return fmt.Errorf("append resource note %s: %w", note.ID, err)
		}
	}
	return nil
}
