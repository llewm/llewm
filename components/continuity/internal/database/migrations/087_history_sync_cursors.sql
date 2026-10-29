-- Disposable, device-local consumption receipts. Advance only after successful
-- materialization and while the observed database generation still matches.
CREATE TABLE history_sync_cursors (
    project_id TEXT NOT NULL REFERENCES projects(id),
    consumer TEXT NOT NULL,
    sequence INTEGER NOT NULL CHECK(sequence>=0),
    generation TEXT NOT NULL,
    state TEXT NOT NULL,
    checksum TEXT NOT NULL,
    PRIMARY KEY(project_id,consumer)
) STRICT;
