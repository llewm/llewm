-- Durable transport observations, not a disposable block/semantic index.
-- Existing archives are deliberately NOT activated or copied by SQL migration.
CREATE TABLE history_archive_state (
 project_id TEXT PRIMARY KEY REFERENCES projects(id),
 lineage_id TEXT NOT NULL,
 format_version INTEGER NOT NULL CHECK(format_version=1),
 status TEXT NOT NULL CHECK(status IN ('staging','ready')),
 fresh_identity INTEGER NOT NULL DEFAULT 0 CHECK(fresh_identity IN (0,1)),
 epoch TEXT NOT NULL CHECK(length(epoch)=32 AND epoch NOT GLOB '*[^0-9a-f]*'),
 last_sequence INTEGER NOT NULL DEFAULT 0 CHECK(last_sequence>=0),
 record_count INTEGER NOT NULL DEFAULT 0 CHECK(record_count BETWEEN 0 AND 100000),
 record_bytes INTEGER NOT NULL DEFAULT 0 CHECK(record_bytes BETWEEN 0 AND 67108864),
 origin_count INTEGER NOT NULL DEFAULT 0 CHECK(origin_count BETWEEN 0 AND 500000),
 change_count INTEGER NOT NULL DEFAULT 0 CHECK(change_count=record_count+origin_count),
 migration_json TEXT CHECK(migration_json IS NULL OR (length(migration_json)<=4096 AND json_valid(migration_json))),
 migration_checksum TEXT CHECK(migration_checksum IS NULL OR (length(migration_checksum)=64 AND migration_checksum NOT GLOB '*[^0-9a-f]*')),
 CHECK((migration_json IS NULL)=(migration_checksum IS NULL))
) STRICT;
CREATE TABLE history_archive_envelopes (
 project_id TEXT NOT NULL REFERENCES history_archive_state(project_id),
 envelope_hash TEXT NOT NULL CHECK(length(envelope_hash)=64 AND envelope_hash NOT GLOB '*[^0-9a-f]*'),
 record_type TEXT NOT NULL,record_id TEXT NOT NULL,record_revision INTEGER NOT NULL CHECK(record_revision>=0),
 initial_origin_hash TEXT NOT NULL CHECK(length(initial_origin_hash)=64 AND initial_origin_hash NOT GLOB '*[^0-9a-f]*'),
 envelope_json BLOB NOT NULL CHECK(length(envelope_json) BETWEEN 1 AND 212992),
 PRIMARY KEY(project_id,envelope_hash),
 FOREIGN KEY(project_id,envelope_hash,initial_origin_hash) REFERENCES history_archive_origins(project_id,envelope_hash,origin_hash) DEFERRABLE INITIALLY DEFERRED
) STRICT;
CREATE INDEX history_archive_identity ON history_archive_envelopes(project_id,record_type,record_id,record_revision);
CREATE TABLE history_archive_origins (
 project_id TEXT NOT NULL,envelope_hash TEXT NOT NULL,
 origin_hash TEXT NOT NULL CHECK(length(origin_hash)=64 AND origin_hash NOT GLOB '*[^0-9a-f]*'),
 PRIMARY KEY(project_id,envelope_hash,origin_hash),
 FOREIGN KEY(project_id,envelope_hash) REFERENCES history_archive_envelopes(project_id,envelope_hash)
) STRICT;
CREATE INDEX history_archive_origin_members ON history_archive_origins(project_id,origin_hash,envelope_hash);
CREATE TABLE history_archive_changes (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT,
 project_id TEXT NOT NULL,kind TEXT NOT NULL CHECK(kind IN ('envelope','origin')),
 envelope_hash TEXT NOT NULL,origin_hash TEXT,
 CHECK((kind='envelope' AND origin_hash IS NULL) OR (kind='origin' AND origin_hash IS NOT NULL)),
 FOREIGN KEY(project_id,envelope_hash) REFERENCES history_archive_envelopes(project_id,envelope_hash),
 FOREIGN KEY(project_id,envelope_hash,origin_hash) REFERENCES history_archive_origins(project_id,envelope_hash,origin_hash)
) STRICT;
CREATE INDEX history_archive_project_changes ON history_archive_changes(project_id,sequence);
CREATE UNIQUE INDEX history_archive_envelope_change ON history_archive_changes(project_id,envelope_hash) WHERE kind='envelope';
CREATE UNIQUE INDEX history_archive_origin_change ON history_archive_changes(project_id,envelope_hash,origin_hash) WHERE kind='origin';
CREATE TABLE history_archive_cursors (
 project_id TEXT NOT NULL REFERENCES history_archive_state(project_id),
 consumer TEXT NOT NULL CHECK(length(consumer)=64 AND consumer NOT GLOB '*[^0-9a-f]*'),
 snapshot_json TEXT NOT NULL CHECK(length(snapshot_json)<=2048 AND json_valid(snapshot_json)),
 checksum TEXT NOT NULL CHECK(length(checksum)=64 AND checksum NOT GLOB '*[^0-9a-f]*'),
 PRIMARY KEY(project_id,consumer)
) STRICT;
-- Disposable endpoint bookkeeping; not a source of archived observations.
CREATE TABLE history_archive_status (
 project_id TEXT NOT NULL REFERENCES history_archive_state(project_id),
 consumer TEXT NOT NULL CHECK(length(consumer)=64 AND consumer NOT GLOB '*[^0-9a-f]*'),
 value_json TEXT NOT NULL CHECK(length(value_json)<=65536 AND json_valid(value_json)),
 checksum TEXT NOT NULL CHECK(length(checksum)=64 AND checksum NOT GLOB '*[^0-9a-f]*'),
 PRIMARY KEY(project_id,consumer)
) STRICT;
-- Only identities first created under this schema carry proof that their
-- archive starts empty. Existing identities are deliberately not backfilled.
CREATE TRIGGER history_archive_new_identity AFTER INSERT ON project_memory_identities BEGIN
 INSERT INTO history_archive_state(project_id,lineage_id,format_version,status,fresh_identity,epoch)
 VALUES(new.project_id,new.lineage_id,1,'staging',1,lower(hex(randomblob(16)))) ON CONFLICT DO NOTHING;
