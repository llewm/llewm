-- Evidence records (commit narratives) and reference cards are cut (6e4b).
-- Every evidence record's commit already has a work narrative (imported from
-- the same Git note); this migration's Go pre-step refuses to run otherwise.
-- Their portable records stay in the memory repository's Git history.

DROP VIEW superseded_records;
CREATE VIEW superseded_records AS
SELECT project_id, 'decision' AS family, supersedes_id AS id, recorded_at AS at FROM decision_records WHERE supersedes_id IS NOT NULL
UNION ALL
SELECT project_id, 'narrative', supersedes_id, recorded_at FROM work_narratives WHERE supersedes_id IS NOT NULL
UNION ALL
SELECT project_id, family, id, recorded_at FROM fork_rejected_records;

DROP TABLE evidence_record_paths;
DROP TABLE evidence_records;
DROP TABLE reference_card_links;
DROP TABLE reference_card_sources;
DROP TABLE reference_card_tags;
DROP TABLE reference_cards;
DELETE FROM history_chunks WHERE family = 'card';

DROP TRIGGER decision_links_no_update;
DROP TRIGGER decision_links_no_delete;
CREATE TABLE decision_links_next (
    project_id TEXT NOT NULL,
    decision_id TEXT NOT NULL,
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    kind TEXT NOT NULL CHECK (kind IN ('subject', 'supports', 'opposes', 'resulted_in', 'supersedes')),
    target_kind TEXT NOT NULL CHECK (target_kind IN ('task', 'checkpoint', 'roadmap_node', 'track_plan', 'work_item', 'narrative', 'commit', 'decision')),
    target_id TEXT NOT NULL,
    target_revision TEXT NOT NULL,
    authority TEXT NOT NULL CHECK (authority = 'caller_asserted'),
    PRIMARY KEY (project_id, decision_id, ordinal),
    FOREIGN KEY (decision_id, project_id) REFERENCES decision_records(id, project_id)
) STRICT;
INSERT INTO decision_links_next SELECT * FROM decision_links;
DROP TABLE decision_links;
ALTER TABLE decision_links_next RENAME TO decision_links;
CREATE INDEX decision_links_target ON decision_links(project_id, target_kind, target_id, target_revision, decision_id);
CREATE TRIGGER decision_links_no_update BEFORE UPDATE ON decision_links BEGIN SELECT RAISE(ABORT, 'decision links are append-only'); END;
CREATE TRIGGER decision_links_no_delete BEFORE DELETE ON decision_links BEGIN SELECT RAISE(ABORT, 'decision links are append-only'); END;

CREATE TABLE topic_links_next (
    id TEXT NOT NULL,
    project_id TEXT NOT NULL REFERENCES projects(id),
    topic_id TEXT NOT NULL,
    target_kind TEXT NOT NULL CHECK (target_kind IN ('roadmap_node', 'narrative', 'narrative_claim', 'decision', 'work_item', 'track_plan', 'task', 'checkpoint', 'commit', 'file')),
    target_id TEXT NOT NULL,
    target_revision TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('proposed', 'accepted', 'retired')),
    creator TEXT NOT NULL,
    model TEXT,
    authority TEXT NOT NULL CHECK (authority IN ('caller_asserted', 'user_reported', 'model_suggested')),
    accepted_from TEXT,
    recorded_at TEXT NOT NULL,
    PRIMARY KEY (project_id, id),
    FOREIGN KEY (project_id, accepted_from) REFERENCES topic_links_next(project_id, id)
) STRICT;
INSERT INTO topic_links_next SELECT * FROM topic_links ORDER BY recorded_at, id;
DROP TABLE topic_links;
ALTER TABLE topic_links_next RENAME TO topic_links;
CREATE INDEX topic_links_topic ON topic_links(project_id, topic_id, target_kind, target_id, recorded_at, id);
CREATE INDEX topic_links_target ON topic_links(project_id, target_kind, target_id, target_revision, recorded_at, id);
