package attribution

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/redact"
)

type Service struct{ DB *sql.DB }

const maxTextBytes = 16 << 10

func prepareID(id string) string {
	if strings.TrimSpace(id) == "" {
		return activity.NewID()
	}
	return strings.TrimSpace(id)
}

func prepareText(value string) (string, error) {
	value = strings.TrimSpace(redact.String(value))
	if len(value) > maxTextBytes {
		return "", fmt.Errorf("attribution field exceeds 16 KiB limit")
	}
	return value, nil
}

func prepareTimes(occurred, received time.Time) (time.Time, time.Time) {
	now := time.Now().UTC()
	if occurred.IsZero() {
		occurred = now
	}
	if received.IsZero() {
		received = now
	}
	return occurred.UTC(), received.UTC()
}

func version(value int) (int, error) {
	if value == 0 {
		return SchemaVersion, nil
	}
	if value != SchemaVersion {
		return 0, fmt.Errorf("unsupported attribution schema version %d", value)
	}
	return value, nil
}

func invocationVersion(value int) (int, error) {
	if value == 0 {
		return InvocationSchemaVersion, nil
	}
	if value != InvocationSchemaVersion {
		return 0, fmt.Errorf("unsupported invocation schema version %d", value)
	}
	return value, nil
}

func required(name, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", name)
	}
	return nil
}

func oneOf(name, value string, allowed ...string) error {
	for _, candidate := range allowed {
		if value == candidate {
			return nil
		}
	}
	return fmt.Errorf("unsupported %s %q", name, value)
}

func (s Service) AppendInvocation(ctx context.Context, record Invocation) (Invocation, error) {
	if err := required("project ID", record.ProjectID); err != nil {
		return Invocation{}, err
	}
	if record.ActorType == "" {
		record.ActorType = "unknown"
	}
	if err := oneOf("actor type", record.ActorType, "unknown", "human", "agent", "system", "tool"); err != nil {
		return Invocation{}, err
	}
	if record.IdentitySource == "" {
		record.IdentitySource = "unknown"
	}
	var err error
	for _, field := range []*string{&record.SessionID, &record.ParentInvocationID, &record.ActorType, &record.Client, &record.ClientVersion, &record.Provider, &record.Model, &record.IdentitySource} {
		*field, err = prepareText(*field)
		if err != nil {
			return Invocation{}, err
		}
	}
	if record.EffortRequested, err = prepareEffort("requested effort", record.EffortRequested); err != nil {
		return Invocation{}, err
	}
	if record.EffortEffective, err = prepareEffort("effective effort", record.EffortEffective); err != nil {
		return Invocation{}, err
	}
	record.ID = prepareID(record.ID)
	record.OccurredAt, record.ReceivedAt = prepareTimes(record.OccurredAt, record.ReceivedAt)
	record.SchemaVersion, err = invocationVersion(record.SchemaVersion)
	if err != nil {
		return Invocation{}, err
	}
	requested := effortValues(record.EffortRequested)
	effective := effortValues(record.EffortEffective)
	_, err = s.DB.ExecContext(ctx, `INSERT INTO actor_invocations(id, project_id, session_id, parent_invocation_id, actor_type, client, client_version, provider, model, identity_source, occurred_at, received_at, schema_version, effort_requested_raw, effort_requested_normalized, effort_requested_source, effort_requested_authority, effort_effective_raw, effort_effective_normalized, effort_effective_source, effort_effective_authority) VALUES (?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''))`, record.ID, record.ProjectID, record.SessionID, record.ParentInvocationID, record.ActorType, record.Client, record.ClientVersion, record.Provider, record.Model, record.IdentitySource, record.OccurredAt.Format(time.RFC3339Nano), record.ReceivedAt.Format(time.RFC3339Nano), record.SchemaVersion, requested[0], requested[1], requested[2], requested[3], effective[0], effective[1], effective[2], effective[3])
	return record, err
}

func prepareEffort(name string, value *Effort) (*Effort, error) {
	if value == nil {
		return nil, nil
	}
	copy := *value
	var err error
	for _, field := range []*string{&copy.Raw, &copy.Normalized, &copy.Source, &copy.Authority} {
		*field, err = prepareText(*field)
		if err != nil {
			return nil, err
		}
	}
	if err := required(name+" raw value", copy.Raw); err != nil {
		return nil, err
	}
	if err := required(name+" source", copy.Source); err != nil {
		return nil, err
	}
	if err := required(name+" authority", copy.Authority); err != nil {
		return nil, err
	}
	if copy.Normalized != "" {
		if err := oneOf(name+" normalized value", copy.Normalized, "minimal", "low", "medium", "high", "xhigh"); err != nil {
			return nil, err
		}
	}
	return &copy, nil
}

func effortValues(value *Effort) [4]string {
	if value == nil {
		return [4]string{}
	}
	return [4]string{value.Raw, value.Normalized, value.Source, value.Authority}
}
