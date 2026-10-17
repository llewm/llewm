-- Retain the canonical transport form at first export or successful import.
-- Operational rows remain attributable observations; another host's redaction
-- context must never re-encode an already portable record.
CREATE TABLE portable_envelopes (
    project_id TEXT NOT NULL REFERENCES projects(id),
    record_type TEXT NOT NULL,
    record_id TEXT NOT NULL,
    record_revision INTEGER NOT NULL CHECK(record_revision >= 0),
    envelope_json TEXT NOT NULL,
    origin TEXT NOT NULL CHECK(origin IN ('export','import')),
    PRIMARY KEY(project_id,record_type,record_id,record_revision)
) STRICT;
CREATE TRIGGER portable_envelopes_no_update BEFORE UPDATE ON portable_envelopes BEGIN SELECT RAISE(ABORT,'portable envelopes are append-only'); END;
CREATE TRIGGER portable_envelopes_no_delete BEFORE DELETE ON portable_envelopes BEGIN SELECT RAISE(ABORT,'portable envelopes are append-only'); END;
