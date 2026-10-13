CREATE TABLE work_narratives (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    title TEXT NOT NULL,
    trigger_kind TEXT NOT NULL CHECK (trigger_kind IN ('meaningful_checkpoint', 'accepted_decision', 'completed_roadmap_slice', 'commit', 'manual')),
    occurred_from TEXT,
    occurred_to TEXT,
    creator TEXT NOT NULL,
    creator_invocation_id TEXT REFERENCES actor_invocations(id),
    semantic_sha256 TEXT NOT NULL,
    supersedes_id TEXT REFERENCES work_narratives(id),
    recorded_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version = 1),
    payload_json TEXT NOT NULL,
    UNIQUE(project_id, id)
) STRICT;

CREATE TABLE work_narrative_anchors (
    project_id TEXT NOT NULL,
    narrative_id TEXT NOT NULL,
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    anchor_kind TEXT NOT NULL CHECK (anchor_kind IN ('checkpoint', 'task', 'roadmap_node', 'commit', 'file', 'region', 'invocation', 'verification', 'portable_evidence')),
    target TEXT NOT NULL,
    target_revision TEXT,
    content_sha256 TEXT,
    region_locator TEXT,
    authority TEXT NOT NULL CHECK (authority IN ('machine_observed', 'caller_asserted', 'user_reported')),
    PRIMARY KEY(project_id, narrative_id, ordinal),
    FOREIGN KEY(project_id, narrative_id) REFERENCES work_narratives(project_id, id)
) STRICT;

CREATE INDEX work_narratives_project_time
    ON work_narratives(project_id, recorded_at DESC, id DESC);
CREATE INDEX work_narratives_project_trigger
    ON work_narratives(project_id, trigger_kind, recorded_at DESC, id DESC);
CREATE INDEX work_narrative_anchors_target
    ON work_narrative_anchors(project_id, anchor_kind, target, narrative_id);
CREATE UNIQUE INDEX work_narratives_one_successor
    ON work_narratives(supersedes_id)
    WHERE supersedes_id IS NOT NULL;

CREATE TRIGGER work_narratives_creator_invocation_project_insert
BEFORE INSERT ON work_narratives
WHEN NEW.creator_invocation_id IS NOT NULL
 AND NOT EXISTS (
    SELECT 1 FROM actor_invocations
    WHERE id = NEW.creator_invocation_id AND project_id = NEW.project_id
 )
BEGIN
    SELECT RAISE(ABORT, 'creator invocation must belong to narrative project');
END;
