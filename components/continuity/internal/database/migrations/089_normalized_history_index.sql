-- Hash-addressed envelope bodies/references; blocks retain only membership.
-- Derived cache only. Preserve all publication intent and uncovered envelopes.
DROP TRIGGER history_sync_history_index_blocks_insert;
DROP TRIGGER history_sync_history_index_blocks_update;
DROP TRIGGER history_sync_history_index_blocks_delete;
DROP TRIGGER history_sync_history_index_records_insert;
DROP TRIGGER history_sync_history_index_records_update;
DROP TRIGGER history_sync_history_index_records_delete;
DROP TRIGGER history_sync_history_index_refs_insert;
DROP TRIGGER history_sync_history_index_refs_update;
DROP TRIGGER history_sync_history_index_refs_delete;
DROP TRIGGER history_sync_actor_invocations_insert;
DROP TRIGGER history_sync_actor_invocations_update;
DROP TRIGGER history_sync_actor_invocations_delete;
CREATE TEMP TABLE history_migration_covered (
 project_id TEXT NOT NULL,envelope_hash TEXT NOT NULL,envelope_json TEXT NOT NULL,
 record_type TEXT NOT NULL,record_id TEXT NOT NULL,record_revision INTEGER NOT NULL,
 PRIMARY KEY(project_id,envelope_hash)
) WITHOUT ROWID;
INSERT INTO history_migration_covered
 SELECT DISTINCT r.project_id,r.envelope_hash,r.envelope_json,r.record_type,r.record_id,r.record_revision
 FROM history_index_records r JOIN history_index_blocks b USING(project_id,block_hash)
 JOIN history_index_publications p USING(project_id,block_hash) WHERE b.index_version=5;
DELETE FROM history_index_blocks AS b WHERE b.index_version<5
 AND NOT EXISTS(SELECT 1 FROM history_index_publications p WHERE p.project_id=b.project_id AND p.block_hash=b.block_hash)
 AND NOT EXISTS(SELECT 1 FROM history_index_records r WHERE r.project_id=b.project_id AND r.block_hash=b.block_hash
   AND NOT EXISTS(SELECT 1 FROM history_migration_covered c WHERE c.project_id=r.project_id AND c.envelope_hash=r.envelope_hash
    AND c.envelope_json=r.envelope_json AND c.record_type=r.record_type AND c.record_id=r.record_id AND c.record_revision=r.record_revision));
DROP TABLE history_migration_covered;
CREATE TABLE history_index_envelopes (
 project_id TEXT NOT NULL REFERENCES projects(id),envelope_hash TEXT NOT NULL,
 record_type TEXT NOT NULL,record_id TEXT NOT NULL,record_revision INTEGER NOT NULL,envelope_json TEXT NOT NULL,
 PRIMARY KEY(project_id,envelope_hash)
) STRICT;
CREATE TABLE history_index_block_members (
 project_id TEXT NOT NULL,block_hash TEXT NOT NULL,envelope_hash TEXT NOT NULL,
 PRIMARY KEY(project_id,block_hash,envelope_hash),
 FOREIGN KEY(project_id,block_hash) REFERENCES history_index_blocks(project_id,block_hash) ON DELETE CASCADE,
 FOREIGN KEY(project_id,envelope_hash) REFERENCES history_index_envelopes(project_id,envelope_hash) ON DELETE CASCADE
) STRICT;
CREATE INDEX history_index_envelope_members ON history_index_block_members(project_id,envelope_hash);
CREATE TABLE history_index_refs_next (
 project_id TEXT NOT NULL,envelope_hash TEXT NOT NULL,ref_kind TEXT NOT NULL,ref_id TEXT NOT NULL,
 PRIMARY KEY(project_id,envelope_hash,ref_kind,ref_id),
 FOREIGN KEY(project_id,envelope_hash) REFERENCES history_index_envelopes(project_id,envelope_hash) ON DELETE CASCADE
) STRICT;
INSERT INTO history_index_envelopes SELECT DISTINCT project_id,envelope_hash,record_type,record_id,record_revision,envelope_json FROM history_index_records;
INSERT INTO history_index_block_members SELECT project_id,block_hash,envelope_hash FROM history_index_records;
INSERT INTO history_index_refs_next SELECT DISTINCT project_id,envelope_hash,ref_kind,ref_id FROM history_index_refs;
DROP TABLE history_index_refs;
DROP TABLE history_index_records;
ALTER TABLE history_index_refs_next RENAME TO history_index_refs;
CREATE INDEX history_index_dependents ON history_index_refs(project_id,ref_kind,ref_id);
CREATE TRIGGER history_index_collect_envelope AFTER DELETE ON history_index_block_members
 WHEN NOT EXISTS(SELECT 1 FROM history_index_block_members WHERE project_id=old.project_id AND envelope_hash=old.envelope_hash)
 BEGIN DELETE FROM history_index_envelopes WHERE project_id=old.project_id AND envelope_hash=old.envelope_hash; END;
