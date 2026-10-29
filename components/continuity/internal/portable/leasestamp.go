package portable

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/lease"
)

// Lease stamps written under a token travel so every machine can flag writes made under a superseded
// token (lease.StaleWrites); stale-write resolutions travel so each is settled
// once. The record a stamp names may arrive later or not at all, so import does
// not require it. Both are their own record types, so an older build defers
// them instead of refusing the checkpoints and plans they describe.

type leaseStampPayload struct {
	RecordKind               string `json:"record_kind"`
	RecordID                 string `json:"record_id"`
	ScopeKind                string `json:"scope_kind"`
	ScopeID                  string `json:"scope_id"`
	State                    string `json:"state"`
	Token                    int64  `json:"token,omitempty"`
	Holder                   string `json:"holder,omitempty"`
	SessionID                string `json:"session_id,omitempty"`
	InstanceID               string `json:"instance_id,omitempty"`
	OperationalSchemaVersion int    `json:"operational_schema_version"`
}

type staleResolutionPayload struct {
	RecordKind               string `json:"record_kind"`
	RecordID                 string `json:"record_id"`
	Accepted                 string `json:"accepted"`
	Resolver                 string `json:"resolver"`
	OperationalSchemaVersion int    `json:"operational_schema_version"`
}

type portableLeaseRecords struct {
	Stamps      map[string]lease.Stamp
	Resolutions map[string]lease.StaleResolution
}

func newPortableLeaseRecords() portableLeaseRecords {
	return portableLeaseRecords{Stamps: map[string]lease.Stamp{}, Resolutions: map[string]lease.StaleResolution{}}
}

func leaseStampRecordID(stamp lease.Stamp) string { return stamp.RecordKind + ":" + stamp.RecordID }

func leaseStampIndex(payload leaseStampPayload) envelopeIndex {
	return envelopeIndex{Title: payload.ScopeKind + ":" + payload.ScopeID, Status: payload.State}
}

func staleResolutionIndex(payload staleResolutionPayload) envelopeIndex {
	return envelopeIndex{Title: lease.StaleSubject(payload.RecordKind, payload.RecordID), Status: payload.Accepted}
}

func loadLeaseSpecs(ctx context.Context, source queryer, projectID, projectRoot, lineageID string) ([]EnvelopeSpec, error) {
	stamps, err := lease.Stamps(ctx, selectedExportSource(source, "lease_stamps"), projectID)
	if err != nil {
		return nil, err
	}
	resolutions, err := lease.StaleResolutions(ctx, selectedExportSource(source, "stale_write_resolutions"), projectID)
	if err != nil {
		return nil, err
	}
	var specs []EnvelopeSpec
	add := func(recordType, id string, recorded time.Time, authority string, index envelopeIndex, payload any) error {
		body, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		indexBody, err := json.Marshal(index)
		if err != nil {
			return err
		}
		spec := EnvelopeSpec{LineageID: lineageID, RecordType: recordType, RecordSchemaVersion: 1, RecordID: id, RecordedAt: recorded, Authority: authority, Index: indexBody, Refs: []Reference{}, Payload: body}
		encoded, err := EncodeEnvelope(spec)
		if err != nil {
			return err
		}
		// Refuse to export what this build could not import.
		if _, err := decodeLeaseEnvelope(newPortableLeaseRecords(), encoded.Envelope); err != nil {
			return err
		}
		specs = append(specs, spec)
		return nil
	}
	for _, stamp := range stamps {
		if stamp.State == lease.Unleased {
			// Only a token can be superseded; unleased stamps stay local.
			continue
		}
		payload := leaseStampPayload{RecordKind: stamp.RecordKind, RecordID: stamp.RecordID, ScopeKind: stamp.ScopeKind, ScopeID: stamp.ScopeID, State: stamp.State, Token: stamp.Token, Holder: stamp.Holder, SessionID: stamp.SessionID, InstanceID: stamp.InstanceID, OperationalSchemaVersion: lease.StampSchemaVersion}
		if err := add("lease_stamp", leaseStampRecordID(stamp), stamp.RecordedAt, "machine_observed", leaseStampIndex(payload), payload); err != nil {
			return nil, err
		}
	}
	for _, value := range resolutions {
		payload := staleResolutionPayload{RecordKind: value.RecordKind, RecordID: value.RecordID, Accepted: value.Accepted, Resolver: portableReviewText(value.Resolver, projectID, projectRoot), OperationalSchemaVersion: value.SchemaVersion}
		if err := add("stale_write_resolution", value.ID, value.RecordedAt, value.Authority, staleResolutionIndex(payload), payload); err != nil {
			return nil, err
		}
	}
	return specs, nil
}

