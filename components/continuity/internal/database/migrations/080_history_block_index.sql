-- Derived inventory only: it is not a receipt that a record was applied.
-- Rebuilding or removing it never changes canonical observations.
CREATE TABLE history_index_blocks (
    project_id TEXT NOT NULL REFERENCES projects(id),
    block_hash TEXT NOT NULL,
    index_version INTEGER NOT NULL,
    PRIMARY KEY(project_id, block_hash)
) STRICT;
CREATE TABLE history_index_records (
    project_id TEXT NOT NULL,
    block_hash TEXT NOT NULL,
    envelope_hash TEXT NOT NULL,
    record_type TEXT NOT NULL,
    record_id TEXT NOT NULL,
    record_revision INTEGER NOT NULL,
    envelope_json TEXT NOT NULL,
    PRIMARY KEY(project_id, block_hash, envelope_hash),
    FOREIGN KEY(project_id, block_hash) REFERENCES history_index_blocks(project_id, block_hash) ON DELETE CASCADE
) STRICT;
CREATE INDEX history_index_identity ON history_index_records(project_id, record_type, record_id, record_revision);
CREATE TABLE history_index_refs (
    project_id TEXT NOT NULL,
    block_hash TEXT NOT NULL,
    envelope_hash TEXT NOT NULL,
    ref_kind TEXT NOT NULL,
    ref_id TEXT NOT NULL,
    PRIMARY KEY(project_id, block_hash, envelope_hash, ref_kind, ref_id),
    FOREIGN KEY(project_id, block_hash, envelope_hash) REFERENCES history_index_records(project_id, block_hash, envelope_hash) ON DELETE CASCADE
) STRICT;
CREATE INDEX history_index_dependents ON history_index_refs(project_id, ref_kind, ref_id);

-- Proof that the exact set passed semantic validation at this index version.
-- It is not an import receipt and must be rechecked against local records.
CREATE TABLE history_index_valid_sets (
    project_id TEXT NOT NULL REFERENCES projects(id),
    set_hash TEXT NOT NULL,
    index_version INTEGER NOT NULL,
    PRIMARY KEY(project_id, set_hash)
) STRICT;
