package activity

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/project"
)

type Store struct{ DB *sql.DB }

// EnsureProject returns the project registered at root, creating it with a new
// opaque ID when absent. Live checkouts resolve through project.Resolver.Ensure,
// which also recognizes moves.
func (s Store) EnsureProject(ctx context.Context, root, name string) (string, error) {
	return project.Resolver{DB: s.DB}.Register(ctx, root, name)
}

func (s Store) Append(ctx context.Context, event Event) error {
	_, err := s.AppendOnce(ctx, event)
	return err
}

// AppendOnce reports whether this event ID was newly inserted. Stable derived
// event IDs can use this as an atomic claim without replacing prior observations.
func (s Store) AppendOnce(ctx context.Context, event Event) (bool, error) {
	payload, err := json.Marshal(event.Payload)
	if err != nil {
		return false, err
	}
	hash := sha256.Sum256(payload)
	result, err := s.DB.ExecContext(ctx, `INSERT OR IGNORE INTO events(
        id, occurred_at, received_at, project_id, session_id, task_id, client,
        event_type, category, actor_type, actor_model, outcome, duration_ms,
        payload_json, schema_version, content_hash
    ) VALUES (?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, ?)`,
		event.ID, event.OccurredAt.Format(time.RFC3339Nano), event.ReceivedAt.Format(time.RFC3339Nano),
		event.ProjectID, event.SessionID, event.TaskID, event.Client, event.EventType, event.Category,
		event.ActorType, event.ActorModel, event.Outcome, event.DurationMS, string(payload), event.SchemaVersion,
		hex.EncodeToString(hash[:]))
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows == 1, err
}

func (s Store) ObserveSession(ctx context.Context, projectID, sessionID, client, model, eventType string, at time.Time) error {
	if sessionID == "" {
		return nil
	}
	status := "active"
	var endedAt any
	if eventType == "sessionend" {
		status = "ended"
		endedAt = at.Format(time.RFC3339Nano)
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO sessions(id, project_id, client, model, started_at, ended_at, status)
      VALUES (?, ?, ?, NULLIF(?, ''), ?, ?, ?)
      ON CONFLICT(id) DO UPDATE SET model=COALESCE(excluded.model, sessions.model), ended_at=COALESCE(excluded.ended_at, sessions.ended_at), status=excluded.status`,
		sessionID, projectID, client, model, at.Format(time.RFC3339Nano), endedAt, status)
	return err
}