// decodeLeaseEnvelope decodes lease stamp and stale-write resolution
// envelopes, reporting false for other types.
func decodeLeaseEnvelope(records portableLeaseRecords, envelope Envelope) (bool, error) {
	recorded, timeErr := parsePortableTime(envelope.RecordedAt)
	switch envelope.RecordType {
	case "lease_stamp":
		var payload leaseStampPayload
		if err := strictDecode(envelope.Payload, &payload); err != nil {
			return true, fmt.Errorf("decode lease stamp %s: %w", envelope.RecordID, err)
		}
		value := lease.Stamp{RecordKind: payload.RecordKind, RecordID: payload.RecordID, ScopeKind: payload.ScopeKind, ScopeID: payload.ScopeID, State: payload.State, Token: payload.Token, Holder: payload.Holder, SessionID: payload.SessionID, InstanceID: payload.InstanceID, RecordedAt: recorded}
		err := lease.ValidateStamp(value)
		if err == nil && value.State == lease.Unleased {
			err = fmt.Errorf("unleased stamps do not travel")
		}
		if timeErr != nil || envelope.RecordRevision != 0 || envelope.Authority != "machine_observed" || envelope.RecordID != leaseStampRecordID(value) || payload.OperationalSchemaVersion != lease.StampSchemaVersion || err != nil {
			return true, fmt.Errorf("lease stamp %s is invalid: %v", envelope.RecordID, firstError(err, timeErr))
		}
		if !sameReferences(envelope.Refs, []Reference{}) || !checkpointIndexMatches(envelope.Index, leaseStampIndex(payload)) {
			return true, fmt.Errorf("lease stamp %s metadata does not match payload", envelope.RecordID)
		}
		records.Stamps[identityKey(envelope.RecordType, envelope.RecordID, 0)] = value
		return true, nil
	case "stale_write_resolution":
		var payload staleResolutionPayload
		if err := strictDecode(envelope.Payload, &payload); err != nil {
			return true, fmt.Errorf("decode stale write resolution %s: %w", envelope.RecordID, err)
		}
		value := lease.StaleResolution{ID: envelope.RecordID, RecordKind: payload.RecordKind, RecordID: payload.RecordID, Accepted: payload.Accepted, Resolver: payload.Resolver, Authority: envelope.Authority, RecordedAt: recorded, SchemaVersion: payload.OperationalSchemaVersion}
		normalized, err := lease.ValidateStaleResolution(value)
		if timeErr != nil || envelope.RecordRevision != 0 || !portableIncomingIdentifier(envelope.RecordID) || err != nil {
			return true, fmt.Errorf("stale write resolution %s is invalid: %v", envelope.RecordID, firstError(err, timeErr))
		}
		if !reflect.DeepEqual(normalized, value) {
			return true, fmt.Errorf("stale write resolution %s is not in canonical form", envelope.RecordID)
		}
		if !sameReferences(envelope.Refs, []Reference{}) || !checkpointIndexMatches(envelope.Index, staleResolutionIndex(payload)) {
			return true, fmt.Errorf("stale write resolution %s metadata does not match payload", envelope.RecordID)
		}
		records.Resolutions[identityKey(envelope.RecordType, envelope.RecordID, 0)] = value
		return true, nil
	}
	return false, nil
}

// appendLeaseRecords inserts new stamps and resolutions in recorded order.
func appendLeaseRecords(ctx context.Context, tx *sql.Tx, projectID string, records portableLeaseRecords, appendKeys map[string]struct{}) error {
	stamps := make([]lease.Stamp, 0, len(records.Stamps))
	for key, value := range records.Stamps {
		if _, ok := appendKeys[key]; ok {
			stamps = append(stamps, value)
		}
	}
	sort.Slice(stamps, func(i, j int) bool {
		if !stamps[i].RecordedAt.Equal(stamps[j].RecordedAt) {
			return stamps[i].RecordedAt.Before(stamps[j].RecordedAt)
		}
		return leaseStampRecordID(stamps[i]) < leaseStampRecordID(stamps[j])
	})
	for _, value := range stamps {
		if err := lease.InsertStamp(ctx, tx, projectID, value); err != nil {
			return fmt.Errorf("append lease stamp %s: %w", leaseStampRecordID(value), err)
		}
	}
	resolutions := make([]lease.StaleResolution, 0, len(records.Resolutions))
	for key, value := range records.Resolutions {
		if _, ok := appendKeys[key]; ok {
			resolutions = append(resolutions, value)
		}
	}
	sort.Slice(resolutions, func(i, j int) bool {
		if !resolutions[i].RecordedAt.Equal(resolutions[j].RecordedAt) {
			return resolutions[i].RecordedAt.Before(resolutions[j].RecordedAt)
		}
		return resolutions[i].ID < resolutions[j].ID
	})
	for _, value := range resolutions {
		value.ProjectID = projectID
		if err := lease.InsertStaleResolution(ctx, tx, value); err != nil {
			return fmt.Errorf("append stale write resolution %s: %w", value.ID, err)
		}
	}
	return nil
}
