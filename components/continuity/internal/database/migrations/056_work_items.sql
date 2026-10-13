-- Work items (6e4a) replace ideas, agent suggestions, and friction
-- dispositions. Idea and suggestion IDs are kept, so work_item:<id> resolves
-- every former idea:<id> and suggestion:<id>. Suggestions become ideas tagged
-- `suggestion` and with their former kind; their authority is kept. Friction
-- candidates stay derived: a work item linked derived_from friction:<id>
-- tracks one. Decision links are rehashed by this migration's Go step.

CREATE TABLE work_items (
    id TEXT NOT NULL,
    project_id TEXT NOT NULL REFERENCES projects(id),
    kind TEXT NOT NULL CHECK (kind IN ('idea', 'bug', 'task')),
    title TEXT NOT NULL,
    body TEXT NOT NULL,
    observed TEXT,
    reproduction TEXT,
    environment TEXT,
    severity TEXT CHECK (severity IS NULL OR severity IN ('low', 'medium', 'high', 'critical')),
    task_id TEXT,
    checkpoint_id TEXT,
    roadmap_node_id TEXT,
    creator TEXT NOT NULL,
    model TEXT,
    authority TEXT NOT NULL CHECK (authority IN ('caller_asserted', 'user_reported', 'model_suggested')),
    recorded_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version = 1),
    CHECK (kind = 'bug' OR (observed IS NULL AND reproduction IS NULL AND environment IS NULL AND severity IS NULL)),
    PRIMARY KEY (project_id, id)
) STRICT;

CREATE INDEX work_items_project_time ON work_items(project_id, recorded_at, id);
CREATE INDEX work_items_project_kind ON work_items(project_id, kind, recorded_at, id);
CREATE INDEX work_items_task_origin ON work_items(project_id, task_id, recorded_at, id) WHERE task_id IS NOT NULL;
CREATE INDEX work_items_checkpoint_origin ON work_items(project_id, checkpoint_id, recorded_at, id) WHERE checkpoint_id IS NOT NULL;
CREATE INDEX work_items_roadmap_origin ON work_items(project_id, roadmap_node_id, recorded_at, id) WHERE roadmap_node_id IS NOT NULL;

CREATE TABLE work_item_tags (
    project_id TEXT NOT NULL,
    item_id TEXT NOT NULL,
    tag TEXT NOT NULL,
    PRIMARY KEY (project_id, item_id, tag),
    FOREIGN KEY (project_id, item_id) REFERENCES work_items(project_id, id)
) STRICT;

CREATE INDEX work_item_tags_project_tag ON work_item_tags(project_id, tag, item_id);

CREATE TABLE work_item_dispositions (
    id TEXT NOT NULL,
    project_id TEXT NOT NULL,
    item_id TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('accepted', 'deferred', 'integrated', 'discarded', 'merged')),
    reason TEXT NOT NULL,
    target_refs_json TEXT NOT NULL,
    creator TEXT NOT NULL,
    model TEXT,
    authority TEXT NOT NULL CHECK (authority = 'caller_asserted'),
    recorded_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version = 1),
    PRIMARY KEY (project_id, id),
    FOREIGN KEY (project_id, item_id) REFERENCES work_items(project_id, id)
) STRICT;

CREATE INDEX work_item_dispositions_item_time
    ON work_item_dispositions(project_id, item_id, recorded_at, id);
CREATE UNIQUE INDEX work_item_dispositions_one_terminal
    ON work_item_dispositions(project_id, item_id) WHERE status IN ('integrated', 'discarded', 'merged');

CREATE TABLE work_item_links (
    id TEXT NOT NULL,
    project_id TEXT NOT NULL,
    item_id TEXT NOT NULL,
    relationship TEXT NOT NULL CHECK (relationship IN ('derived_from', 'follow_up_to', 'supports', 'depends_on', 'alternative_to', 'duplicates', 'belongs_to_track')),
    target_ref TEXT NOT NULL,
    creator TEXT NOT NULL,
    authority TEXT NOT NULL CHECK (authority = 'caller_asserted'),
    recorded_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version = 1),
    PRIMARY KEY (project_id, id),
    FOREIGN KEY (project_id, item_id) REFERENCES work_items(project_id, id)
) STRICT;