END;
CREATE TRIGGER history_archive_envelope_insert AFTER INSERT ON history_archive_envelopes BEGIN
 INSERT INTO history_archive_changes(project_id,kind,envelope_hash) VALUES(new.project_id,'envelope',new.envelope_hash);
END;
CREATE TRIGGER history_archive_origin_insert AFTER INSERT ON history_archive_origins BEGIN
 INSERT INTO history_archive_changes(project_id,kind,envelope_hash,origin_hash) VALUES(new.project_id,'origin',new.envelope_hash,new.origin_hash);
END;
CREATE TRIGGER history_archive_change_insert AFTER INSERT ON history_archive_changes BEGIN
 UPDATE history_archive_state SET last_sequence=max(last_sequence,new.sequence),
 record_count=record_count+(new.kind='envelope'),
 record_bytes=record_bytes+CASE WHEN new.kind='envelope' THEN (SELECT length(envelope_json)+1 FROM history_archive_envelopes WHERE project_id=new.project_id AND envelope_hash=new.envelope_hash) ELSE 0 END,
 origin_count=origin_count+(new.kind='origin'),change_count=change_count+1 WHERE project_id=new.project_id;
END;
-- Counter maintenance is legitimate only for the exact newly appended event.
-- Other tracking edits invalidate epoch-bound capture proofs without scanning
-- history on every append. Both lookups below use immutable primary keys.
CREATE TRIGGER history_archive_tracking_changed AFTER UPDATE OF last_sequence,record_count,record_bytes,origin_count,change_count ON history_archive_state
 WHEN (new.last_sequence IS NOT old.last_sequence OR new.record_count IS NOT old.record_count
 OR new.record_bytes IS NOT old.record_bytes OR new.origin_count IS NOT old.origin_count OR new.change_count IS NOT old.change_count)
 AND NOT(new.last_sequence>old.last_sequence AND new.change_count=old.change_count+1 AND EXISTS(
 SELECT 1 FROM history_archive_changes c WHERE c.project_id=new.project_id AND c.sequence=new.last_sequence AND (
 (c.kind='origin' AND new.record_count=old.record_count AND new.record_bytes=old.record_bytes AND new.origin_count=old.origin_count+1)
 OR (c.kind='envelope' AND new.record_count=old.record_count+1 AND new.origin_count=old.origin_count
 AND new.record_bytes=old.record_bytes+(SELECT length(e.envelope_json)+1 FROM history_archive_envelopes e WHERE e.project_id=c.project_id AND e.envelope_hash=c.envelope_hash)))))
 BEGIN UPDATE history_archive_state SET epoch=lower(hex(randomblob(16))) WHERE project_id=new.project_id; END;
