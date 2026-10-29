package portable

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"

	"github.com/llewm/llewm/components/continuity/internal/procedures"
)

// Procedures (checks and runbooks) travel as one envelope per revision. They are
// inert definitions: importing one grants nothing and runs nothing. Completion
// profile bindings stay local, like the profiles they belong to.

type procedurePayload struct {
	Title                    string                 `json:"title"`
	Purpose                  string                 `json:"purpose"`
	Kind                     string                 `json:"kind"`
	Status                   string                 `json:"status"`
	Platforms                []string               `json:"platforms"`
	WorkingDirectory         string                 `json:"working_directory"`
	Parameters               []procedures.Parameter `json:"parameters"`
	DeclaredEffects          []string               `json:"declared_effects"`
	Validations              []string               `json:"validations"`
	PostRunChecks            []string               `json:"post_run_checks"`
	Steps                    []procedures.Step      `json:"steps"`
	Source                   string                 `json:"source"`
	SourceRef                string                 `json:"source_ref,omitempty"`
	OperationalSchemaVersion int                    `json:"operational_schema_version"`
}

func loadProcedureSpecs(ctx context.Context, source queryer, projectID, projectRoot, lineageID string) ([]EnvelopeSpec, error) {
	rows, err := exportRows(ctx, source, "reviewed_procedures", `SELECT `+procedures.Columns+` FROM reviewed_procedures WHERE project_id=? ORDER BY procedure_id,revision`, projectID)
	if err != nil {
		return nil, err
	}
	values, err := procedures.ScanAll(rows, projectID)
	if err != nil {
		return nil, err
	}
	text := func(value string) string { return portableReviewText(value, projectID, projectRoot) }
	texts := func(values []string) []string {
		if values == nil {
			return nil
		}
		return portableReviewStringsUnsorted(values, projectID, projectRoot)
	}
	specs := make([]EnvelopeSpec, 0, len(values))
	for _, value := range values {
		steps := make([]procedures.Step, len(value.Steps))
		for i, step := range value.Steps {
			step.Description, step.ArgvTemplate = text(step.Description), texts(step.ArgvTemplate)
			steps[i] = step
		}
		parameters := value.Parameters
		if parameters != nil {
			parameters = make([]procedures.Parameter, len(value.Parameters))
			for i, parameter := range value.Parameters {
				parameter.Description = text(parameter.Description)
				parameters[i] = parameter
			}
		}
		payload := procedurePayload{
			Title: text(value.Title), Purpose: text(value.Purpose), Kind: value.Kind, Status: value.Status,
			Platforms: texts(value.Platforms), WorkingDirectory: value.WorkingDirectory, Parameters: parameters,
			DeclaredEffects: texts(value.DeclaredEffects), Validations: texts(value.Validations), PostRunChecks: texts(value.PostRunChecks),
			Steps: steps, Source: text(value.Source), SourceRef: text(value.SourceRef), OperationalSchemaVersion: value.SchemaVersion,
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		index, err := json.Marshal(envelopeIndex{Title: payload.Title, Status: payload.Status})
		if err != nil {
			return nil, err
		}
		spec := EnvelopeSpec{LineageID: lineageID, RecordType: "procedure", RecordSchemaVersion: 1, RecordID: value.ID, RecordRevision: value.Revision, RecordedAt: value.RecordedAt, Authority: value.Authority, Index: index, Refs: []Reference{}, Payload: body}
		encoded, err := EncodeEnvelope(spec)
		if err != nil {
			return nil, err
		}
		// Refuse to export what this build could not import.
		if _, err := decodeProcedureEnvelope(portableProcedures{}, encoded.Envelope); err != nil {
			return nil, err
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

type portableProcedures map[string]procedures.Procedure

// decodeProcedureEnvelope decodes a procedure envelope, reporting false for other
// record types. The payload must already be exactly what this build would store.
func decodeProcedureEnvelope(records portableProcedures, envelope Envelope) (bool, error) {
	if envelope.RecordType != "procedure" {
		return false, nil
	}
	var payload procedurePayload
	if err := strictDecode(envelope.Payload, &payload); err != nil {
		return true, fmt.Errorf("decode procedure %s: %w", envelope.RecordID, err)
	}
	recorded, timeErr := parsePortableTime(envelope.RecordedAt)
	if timeErr != nil || envelope.RecordRevision < 1 || envelope.Authority != "caller_asserted" || !portableIncomingIdentifier(envelope.RecordID) || payload.OperationalSchemaVersion != procedures.SchemaVersion {
		return true, fmt.Errorf("procedure %s is invalid", envelope.RecordID)
	}
	value := procedures.Procedure{
		ProjectID: "portable", ID: envelope.RecordID, Revision: envelope.RecordRevision, Title: payload.Title, Purpose: payload.Purpose,
		Kind: payload.Kind, Status: payload.Status, Platforms: payload.Platforms, WorkingDirectory: payload.WorkingDirectory,
		Parameters: payload.Parameters, DeclaredEffects: payload.DeclaredEffects, Validations: payload.Validations, PostRunChecks: payload.PostRunChecks,
		Steps: payload.Steps, Source: payload.Source, SourceRef: payload.SourceRef, Authority: envelope.Authority, RecordedAt: recorded, SchemaVersion: payload.OperationalSchemaVersion,
	}
	normalized, err := procedures.Validate(value)
	if err != nil {
		return true, fmt.Errorf("procedure %s is invalid: %w", envelope.RecordID, err)
	}
	normalized.RecordedAt = recorded
	if !reflect.DeepEqual(filledProcedure(normalized), filledProcedure(value)) {
		return true, fmt.Errorf("procedure %s is not in canonical form", envelope.RecordID)
	}
	if !sameReferences(envelope.Refs, []Reference{}) || !checkpointIndexMatches(envelope.Index, envelopeIndex{Title: payload.Title, Status: payload.Status}) {
		return true, fmt.Errorf("procedure %s metadata does not match payload", envelope.RecordID)
	}
	records[identityKey("procedure", envelope.RecordID, envelope.RecordRevision)] = value
	return true, nil
}

// filledProcedure treats nil and empty lists alike, which is how the stored JSON
// round-trips them, so a canonical payload compares equal to its normalized form.
func filledProcedure(value procedures.Procedure) procedures.Procedure {
	fill := func(values []string) []string {
		if values == nil {
			return []string{}
		}
		return values
	}
	value.Platforms, value.DeclaredEffects, value.Validations, value.PostRunChecks = fill(value.Platforms), fill(value.DeclaredEffects), fill(value.Validations), fill(value.PostRunChecks)
	if value.Parameters == nil {
		value.Parameters = []procedures.Parameter{}
	}
	steps := make([]procedures.Step, len(value.Steps))
	for i, step := range value.Steps {
		step.ArgvTemplate = fill(step.ArgvTemplate)
		steps[i] = step
	}
	value.Steps = steps
	return value
}

// appendProcedures inserts new procedure revisions in revision order.
func appendProcedures(ctx context.Context, tx *sql.Tx, projectID string, values portableProcedures, appendKeys map[string]struct{}) error {
	ordered := make([]procedures.Procedure, 0, len(values))
	for key, value := range values {
		if _, ok := appendKeys[key]; ok {
			ordered = append(ordered, value)
		}
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].ID != ordered[j].ID {
			return ordered[i].ID < ordered[j].ID
		}
		return ordered[i].Revision < ordered[j].Revision
	})
	for _, value := range ordered {
		value.ProjectID = projectID
		if err := procedures.Insert(ctx, tx, value); err != nil {
			return fmt.Errorf("append procedure %s@%d: %w", value.ID, value.Revision, err)
		}
	}
	return nil
}
