-- Disposable selector cache tracking, separate from canonical and raw inventory
-- generations. A token is not proof of validation. Semantic consumers establish
-- a complete baseline before reusing selectors across archive journal deltas.
CREATE TABLE history_index_dependency_generation (
 project_id TEXT PRIMARY KEY REFERENCES projects(id),
 token TEXT NOT NULL CHECK(length(token)=32 AND token NOT GLOB '*[^0-9a-f]*')
) STRICT;
INSERT INTO history_index_dependency_generation SELECT id,lower(hex(randomblob(16))) FROM projects;
CREATE TRIGGER history_dependency_project_insert AFTER INSERT ON projects BEGIN
 INSERT INTO history_index_dependency_generation(project_id,token) VALUES(new.id,lower(hex(randomblob(16))));
END;
CREATE TRIGGER history_dependency_records_insert AFTER INSERT ON history_index_dependency_records BEGIN
 INSERT INTO history_index_dependency_generation(project_id,token) VALUES(new.project_id,lower(hex(randomblob(16)))) ON CONFLICT(project_id) DO UPDATE SET token=excluded.token;
END;
CREATE TRIGGER history_dependency_records_update AFTER UPDATE ON history_index_dependency_records
WHEN old.project_id IS NOT new.project_id OR old.envelope_hash IS NOT new.envelope_hash OR old.index_version IS NOT new.index_version OR old.supported IS NOT new.supported OR old.selectors_sha256 IS NOT new.selectors_sha256 BEGIN
 INSERT INTO history_index_dependency_generation(project_id,token) VALUES(old.project_id,lower(hex(randomblob(16)))) ON CONFLICT(project_id) DO UPDATE SET token=excluded.token;
 INSERT INTO history_index_dependency_generation(project_id,token) VALUES(new.project_id,lower(hex(randomblob(16)))) ON CONFLICT(project_id) DO UPDATE SET token=excluded.token;
END;
CREATE TRIGGER history_dependency_records_delete AFTER DELETE ON history_index_dependency_records BEGIN
 INSERT INTO history_index_dependency_generation(project_id,token) VALUES(old.project_id,lower(hex(randomblob(16)))) ON CONFLICT(project_id) DO UPDATE SET token=excluded.token;
END;
CREATE TRIGGER history_dependency_edges_insert AFTER INSERT ON history_index_dependencies BEGIN
 INSERT INTO history_index_dependency_generation(project_id,token) VALUES(new.project_id,lower(hex(randomblob(16)))) ON CONFLICT(project_id) DO UPDATE SET token=excluded.token;
END;
CREATE TRIGGER history_dependency_edges_update AFTER UPDATE ON history_index_dependencies
WHEN old.project_id IS NOT new.project_id OR old.envelope_hash IS NOT new.envelope_hash OR old.target_type IS NOT new.target_type OR old.target_id IS NOT new.target_id OR old.selection_mode IS NOT new.selection_mode OR old.target_revision IS NOT new.target_revision BEGIN
 INSERT INTO history_index_dependency_generation(project_id,token) VALUES(old.project_id,lower(hex(randomblob(16)))) ON CONFLICT(project_id) DO UPDATE SET token=excluded.token;
 INSERT INTO history_index_dependency_generation(project_id,token) VALUES(new.project_id,lower(hex(randomblob(16)))) ON CONFLICT(project_id) DO UPDATE SET token=excluded.token;
END;
CREATE TRIGGER history_dependency_edges_delete AFTER DELETE ON history_index_dependencies BEGIN
 INSERT INTO history_index_dependency_generation(project_id,token) VALUES(old.project_id,lower(hex(randomblob(16)))) ON CONFLICT(project_id) DO UPDATE SET token=excluded.token;
END;

-- Envelope-first publication probes must stream a bounded ordered membership
-- range before looking up endpoint markers, including for absent envelopes.
DROP INDEX history_index_envelope_members;
CREATE INDEX history_index_envelope_members ON history_index_block_members(project_id,envelope_hash,block_hash);
