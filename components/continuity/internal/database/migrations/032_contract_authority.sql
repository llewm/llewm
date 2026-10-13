CREATE TABLE contract_authority_migrations (
 id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL REFERENCES projects(id),
 contract_id TEXT NOT NULL,
 sequence INTEGER NOT NULL CHECK(sequence > 0),
 predecessor_id TEXT NOT NULL,
 target_mode TEXT NOT NULL CHECK(target_mode IN ('repository','llmem_registry')),
 destination TEXT NOT NULL,
 request_key TEXT NOT NULL,
 request_sha256 TEXT NOT NULL,
 creator_invocation_id TEXT,
 recorded_at TEXT NOT NULL,
 payload_json TEXT NOT NULL,
 UNIQUE(project_id, request_key),
 UNIQUE(project_id, contract_id, sequence),
 UNIQUE(project_id, contract_id, predecessor_id),
 FOREIGN KEY(creator_invocation_id,project_id) REFERENCES actor_invocations(id,project_id)
) STRICT;
CREATE TRIGGER contract_authority_no_update BEFORE UPDATE ON contract_authority_migrations
BEGIN SELECT RAISE(ABORT,'contract authority history is append-only'); END;
CREATE TRIGGER contract_authority_no_delete BEFORE DELETE ON contract_authority_migrations
BEGIN SELECT RAISE(ABORT,'contract authority history is append-only'); END;