CREATE INDEX work_item_links_item ON work_item_links(project_id, item_id, recorded_at, id);
CREATE INDEX work_item_links_target ON work_item_links(project_id, target_ref, item_id);

-- Ideas.
INSERT INTO work_items(id, project_id, kind, title, body, task_id, checkpoint_id, creator, model, authority, recorded_at, schema_version)
SELECT id, project_id, 'idea', title, body, task_id, checkpoint_id, creator, model, authority, recorded_at, 1 FROM ideas;
INSERT INTO work_item_tags(project_id, item_id, tag) SELECT project_id, idea_id, tag FROM idea_tags;
INSERT INTO work_item_dispositions(id, project_id, item_id, status, reason, target_refs_json, creator, model, authority, recorded_at, schema_version)
SELECT id, project_id, idea_id, status, reason, target_refs_json, creator, model, authority, recorded_at, 1 FROM idea_dispositions;

-- Agent suggestions become ideas tagged with their origin and former kind.
INSERT INTO work_items(id, project_id, kind, title, body, task_id, checkpoint_id, roadmap_node_id, creator, model, authority, recorded_at, schema_version)
SELECT id, project_id, 'idea', summary, rationale, task_id, checkpoint_id, roadmap_node_id, creator, model, authority, recorded_at, 1 FROM agent_suggestions;
INSERT OR IGNORE INTO work_item_tags(project_id, item_id, tag) SELECT project_id, id, 'suggestion' FROM agent_suggestions;
INSERT OR IGNORE INTO work_item_tags(project_id, item_id, tag) SELECT project_id, id, replace(kind, '_', '-') FROM agent_suggestions;
INSERT INTO work_item_links(id, project_id, item_id, relationship, target_ref, creator, authority, recorded_at, schema_version)
SELECT id || '-idea', project_id, id, 'follow_up_to', 'work_item:' || idea_id, creator, 'caller_asserted', recorded_at, 1 FROM agent_suggestions WHERE idea_id IS NOT NULL;
INSERT INTO work_item_dispositions(id, project_id, item_id, status, reason, target_refs_json, creator, model, authority, recorded_at, schema_version)
SELECT id, project_id, suggestion_id,
       CASE status WHEN 'rejected' THEN 'discarded' WHEN 'superseded' THEN 'merged' ELSE status END,
       reason, target_refs_json, creator, model, authority, recorded_at, 1
FROM agent_suggestion_dispositions;
INSERT INTO work_item_links(id, project_id, item_id, relationship, target_ref, creator, authority, recorded_at, schema_version)
SELECT id, project_id, suggestion_id,
       CASE relationship WHEN 'suggested_while' THEN 'derived_from' WHEN 'supersedes' THEN 'duplicates' WHEN 'return_to_track' THEN 'belongs_to_track' ELSE relationship END,
       target_ref, creator, authority, recorded_at, 1
FROM agent_suggestion_links;

-- Plan provenance names work items.
CREATE TABLE track_plan_work_items (
    project_id TEXT NOT NULL,
    plan_id TEXT NOT NULL,
    plan_revision INTEGER NOT NULL,
    item_id TEXT NOT NULL,
    PRIMARY KEY (project_id, plan_id, plan_revision, item_id),
    FOREIGN KEY (project_id, plan_id, plan_revision) REFERENCES track_plan_revisions(project_id, id, revision),
    FOREIGN KEY (project_id, item_id) REFERENCES work_items(project_id, id)
) STRICT;
CREATE INDEX track_plan_work_items_target ON track_plan_work_items(project_id, item_id, plan_id, plan_revision);
INSERT INTO track_plan_work_items(project_id, plan_id, plan_revision, item_id)
SELECT project_id, plan_id, plan_revision, suggestion_id FROM track_plan_suggestions;
DROP TABLE track_plan_suggestions;
ALTER TABLE track_plan_items RENAME COLUMN suggestion_refs_json TO work_item_refs_json;

