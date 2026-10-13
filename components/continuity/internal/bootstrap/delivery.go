package bootstrap

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"time"
)

// Delivery is one packet the SessionStart hook handed a client session.
type Delivery struct {
	ProjectID    string
	Client       string
	SessionID    string
	DeliveredAt  time.Time
	CheckpointID string
	PacketSHA256 string
}

const deliveryRetention = 30 * 24 * time.Hour

// PacketHash names a packet's rendered Markdown, the part the hook and the
// MCP tool both produce; lines the hook appends (sync, unnarrated commits,
// roadmap scan) are not part of it.
func PacketHash(markdown string) string {
	sum := sha256.Sum256([]byte(markdown))
	return hex.EncodeToString(sum[:])
}

// RecordDelivery stores a delivery and prunes this project's deliveries older
// than the retention window.
func RecordDelivery(ctx context.Context, db *sql.DB, value Delivery) error {
	if _, err := db.ExecContext(ctx, `INSERT OR REPLACE INTO bootstrap_deliveries(project_id, client, session_id, delivered_at, checkpoint_id, packet_sha256) VALUES (?, ?, ?, ?, ?, ?)`,
		value.ProjectID, value.Client, value.SessionID, value.DeliveredAt.UTC().Format(time.RFC3339Nano), value.CheckpointID, value.PacketSHA256); err != nil {
		return err
	}
	_, err := db.ExecContext(ctx, `DELETE FROM bootstrap_deliveries WHERE project_id=? AND delivered_at < ?`, value.ProjectID, value.DeliveredAt.Add(-deliveryRetention).UTC().Format(time.RFC3339Nano))
	return err
}

// DeliveredSince returns the project's deliveries at or after since, newest
// first; with a session ID, only that session's.
func DeliveredSince(ctx context.Context, db *sql.DB, projectID, sessionID string, since time.Time) ([]Delivery, error) {
	rows, err := db.QueryContext(ctx, `SELECT client, session_id, delivered_at, checkpoint_id, packet_sha256 FROM bootstrap_deliveries
		WHERE project_id=? AND delivered_at >= ? AND (?='' OR session_id=?) ORDER BY delivered_at DESC`,
		projectID, since.UTC().Format(time.RFC3339Nano), sessionID, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []Delivery
	for rows.Next() {
		value := Delivery{ProjectID: projectID}
		var at string
		if err := rows.Scan(&value.Client, &value.SessionID, &at, &value.CheckpointID, &value.PacketSHA256); err != nil {
			return nil, err
		}
		if value.DeliveredAt, err = time.Parse(time.RFC3339Nano, at); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

// DeliveredUnchanged reports whether deliveries show that the asking session
// already holds a packet with this hash: the newest delivery matches, and the
// deliveries belong to one session. Deliveries to several sessions cannot say
// which one is asking, so they never count as unchanged.
func DeliveredUnchanged(deliveries []Delivery, hash string) (Delivery, bool) {
	if len(deliveries) == 0 {
		return Delivery{}, false
	}
	newest := deliveries[0]
	for _, value := range deliveries[1:] {
		if value.Client != newest.Client || value.SessionID != newest.SessionID {
			return Delivery{}, false
		}
	}
	return newest, newest.PacketSHA256 == hash
}

// CheckpointID returns the ID of the packet's selected checkpoint, or "".
func (p Packet) CheckpointID() string {
	if p.Checkpoint == nil {
		return ""
	}
	return p.Checkpoint.ID
}
