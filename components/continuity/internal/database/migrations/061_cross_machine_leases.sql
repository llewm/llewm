-- Cross-machine leases (5c of continuity-multi-repo-sync). A project that opts
-- in ([leases] remote) keeps each scope's grants as a commit chain on a ref in
-- the memory repository and writes them with a compare-and-swap push. The
-- local lease tables become this machine's mirror: grants from other machines
-- are copied in, and lease_remote_events names the remote commit of every
-- grant event that is on the remote. A grant event this machine wrote without
-- such a row is local-only (the remote was unreachable) until a sync publishes
-- it.
CREATE TABLE lease_remote_events (
    grant_id TEXT NOT NULL REFERENCES lease_grants(id),
    sequence INTEGER NOT NULL CHECK (sequence >= 1),
    version TEXT NOT NULL CHECK (version <> ''),
    observed_at TEXT NOT NULL,
    PRIMARY KEY (grant_id, sequence)
) STRICT;

-- Stamps travel as lease_stamp records so other machines can flag writes made
-- under a superseded token; each names the instance that wrote it. Stamps
-- written before this migration were written here.
ALTER TABLE lease_stamps ADD COLUMN instance_id TEXT;
DROP TRIGGER lease_stamps_no_update;
UPDATE lease_stamps SET instance_id=(SELECT instance_id FROM project_memory_identities i WHERE i.project_id=lease_stamps.project_id);
CREATE TRIGGER lease_stamps_no_update BEFORE UPDATE ON lease_stamps BEGIN SELECT RAISE(ABORT,'lease stamps are append-only'); END;

-- A stale write is a checkpoint or plan revision written under a token after a
-- newer grant of its scope existed. Resolving one records whether the write
-- stands (accepted 'write') or yields to the newer lease ('lease'); both keep
-- the record in history. Append-only; syncs as stale_write_resolution.
CREATE TABLE stale_write_resolutions (
    project_id TEXT NOT NULL REFERENCES projects(id),
    id TEXT NOT NULL,
    record_kind TEXT NOT NULL CHECK (record_kind IN ('checkpoint', 'track_plan_revision')),
    record_id TEXT NOT NULL CHECK (record_id <> ''),
    accepted TEXT NOT NULL CHECK (accepted IN ('write', 'lease')),
    resolver TEXT NOT NULL CHECK (resolver <> ''),
    authority TEXT NOT NULL CHECK (authority IN ('user_reported', 'caller_asserted')),
    recorded_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version = 1),
    PRIMARY KEY (project_id, id)
) STRICT;

CREATE INDEX stale_write_resolutions_record ON stale_write_resolutions(project_id, record_kind, record_id);
CREATE TRIGGER stale_write_resolutions_no_update BEFORE UPDATE ON stale_write_resolutions BEGIN SELECT RAISE(ABORT,'stale write resolutions are append-only'); END;
CREATE TRIGGER stale_write_resolutions_no_delete BEFORE DELETE ON stale_write_resolutions BEGIN SELECT RAISE(ABORT,'stale write resolutions are append-only'); END;