-- Decision links: idea and suggestion targets become work_item.
DROP TRIGGER decision_links_no_update;
DROP TRIGGER decision_links_no_delete;
CREATE TABLE decision_links_next (
    project_id TEXT NOT NULL,
    decision_id TEXT NOT NULL,
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    kind TEXT NOT NULL CHECK (kind IN ('subject', 'supports', 'opposes', 'resulted_in', 'supersedes')),
    target_kind TEXT NOT NULL CHECK (target_kind IN ('task', 'checkpoint', 'roadmap_node', 'track_plan', 'work_item', 'narrative', 'evidence', 'commit', 'decision')),
    target_id TEXT NOT NULL,
    target_revision TEXT NOT NULL,
    authority TEXT NOT NULL CHECK (authority = 'caller_asserted'),
    PRIMARY KEY (project_id, decision_id, ordinal),
    FOREIGN KEY (decision_id, project_id) REFERENCES decision_records(id, project_id)
) STRICT;
INSERT INTO decision_links_next
SELECT project_id, decision_id, ordinal, kind,
       CASE WHEN target_kind IN ('idea', 'suggestion') THEN 'work_item' ELSE target_kind END,
       target_id, target_revision, authority
FROM decision_links;
DROP TABLE decision_links;
ALTER TABLE decision_links_next RENAME TO decision_links;
CREATE INDEX decision_links_target ON decision_links(project_id, target_kind, target_id, target_revision, decision_id);
CREATE TRIGGER decision_links_no_update BEFORE UPDATE ON decision_links BEGIN SELECT RAISE(ABORT, 'decision links are append-only'); END;
CREATE TRIGGER decision_links_no_delete BEFORE DELETE ON decision_links BEGIN SELECT RAISE(ABORT, 'decision links are append-only'); END;

-- Topic links: idea targets become work_item.
CREATE TABLE topic_links_next (
    id TEXT NOT NULL,
    project_id TEXT NOT NULL REFERENCES projects(id),
    topic_id TEXT NOT NULL,
    target_kind TEXT NOT NULL CHECK (target_kind IN ('roadmap_node', 'narrative', 'narrative_claim', 'decision', 'work_item', 'card', 'track_plan', 'task', 'checkpoint', 'commit', 'file')),
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
INSERT INTO topic_links_next
SELECT id, project_id, topic_id, CASE target_kind WHEN 'idea' THEN 'work_item' ELSE target_kind END,
       target_id, target_revision, status, creator, model, authority, accepted_from, recorded_at
FROM topic_links ORDER BY recorded_at, id;
DROP TABLE topic_links;
ALTER TABLE topic_links_next RENAME TO topic_links;
CREATE INDEX topic_links_topic ON topic_links(project_id, topic_id, target_kind, target_id, recorded_at, id);
CREATE INDEX topic_links_target ON topic_links(project_id, target_kind, target_id, target_revision, recorded_at, id);

-- History search indexes work items.
DELETE FROM history_chunks WHERE family = 'idea';
CREATE TRIGGER work_items_index AFTER INSERT ON work_items
BEGIN
    INSERT INTO history_chunks(title, body, project_id, family, ref, kind, recorded_at)
    VALUES (NEW.title, NEW.body, NEW.project_id, 'work_item', 'work_item:' || NEW.id, NEW.kind, NEW.recorded_at);
END;
INSERT INTO history_chunks(title, body, project_id, family, ref, kind, recorded_at)
SELECT title, body, project_id, 'work_item', 'work_item:' || id, kind, recorded_at FROM work_items;

DROP TABLE agent_suggestion_links;
DROP TABLE agent_suggestion_dispositions;
DROP TABLE agent_suggestions;
DROP TABLE idea_dispositions;
DROP TABLE idea_tags;
DROP TABLE ideas;
DROP TABLE friction_dispositions;
