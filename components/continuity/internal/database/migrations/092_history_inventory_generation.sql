-- Project-scoped disposable inventory tracking, separate from canonical changes.
-- A token is not a validation receipt: consumers first verify exact source bytes.
CREATE TABLE history_index_inventory_generation (
 project_id TEXT PRIMARY KEY REFERENCES projects(id),
 token TEXT NOT NULL CHECK(length(token)=32 AND token NOT GLOB '*[^0-9a-f]*')
) STRICT;
INSERT INTO history_index_inventory_generation SELECT id,lower(hex(randomblob(16))) FROM projects;
CREATE TRIGGER history_inventory_project_insert AFTER INSERT ON projects BEGIN
 INSERT INTO history_index_inventory_generation(project_id,token) VALUES(new.id,lower(hex(randomblob(16))));
END;
CREATE TRIGGER history_inventory_history_index_blocks_insert AFTER INSERT ON history_index_blocks BEGIN
 INSERT INTO history_index_inventory_generation(project_id,token) VALUES(new.project_id,lower(hex(randomblob(16)))) ON CONFLICT(project_id) DO UPDATE SET token=excluded.token;
END;
CREATE TRIGGER history_inventory_history_index_blocks_update AFTER UPDATE ON history_index_blocks WHEN old."project_id" IS NOT new."project_id" OR old."block_hash" IS NOT new."block_hash" OR old."index_version" IS NOT new."index_version" BEGIN
 INSERT INTO history_index_inventory_generation(project_id,token) VALUES(old.project_id,lower(hex(randomblob(16)))) ON CONFLICT(project_id) DO UPDATE SET token=excluded.token;
 INSERT INTO history_index_inventory_generation(project_id,token) VALUES(new.project_id,lower(hex(randomblob(16)))) ON CONFLICT(project_id) DO UPDATE SET token=excluded.token;
END;
CREATE TRIGGER history_inventory_history_index_blocks_delete AFTER DELETE ON history_index_blocks BEGIN
 INSERT INTO history_index_inventory_generation(project_id,token) VALUES(old.project_id,lower(hex(randomblob(16)))) ON CONFLICT(project_id) DO UPDATE SET token=excluded.token;
END;
CREATE TRIGGER history_inventory_history_index_envelopes_insert AFTER INSERT ON history_index_envelopes BEGIN
 INSERT INTO history_index_inventory_generation(project_id,token) VALUES(new.project_id,lower(hex(randomblob(16)))) ON CONFLICT(project_id) DO UPDATE SET token=excluded.token;
END;
CREATE TRIGGER history_inventory_history_index_envelopes_update AFTER UPDATE ON history_index_envelopes WHEN old."project_id" IS NOT new."project_id" OR old."envelope_hash" IS NOT new."envelope_hash" OR old."record_type" IS NOT new."record_type" OR old."record_id" IS NOT new."record_id" OR old."record_revision" IS NOT new."record_revision" OR old."envelope_json" IS NOT new."envelope_json" BEGIN
 INSERT INTO history_index_inventory_generation(project_id,token) VALUES(old.project_id,lower(hex(randomblob(16)))) ON CONFLICT(project_id) DO UPDATE SET token=excluded.token;
 INSERT INTO history_index_inventory_generation(project_id,token) VALUES(new.project_id,lower(hex(randomblob(16)))) ON CONFLICT(project_id) DO UPDATE SET token=excluded.token;
END;
CREATE TRIGGER history_inventory_history_index_envelopes_delete AFTER DELETE ON history_index_envelopes BEGIN
 INSERT INTO history_index_inventory_generation(project_id,token) VALUES(old.project_id,lower(hex(randomblob(16)))) ON CONFLICT(project_id) DO UPDATE SET token=excluded.token;
END;
CREATE TRIGGER history_inventory_history_index_block_members_insert AFTER INSERT ON history_index_block_members BEGIN
 INSERT INTO history_index_inventory_generation(project_id,token) VALUES(new.project_id,lower(hex(randomblob(16)))) ON CONFLICT(project_id) DO UPDATE SET token=excluded.token;
