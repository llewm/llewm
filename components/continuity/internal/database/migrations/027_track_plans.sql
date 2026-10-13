CREATE TABLE agent_suggestions (
    id TEXT NOT NULL,
    project_id TEXT NOT NULL REFERENCES projects(id),
    kind TEXT NOT NULL CHECK(kind IN ('next_action', 'follow_up', 'risk', 'investigation', 'improvement', 'decision_option', 'roadmap_candidate')),
    summary TEXT NOT NULL,
    rationale TEXT NOT NULL,
    creator TEXT NOT NULL,
    actor_type TEXT NOT NULL CHECK(actor_type IN ('agent', 'human', 'system', 'unknown')),
    client TEXT,
    model TEXT,
    invocation_id TEXT,
    session_id TEXT,
    task_id TEXT,
    checkpoint_id TEXT,
    roadmap_node_id TEXT,
    roadmap_node_revision INTEGER,
    idea_id TEXT,
    authority TEXT NOT NULL CHECK(authority IN ('model_suggested', 'user_reported', 'caller_asserted')),
    semantic_sha256 TEXT NOT NULL,
    recorded_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL,
    PRIMARY KEY(project_id, id),
    FOREIGN KEY(invocation_id, project_id) REFERENCES actor_invocations(id, project_id),
    FOREIGN KEY(project_id, checkpoint_id) REFERENCES checkpoints(project_id, id),
    FOREIGN KEY(project_id, idea_id) REFERENCES ideas(project_id, id)
) STRICT;

CREATE INDEX agent_suggestions_origin
    ON agent_suggestions(project_id, roadmap_node_id, task_id, recorded_at, id);

CREATE TABLE agent_suggestion_dispositions (
    id TEXT NOT NULL,
    project_id TEXT NOT NULL,
    suggestion_id TEXT NOT NULL,
    status TEXT NOT NULL CHECK(status IN ('accepted', 'deferred', 'integrated', 'rejected', 'merged', 'superseded')),
    reason TEXT NOT NULL,
    target_refs_json TEXT NOT NULL,
    creator TEXT NOT NULL,
    model TEXT,
    authority TEXT NOT NULL CHECK(authority = 'caller_asserted'),
    recorded_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL,
    PRIMARY KEY(project_id, id),
    FOREIGN KEY(project_id, suggestion_id) REFERENCES agent_suggestions(project_id, id)
) STRICT;

CREATE INDEX agent_suggestion_dispositions_current
    ON agent_suggestion_dispositions(project_id, suggestion_id, recorded_at DESC, id DESC);

CREATE TABLE agent_suggestion_links (
    id TEXT NOT NULL,
    project_id TEXT NOT NULL,
    suggestion_id TEXT NOT NULL,
    relationship TEXT NOT NULL CHECK(relationship IN ('follow_up_to', 'suggested_while', 'supports', 'depends_on', 'alternative_to', 'duplicates', 'supersedes', 'belongs_to_track', 'return_to_track')),
    target_ref TEXT NOT NULL,
    creator TEXT NOT NULL,
    authority TEXT NOT NULL CHECK(authority = 'caller_asserted'),
    recorded_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL,
    PRIMARY KEY(project_id, id),
    FOREIGN KEY(project_id, suggestion_id) REFERENCES agent_suggestions(project_id, id)
) STRICT;

CREATE INDEX agent_suggestion_links_source
    ON agent_suggestion_links(project_id, suggestion_id, recorded_at, id);

CREATE TABLE track_plan_revisions (
    project_id TEXT NOT NULL REFERENCES projects(id),
    id TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK(revision > 0),
    roadmap_node_id TEXT NOT NULL,
    task_id TEXT,
    title TEXT NOT NULL,
    summary TEXT NOT NULL,
    status TEXT NOT NULL CHECK(status IN ('proposed', 'accepted', 'active', 'completed', 'superseded')),
    creator TEXT NOT NULL,
    model TEXT,
    authority TEXT NOT NULL CHECK(authority IN ('model_suggested', 'caller_asserted')),
    recorded_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL,
    PRIMARY KEY(project_id, id, revision)
) STRICT;

CREATE INDEX track_plan_revisions_current
    ON track_plan_revisions(project_id, id, revision DESC);

CREATE INDEX track_plan_revisions_roadmap
    ON track_plan_revisions(project_id, roadmap_node_id, revision DESC);

CREATE TABLE track_plan_items (
    project_id TEXT NOT NULL,
    plan_id TEXT NOT NULL,
    plan_revision INTEGER NOT NULL,
    id TEXT NOT NULL,
    position INTEGER NOT NULL CHECK(position >= 0),
    title TEXT NOT NULL,
    detail TEXT NOT NULL,
    status TEXT NOT NULL CHECK(status IN ('pending', 'ready', 'active', 'blocked', 'completed', 'superseded')),
    parent_id TEXT,
    evidence_refs_json TEXT NOT NULL,
    suggestion_refs_json TEXT NOT NULL,
    PRIMARY KEY(project_id, plan_id, plan_revision, id),
    UNIQUE(project_id, plan_id, plan_revision, position),
    FOREIGN KEY(project_id, plan_id, plan_revision)
        REFERENCES track_plan_revisions(project_id, id, revision)
) STRICT;

CREATE TABLE track_plan_item_dependencies (
    project_id TEXT NOT NULL,
    plan_id TEXT NOT NULL,
    plan_revision INTEGER NOT NULL,
    item_id TEXT NOT NULL,
    depends_on_item_id TEXT NOT NULL,
    PRIMARY KEY(project_id, plan_id, plan_revision, item_id, depends_on_item_id),
    FOREIGN KEY(project_id, plan_id, plan_revision, item_id)
        REFERENCES track_plan_items(project_id, plan_id, plan_revision, id),
    FOREIGN KEY(project_id, plan_id, plan_revision, depends_on_item_id)
        REFERENCES track_plan_items(project_id, plan_id, plan_revision, id)
) STRICT;

CREATE TABLE track_plan_suggestions (
    project_id TEXT NOT NULL,
    plan_id TEXT NOT NULL,
    plan_revision INTEGER NOT NULL,
    suggestion_id TEXT NOT NULL,
    PRIMARY KEY(project_id, plan_id, plan_revision, suggestion_id),
    FOREIGN KEY(project_id, plan_id, plan_revision)
        REFERENCES track_plan_revisions(project_id, id, revision),
    FOREIGN KEY(project_id, suggestion_id)
        REFERENCES agent_suggestions(project_id, id)
) STRICT;
