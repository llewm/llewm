-- Bootstrap deliveries (8g of continuity-auditing). The SessionStart hook
-- records a hash of each packet it hands a client, so continuity_bootstrap can
-- say the packet is unchanged instead of repeating the several kilobytes the
-- session already holds. Device-local, never synced, pruned after 30 days.
CREATE TABLE bootstrap_deliveries (
    project_id TEXT NOT NULL REFERENCES projects(id),
    client TEXT NOT NULL,
    session_id TEXT NOT NULL,
    delivered_at TEXT NOT NULL,
    checkpoint_id TEXT NOT NULL,
    packet_sha256 TEXT NOT NULL,
    PRIMARY KEY (project_id, client, session_id, delivered_at)
) STRICT;
CREATE INDEX bootstrap_deliveries_time ON bootstrap_deliveries(project_id, delivered_at);