END;
CREATE TRIGGER history_inventory_history_index_block_members_update AFTER UPDATE ON history_index_block_members WHEN old."project_id" IS NOT new."project_id" OR old."block_hash" IS NOT new."block_hash" OR old."envelope_hash" IS NOT new."envelope_hash" BEGIN
 INSERT INTO history_index_inventory_generation(project_id,token) VALUES(old.project_id,lower(hex(randomblob(16)))) ON CONFLICT(project_id) DO UPDATE SET token=excluded.token;
 INSERT INTO history_index_inventory_generation(project_id,token) VALUES(new.project_id,lower(hex(randomblob(16)))) ON CONFLICT(project_id) DO UPDATE SET token=excluded.token;
END;
CREATE TRIGGER history_inventory_history_index_block_members_delete AFTER DELETE ON history_index_block_members BEGIN
 INSERT INTO history_index_inventory_generation(project_id,token) VALUES(old.project_id,lower(hex(randomblob(16)))) ON CONFLICT(project_id) DO UPDATE SET token=excluded.token;
END;
CREATE TRIGGER history_inventory_history_index_refs_insert AFTER INSERT ON history_index_refs BEGIN
 INSERT INTO history_index_inventory_generation(project_id,token) VALUES(new.project_id,lower(hex(randomblob(16)))) ON CONFLICT(project_id) DO UPDATE SET token=excluded.token;
END;
CREATE TRIGGER history_inventory_history_index_refs_update AFTER UPDATE ON history_index_refs WHEN old."project_id" IS NOT new."project_id" OR old."envelope_hash" IS NOT new."envelope_hash" OR old."ref_kind" IS NOT new."ref_kind" OR old."ref_id" IS NOT new."ref_id" BEGIN
 INSERT INTO history_index_inventory_generation(project_id,token) VALUES(old.project_id,lower(hex(randomblob(16)))) ON CONFLICT(project_id) DO UPDATE SET token=excluded.token;
 INSERT INTO history_index_inventory_generation(project_id,token) VALUES(new.project_id,lower(hex(randomblob(16)))) ON CONFLICT(project_id) DO UPDATE SET token=excluded.token;
END;
CREATE TRIGGER history_inventory_history_index_refs_delete AFTER DELETE ON history_index_refs BEGIN
 INSERT INTO history_index_inventory_generation(project_id,token) VALUES(old.project_id,lower(hex(randomblob(16)))) ON CONFLICT(project_id) DO UPDATE SET token=excluded.token;
END;
CREATE TRIGGER history_inventory_history_index_publications_insert AFTER INSERT ON history_index_publications BEGIN
 INSERT INTO history_index_inventory_generation(project_id,token) VALUES(new.project_id,lower(hex(randomblob(16)))) ON CONFLICT(project_id) DO UPDATE SET token=excluded.token;
END;
CREATE TRIGGER history_inventory_history_index_publications_update AFTER UPDATE ON history_index_publications WHEN old."project_id" IS NOT new."project_id" OR old."endpoint_hash" IS NOT new."endpoint_hash" OR old."block_hash" IS NOT new."block_hash" BEGIN
 INSERT INTO history_index_inventory_generation(project_id,token) VALUES(old.project_id,lower(hex(randomblob(16)))) ON CONFLICT(project_id) DO UPDATE SET token=excluded.token;
 INSERT INTO history_index_inventory_generation(project_id,token) VALUES(new.project_id,lower(hex(randomblob(16)))) ON CONFLICT(project_id) DO UPDATE SET token=excluded.token;
END;
CREATE TRIGGER history_inventory_history_index_publications_delete AFTER DELETE ON history_index_publications BEGIN
 INSERT INTO history_index_inventory_generation(project_id,token) VALUES(old.project_id,lower(hex(randomblob(16)))) ON CONFLICT(project_id) DO UPDATE SET token=excluded.token;
END;
