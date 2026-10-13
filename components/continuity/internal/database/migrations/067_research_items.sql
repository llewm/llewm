-- Research work items (8j of continuity-auditing). A research item starts from
-- a question (its title) and closes as answered with links to its findings.
-- CHECK constraints cannot change in place, so work_items and the tables that
-- reference it are rebuilt: new tables first (children pointing at the new
-- parent), then the old children, then the old parent, then renames, which
-- rewrite the references.
CREATE TABLE work_items_next (
    id TEXT NOT NULL,
    project_id TEXT NOT NULL REFERENCES projects(id),
    kind TEXT NOT NULL CHECK (kind IN ('idea', 'bug', 'task', 'research')),
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
INSERT INTO work_items_next SELECT * FROM work_items;

CREATE TABLE work_item_tags_next (
    project_id TEXT NOT NULL,
    item_id TEXT NOT NULL,
    tag TEXT NOT NULL,
    PRIMARY KEY (project_id, item_id, tag),
    FOREIGN KEY (project_id, item_id) REFERENCES work_items_next(project_id, id)
) STRICT;
INSERT INTO work_item_tags_next SELECT * FROM work_item_tags;

CREATE TABLE work_item_dispositions_next (
    id TEXT NOT NULL,
    project_id TEXT NOT NULL,
    item_id TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('accepted', 'deferred', 'integrated', 'discarded', 'merged', 'answered')),
    reason TEXT NOT NULL,
    target_refs_json TEXT NOT NULL,
    creator TEXT NOT NULL,
    model TEXT,
    authority TEXT NOT NULL CHECK (authority = 'caller_asserted'),
    recorded_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version = 1),
    PRIMARY KEY (project_id, id),
    FOREIGN KEY (project_id, item_id) REFERENCES work_items_next(project_id, id)
) STRICT;
INSERT INTO work_item_dispositions_next SELECT * FROM work_item_dispositions;

CREATE TABLE work_item_links_next (
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
    FOREIGN KEY (project_id, item_id) REFERENCES work_items_next(project_id, id)
) STRICT;
INSERT INTO work_item_links_next SELECT * FROM work_item_links;

CREATE TABLE track_plan_work_items_next (
    project_id TEXT NOT NULL,
    plan_id TEXT NOT NULL,
    plan_revision INTEGER NOT NULL,
    item_id TEXT NOT NULL,
    PRIMARY KEY (project_id, plan_id, plan_revision, item_id),
    FOREIGN KEY (project_id, plan_id, plan_revision) REFERENCES track_plan_revisions(project_id, id, revision),
    FOREIGN KEY (project_id, item_id) REFERENCES work_items_next(project_id, id)
) STRICT;
INSERT INTO track_plan_work_items_next SELECT * FROM track_plan_work_items;

DROP TABLE work_item_tags;
DROP TABLE work_item_dispositions;
DROP TABLE work_item_links;
DROP TABLE track_plan_work_items;
DROP TABLE work_items;
ALTER TABLE work_items_next RENAME TO work_items;
ALTER TABLE work_item_tags_next RENAME TO work_item_tags;
ALTER TABLE work_item_dispositions_next RENAME TO work_item_dispositions;
ALTER TABLE work_item_links_next RENAME TO work_item_links;
ALTER TABLE track_plan_work_items_next RENAME TO track_plan_work_items;

CREATE INDEX work_items_project_time ON work_items(project_id, recorded_at, id);
CREATE INDEX work_items_project_kind ON work_items(project_id, kind, recorded_at, id);
CREATE INDEX work_items_task_origin ON work_items(project_id, task_id, recorded_at, id) WHERE task_id IS NOT NULL;
CREATE INDEX work_items_checkpoint_origin ON work_items(project_id, checkpoint_id, recorded_at, id) WHERE checkpoint_id IS NOT NULL;
CREATE INDEX work_items_roadmap_origin ON work_items(project_id, roadmap_node_id, recorded_at, id) WHERE roadmap_node_id IS NOT NULL;
CREATE INDEX work_item_tags_project_tag ON work_item_tags(project_id, tag, item_id);
CREATE INDEX work_item_dispositions_item_time ON work_item_dispositions(project_id, item_id, recorded_at, id);
CREATE UNIQUE INDEX work_item_dispositions_one_terminal
    ON work_item_dispositions(project_id, item_id) WHERE status IN ('integrated', 'discarded', 'merged', 'answered');
CREATE INDEX work_item_links_item ON work_item_links(project_id, item_id, recorded_at, id);
CREATE INDEX work_item_links_target ON work_item_links(project_id, target_ref, item_id);
CREATE INDEX track_plan_work_items_target ON track_plan_work_items(project_id, item_id, plan_id, plan_revision);

CREATE TRIGGER work_items_index AFTER INSERT ON work_items
BEGIN
    INSERT INTO history_chunks(title, body, project_id, family, ref, kind, recorded_at)
    VALUES (NEW.title, NEW.body, NEW.project_id, 'work_item', 'work_item:' || NEW.id, NEW.kind, NEW.recorded_at);
END;