CREATE TRIGGER history_archive_envelope_immutable_update BEFORE UPDATE ON history_archive_envelopes BEGIN SELECT RAISE(ABORT,'archived envelopes are immutable'); END;
CREATE TRIGGER history_archive_envelope_immutable_delete BEFORE DELETE ON history_archive_envelopes BEGIN SELECT RAISE(ABORT,'archived envelopes are immutable'); END;
CREATE TRIGGER history_archive_origin_immutable_update BEFORE UPDATE ON history_archive_origins BEGIN SELECT RAISE(ABORT,'archive provenance is append-only'); END;
CREATE TRIGGER history_archive_origin_immutable_delete BEFORE DELETE ON history_archive_origins BEGIN SELECT RAISE(ABORT,'archive provenance is append-only'); END;
CREATE TRIGGER history_archive_change_immutable_update BEFORE UPDATE ON history_archive_changes BEGIN SELECT RAISE(ABORT,'archive journal is append-only'); END;
CREATE TRIGGER history_archive_change_immutable_delete BEFORE DELETE ON history_archive_changes BEGIN SELECT RAISE(ABORT,'archive journal is append-only'); END;
-- If an immutability guard is removed for repair/diagnosis, destructive changes
-- still invalidate every consumer. Full audit, not a guessed delta, is required.
CREATE TRIGGER history_archive_envelope_changed AFTER UPDATE ON history_archive_envelopes BEGIN UPDATE history_archive_state SET epoch=lower(hex(randomblob(16))) WHERE project_id IN (old.project_id,new.project_id); END;
CREATE TRIGGER history_archive_envelope_removed AFTER DELETE ON history_archive_envelopes BEGIN UPDATE history_archive_state SET epoch=lower(hex(randomblob(16))) WHERE project_id=old.project_id; END;
CREATE TRIGGER history_archive_origin_changed AFTER UPDATE ON history_archive_origins BEGIN UPDATE history_archive_state SET epoch=lower(hex(randomblob(16))) WHERE project_id IN (old.project_id,new.project_id); END;
CREATE TRIGGER history_archive_origin_removed AFTER DELETE ON history_archive_origins BEGIN UPDATE history_archive_state SET epoch=lower(hex(randomblob(16))) WHERE project_id=old.project_id; END;
CREATE TRIGGER history_archive_journal_changed AFTER UPDATE ON history_archive_changes BEGIN UPDATE history_archive_state SET epoch=lower(hex(randomblob(16))) WHERE project_id IN (old.project_id,new.project_id); END;
CREATE TRIGGER history_archive_journal_removed AFTER DELETE ON history_archive_changes BEGIN UPDATE history_archive_state SET epoch=lower(hex(randomblob(16))) WHERE project_id=old.project_id; END;
CREATE TRIGGER history_archive_identity_immutable BEFORE UPDATE ON history_archive_state
 WHEN old.project_id IS NOT new.project_id OR old.lineage_id IS NOT new.lineage_id OR old.format_version IS NOT new.format_version
 OR old.fresh_identity IS NOT new.fresh_identity
 OR (old.status IS NOT new.status AND NOT(old.status='staging' AND new.status='ready'))
 OR (old.migration_json IS NOT NULL AND (old.migration_json IS NOT new.migration_json OR old.migration_checksum IS NOT new.migration_checksum))
 BEGIN SELECT RAISE(ABORT,'archive identity and activation are immutable'); END;
CREATE TRIGGER history_archive_state_immutable_delete BEFORE DELETE ON history_archive_state BEGIN SELECT RAISE(ABORT,'durable archive state cannot be deleted'); END;

-- Older CLI writers still own a loose-file archive and must be drained before
-- activation. Use a new writer epoch; compatible MCP surface alone is not proof
-- that a writer understands the durable storage ownership contract.
INSERT INTO writer_floor(singleton,mcp_api,raised_at) VALUES(1,137,strftime('%Y-%m-%dT%H:%M:%fZ','now'))
ON CONFLICT(singleton) DO UPDATE SET mcp_api=excluded.mcp_api,raised_at=excluded.raised_at WHERE excluded.mcp_api>writer_floor.mcp_api;
