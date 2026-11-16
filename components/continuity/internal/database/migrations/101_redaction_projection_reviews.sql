-- redaction_projection_reviews records a reviewer's acceptance of the safe,
-- deterministic redaction projection of one historical observation that the
-- current admission rules reject. Import recomputes the projection from the
-- exact source bytes and requires projected_sha256 to match before it decodes
-- the projection in place of the source; the source envelope is still retained
-- unchanged, so exports and later replays stay byte-identical to other hosts.
-- Hashes and review provenance only: no source content is stored here.
-- Device-local and never synced; each host reviews its own projections.
CREATE TABLE redaction_projection_reviews (
    project_id TEXT NOT NULL REFERENCES projects(id),
    source_sha256 TEXT NOT NULL CHECK (length(source_sha256) = 64),
    projected_sha256 TEXT NOT NULL CHECK (length(projected_sha256) = 64 AND projected_sha256 <> source_sha256),
    record_type TEXT NOT NULL CHECK (record_type IN ('checkpoint', 'checkpoint_action', 'decision', 'resource_note')),
    record_id TEXT NOT NULL CHECK (record_id <> ''),
    record_revision INTEGER NOT NULL CHECK (record_revision >= 0),
    reviewer TEXT NOT NULL CHECK (reviewer <> ''),
    reason TEXT NOT NULL CHECK (reason <> ''),
    reviewed_at TEXT NOT NULL,
    PRIMARY KEY (project_id, source_sha256)
) STRICT;