UPDATE history_index_blocks SET index_version=6 WHERE index_version=5;
DELETE FROM history_index_valid_sets;
DELETE FROM history_index_narrative_tip;
DELETE FROM history_index_mixed_tip;
CREATE INDEX history_index_identity ON history_index_envelopes(project_id,record_type,record_id,record_revision);
CREATE TRIGGER history_sync_actor_invocations_delete AFTER DELETE ON "actor_invocations" WHEN (EXISTS(SELECT 1 FROM history_index_refs WHERE project_id=old.project_id AND ref_kind='actor_invocation' AND ref_id=old.id) OR EXISTS(SELECT 1 FROM portable_envelopes WHERE project_id=old.project_id AND record_type='actor_invocation' AND record_id=old.id)) BEGIN
    INSERT INTO history_sync_changes(project_id,table_name,row_key,operation) VALUES(old."project_id",'actor_invocations',json_array(old."id"),'delete');
END;
CREATE TRIGGER history_sync_actor_invocations_insert AFTER INSERT ON "actor_invocations" WHEN (EXISTS(SELECT 1 FROM history_index_refs WHERE project_id=new.project_id AND ref_kind='actor_invocation' AND ref_id=new.id) OR EXISTS(SELECT 1 FROM portable_envelopes WHERE project_id=new.project_id AND record_type='actor_invocation' AND record_id=new.id)) BEGIN
    INSERT INTO history_sync_changes(project_id,table_name,row_key,operation) VALUES(new."project_id",'actor_invocations',json_array(new."id"),'insert');
END;
CREATE TRIGGER history_sync_actor_invocations_update AFTER UPDATE ON "actor_invocations" WHEN ((EXISTS(SELECT 1 FROM history_index_refs WHERE project_id=new.project_id AND ref_kind='actor_invocation' AND ref_id=new.id) OR EXISTS(SELECT 1 FROM portable_envelopes WHERE project_id=new.project_id AND record_type='actor_invocation' AND record_id=new.id)) OR (EXISTS(SELECT 1 FROM history_index_refs WHERE project_id=old.project_id AND ref_kind='actor_invocation' AND ref_id=old.id) OR EXISTS(SELECT 1 FROM portable_envelopes WHERE project_id=old.project_id AND record_type='actor_invocation' AND record_id=old.id))) AND (old."id" IS NOT new."id" OR old."project_id" IS NOT new."project_id" OR old."session_id" IS NOT new."session_id" OR old."parent_invocation_id" IS NOT new."parent_invocation_id" OR old."actor_type" IS NOT new."actor_type" OR old."client" IS NOT new."client" OR old."client_version" IS NOT new."client_version" OR old."provider" IS NOT new."provider" OR old."model" IS NOT new."model" OR old."identity_source" IS NOT new."identity_source" OR old."occurred_at" IS NOT new."occurred_at" OR old."received_at" IS NOT new."received_at" OR old."schema_version" IS NOT new."schema_version" OR old."effort_requested_raw" IS NOT new."effort_requested_raw" OR old."effort_requested_normalized" IS NOT new."effort_requested_normalized" OR old."effort_requested_source" IS NOT new."effort_requested_source" OR old."effort_requested_authority" IS NOT new."effort_requested_authority" OR old."effort_effective_raw" IS NOT new."effort_effective_raw" OR old."effort_effective_normalized" IS NOT new."effort_effective_normalized" OR old."effort_effective_source" IS NOT new."effort_effective_source" OR old."effort_effective_authority" IS NOT new."effort_effective_authority") BEGIN
    INSERT INTO history_sync_changes(project_id,table_name,row_key,operation) VALUES(old."project_id",'actor_invocations',json_array(old."id"),'update_old');
    INSERT INTO history_sync_changes(project_id,table_name,row_key,operation) VALUES(new."project_id",'actor_invocations',json_array(new."id"),'update_new');
END;
CREATE TRIGGER history_sync_history_index_blocks_insert AFTER INSERT ON "history_index_blocks" BEGIN
 INSERT INTO history_sync_changes(project_id,table_name,row_key,operation) VALUES(new.project_id,'history_index_blocks',json_array(new."project_id",new."block_hash"),'insert');
END;
CREATE TRIGGER history_sync_history_index_blocks_delete AFTER DELETE ON "history_index_blocks" BEGIN
 INSERT INTO history_sync_changes(project_id,table_name,row_key,operation) VALUES(old.project_id,'history_index_blocks',json_array(old."project_id",old."block_hash"),'delete');
END;
CREATE TRIGGER history_sync_history_index_blocks_update AFTER UPDATE ON "history_index_blocks" WHEN old."project_id" IS NOT new."project_id" OR old."block_hash" IS NOT new."block_hash" OR old."index_version" IS NOT new."index_version" BEGIN
 INSERT INTO history_sync_changes(project_id,table_name,row_key,operation) VALUES(old.project_id,'history_index_blocks',json_array(old."project_id",old."block_hash"),'update_old');
 INSERT INTO history_sync_changes(project_id,table_name,row_key,operation) VALUES(new.project_id,'history_index_blocks',json_array(new."project_id",new."block_hash"),'update_new');
