package attribution

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const (
	DefaultReadLimit = 20
	MaxReadLimit     = 100
)

type Snapshot struct {
	Invocations []Invocation `json:"invocations"`
}

func (s Service) Snapshot(ctx context.Context, projectID string, limit int) (Snapshot, error) {
	invocations, err := s.RecentInvocations(ctx, projectID, limit)
	return Snapshot{Invocations: invocations}, err
}

func readLimit(limit int) (int, error) {
	if limit == 0 {
		return DefaultReadLimit, nil
	}
	if limit < 0 || limit > MaxReadLimit {
		return 0, fmt.Errorf("attribution and policy read limit must be between 1 and %d", MaxReadLimit)
	}
	return limit, nil
}

func parseStoredTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse stored attribution time: %w", err)
	}
	return parsed, nil
}

func nullText(value sql.NullString) string {
	if value.Valid {
		return value.String
	}
	return ""
}

func (s Service) RecentInvocations(ctx context.Context, projectID string, limit int) ([]Invocation, error) {
	if err := required("project ID", projectID); err != nil {
		return nil, err
	}
	limit, err := readLimit(limit)
	if err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id, session_id, parent_invocation_id, actor_type, client, client_version, provider, model, identity_source, occurred_at, received_at, schema_version, effort_requested_raw, effort_requested_normalized, effort_requested_source, effort_requested_authority, effort_effective_raw, effort_effective_normalized, effort_effective_source, effort_effective_authority FROM actor_invocations WHERE project_id = ? ORDER BY occurred_at DESC, id DESC LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := make([]Invocation, 0)
	for rows.Next() {
		var record Invocation
		var session, parent, client, clientVersion, provider, model sql.NullString
		var requestedRaw, requestedNormalized, requestedSource, requestedAuthority sql.NullString
		var effectiveRaw, effectiveNormalized, effectiveSource, effectiveAuthority sql.NullString
		var occurred, received string
		if err := rows.Scan(&record.ID, &session, &parent, &record.ActorType, &client, &clientVersion, &provider, &model, &record.IdentitySource, &occurred, &received, &record.SchemaVersion, &requestedRaw, &requestedNormalized, &requestedSource, &requestedAuthority, &effectiveRaw, &effectiveNormalized, &effectiveSource, &effectiveAuthority); err != nil {
			return nil, err
		}
		record.ProjectID = projectID
		record.SessionID, record.ParentInvocationID = nullText(session), nullText(parent)
		record.Client, record.ClientVersion = nullText(client), nullText(clientVersion)
		record.Provider, record.Model = nullText(provider), nullText(model)
		record.EffortRequested = scanEffort(requestedRaw, requestedNormalized, requestedSource, requestedAuthority)
		record.EffortEffective = scanEffort(effectiveRaw, effectiveNormalized, effectiveSource, effectiveAuthority)
		if record.OccurredAt, err = parseStoredTime(occurred); err != nil {
			return nil, err
		}
		if record.ReceivedAt, err = parseStoredTime(received); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func scanEffort(raw, normalized, source, authority sql.NullString) *Effort {
	if !raw.Valid {
		return nil
	}
	return &Effort{Raw: raw.String, Normalized: nullText(normalized), Source: nullText(source), Authority: nullText(authority)}
}
