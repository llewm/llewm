-- Contracts as marked Markdown sections (6f of continuity-multi-repo-sync,
-- decision contracts-markdown-canonical). Project shapes live in repository
-- Markdown under <!-- continuity:contract ID --> markers; the database keeps
-- only an append-only index of the section hashes it last accepted, so drift
-- (a changed, new, or vanished section) can be reported. The governed contract
-- records, their source bindings, lifecycle transitions, and authority
-- migrations are retired; their content is re-expressed as marked sections
-- and the pre-migration backup keeps the rows.
DROP TABLE contract_authority_migrations;
DROP TABLE governed_contract_transitions;
DROP TABLE governed_contract_source_bindings;
DROP TABLE governed_contract_revisions;

CREATE TABLE contract_index_entries (
    project_id TEXT NOT NULL REFERENCES projects(id),
    contract_id TEXT NOT NULL CHECK (contract_id <> ''),
    path TEXT NOT NULL,
    title TEXT NOT NULL,
    kind TEXT NOT NULL,
    -- NULL records that an indexed section was accepted as removed.
    sha256 TEXT,
    recorded_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version = 1)
) STRICT;

CREATE INDEX contract_index_entries_latest ON contract_index_entries(project_id, contract_id, recorded_at);
CREATE TRIGGER contract_index_entries_no_update BEFORE UPDATE ON contract_index_entries BEGIN SELECT RAISE(ABORT,'contract index entries are append-only'); END;
CREATE TRIGGER contract_index_entries_no_delete BEFORE DELETE ON contract_index_entries BEGIN SELECT RAISE(ABORT,'contract index entries are append-only'); END;