END;
CREATE TRIGGER history_sync_history_index_envelopes_insert AFTER INSERT ON "history_index_envelopes" BEGIN
 INSERT INTO history_sync_changes(project_id,table_name,row_key,operation) VALUES(new.project_id,'history_index_envelopes',json_array(new."project_id",new."envelope_hash"),'insert');
END;
CREATE TRIGGER history_sync_history_index_envelopes_delete AFTER DELETE ON "history_index_envelopes" BEGIN
 INSERT INTO history_sync_changes(project_id,table_name,row_key,operation) VALUES(old.project_id,'history_index_envelopes',json_array(old."project_id",old."envelope_hash"),'delete');
END;
CREATE TRIGGER history_sync_history_index_envelopes_update AFTER UPDATE ON "history_index_envelopes" WHEN old."project_id" IS NOT new."project_id" OR old."envelope_hash" IS NOT new."envelope_hash" OR old."record_type" IS NOT new."record_type" OR old."record_id" IS NOT new."record_id" OR old."record_revision" IS NOT new."record_revision" OR old."envelope_json" IS NOT new."envelope_json" BEGIN
 INSERT INTO history_sync_changes(project_id,table_name,row_key,operation) VALUES(old.project_id,'history_index_envelopes',json_array(old."project_id",old."envelope_hash"),'update_old');
 INSERT INTO history_sync_changes(project_id,table_name,row_key,operation) VALUES(new.project_id,'history_index_envelopes',json_array(new."project_id",new."envelope_hash"),'update_new');
END;
CREATE TRIGGER history_sync_history_index_block_members_insert AFTER INSERT ON "history_index_block_members" BEGIN
 INSERT INTO history_sync_changes(project_id,table_name,row_key,operation) VALUES(new.project_id,'history_index_block_members',json_array(new."project_id",new."block_hash",new."envelope_hash"),'insert');
END;
CREATE TRIGGER history_sync_history_index_block_members_delete AFTER DELETE ON "history_index_block_members" BEGIN
 INSERT INTO history_sync_changes(project_id,table_name,row_key,operation) VALUES(old.project_id,'history_index_block_members',json_array(old."project_id",old."block_hash",old."envelope_hash"),'delete');
END;
CREATE TRIGGER history_sync_history_index_block_members_update AFTER UPDATE ON "history_index_block_members" WHEN old."project_id" IS NOT new."project_id" OR old."block_hash" IS NOT new."block_hash" OR old."envelope_hash" IS NOT new."envelope_hash" BEGIN
 INSERT INTO history_sync_changes(project_id,table_name,row_key,operation) VALUES(old.project_id,'history_index_block_members',json_array(old."project_id",old."block_hash",old."envelope_hash"),'update_old');
 INSERT INTO history_sync_changes(project_id,table_name,row_key,operation) VALUES(new.project_id,'history_index_block_members',json_array(new."project_id",new."block_hash",new."envelope_hash"),'update_new');
END;
CREATE TRIGGER history_sync_history_index_refs_insert AFTER INSERT ON "history_index_refs" BEGIN
 INSERT INTO history_sync_changes(project_id,table_name,row_key,operation) VALUES(new.project_id,'history_index_refs',json_array(new."project_id",new."envelope_hash",new."ref_kind",new."ref_id"),'insert');
END;
CREATE TRIGGER history_sync_history_index_refs_delete AFTER DELETE ON "history_index_refs" BEGIN
 INSERT INTO history_sync_changes(project_id,table_name,row_key,operation) VALUES(old.project_id,'history_index_refs',json_array(old."project_id",old."envelope_hash",old."ref_kind",old."ref_id"),'delete');
END;
CREATE TRIGGER history_sync_history_index_refs_update AFTER UPDATE ON "history_index_refs" WHEN old."project_id" IS NOT new."project_id" OR old."envelope_hash" IS NOT new."envelope_hash" OR old."ref_kind" IS NOT new."ref_kind" OR old."ref_id" IS NOT new."ref_id" BEGIN
 INSERT INTO history_sync_changes(project_id,table_name,row_key,operation) VALUES(old.project_id,'history_index_refs',json_array(old."project_id",old."envelope_hash",old."ref_kind",old."ref_id"),'update_old');
 INSERT INTO history_sync_changes(project_id,table_name,row_key,operation) VALUES(new.project_id,'history_index_refs',json_array(new."project_id",new."envelope_hash",new."ref_kind",new."ref_id"),'update_new');
END;
INSERT INTO history_sync_generation(project_id,token,cause) VALUES('',lower(hex(randomblob(16))),'normalized_history_index')
 ON CONFLICT(project_id) DO UPDATE SET token=excluded.token,cause=excluded.cause;
INSERT INTO writer_floor(singleton,mcp_api,raised_at) VALUES(1,136,strftime('%Y-%m-%dT%H:%M:%fZ','now'))
 ON CONFLICT(singleton) DO UPDATE SET mcp_api=max(writer_floor.mcp_api,excluded.mcp_api),raised_at=excluded.raised_at;
