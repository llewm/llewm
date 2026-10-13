CREATE TABLE decision_records (
 id TEXT NOT NULL,
 project_id TEXT NOT NULL REFERENCES projects(id),
 record_sha256 TEXT NOT NULL,
 title TEXT NOT NULL,
 rationale TEXT NOT NULL,
 disposition TEXT NOT NULL CHECK(disposition IN ('accepted','deferred','rejected','superseded')),
 actor TEXT NOT NULL,
 actor_type TEXT NOT NULL CHECK(actor_type = 'human'),
 authority TEXT NOT NULL CHECK(authority IN ('caller_asserted','user_reported')),
 review_source TEXT NOT NULL,
 supersedes_id TEXT,
 recorded_at TEXT NOT NULL,
 schema_version INTEGER NOT NULL CHECK(schema_version = 1),
 PRIMARY KEY(project_id,id),
 UNIQUE(project_id,supersedes_id),
 FOREIGN KEY(supersedes_id,project_id) REFERENCES decision_records(id,project_id)
) STRICT;
CREATE INDEX decision_project_time ON decision_records(project_id,recorded_at DESC,id DESC);
CREATE TABLE decision_links (
 project_id TEXT NOT NULL,
 decision_id TEXT NOT NULL,
 ordinal INTEGER NOT NULL CHECK(ordinal >= 0),
 kind TEXT NOT NULL CHECK(kind IN ('subject','supports','opposes','resulted_in','supersedes')),
 target_kind TEXT NOT NULL CHECK(target_kind IN ('task','checkpoint','roadmap_node','track_plan','suggestion','idea','narrative','evidence','commit','decision')),
 target_id TEXT NOT NULL,
 target_revision TEXT NOT NULL,
 authority TEXT NOT NULL CHECK(authority = 'caller_asserted'),
 PRIMARY KEY(project_id,decision_id,ordinal),
 FOREIGN KEY(decision_id,project_id) REFERENCES decision_records(id,project_id)
) STRICT;
CREATE INDEX decision_links_target ON decision_links(project_id,target_kind,target_id,target_revision,decision_id);
CREATE TRIGGER decision_records_no_update BEFORE UPDATE ON decision_records BEGIN SELECT RAISE(ABORT,'decision history is append-only'); END;
CREATE TRIGGER decision_records_no_delete BEFORE DELETE ON decision_records BEGIN SELECT RAISE(ABORT,'decision history is append-only'); END;
CREATE TRIGGER decision_links_no_update BEFORE UPDATE ON decision_links BEGIN SELECT RAISE(ABORT,'decision links are append-only'); END;
CREATE TRIGGER decision_links_no_delete BEFORE DELETE ON decision_links BEGIN SELECT RAISE(ABORT,'decision links are append-only'); END;
