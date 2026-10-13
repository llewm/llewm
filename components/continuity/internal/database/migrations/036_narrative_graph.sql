-- Searchable narrative graph: claim chunks, topics, and a derived history index.
-- Canonical records are unchanged. Claim rows and index rows are derived by
-- insert triggers so every writer, including portable import, maintains them.

CREATE TABLE work_narrative_claims (
    project_id TEXT NOT NULL,
    narrative_id TEXT NOT NULL,
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    kind TEXT NOT NULL CHECK (kind IN ('intent', 'outcome', 'constraint', 'alternative', 'verification', 'decision', 'next_action', 'limitation', 'note')),
    label TEXT NOT NULL,
    text TEXT NOT NULL,
    authority TEXT NOT NULL,
    PRIMARY KEY(project_id, narrative_id, ordinal),
    FOREIGN KEY(project_id, narrative_id) REFERENCES work_narratives(project_id, id)
) STRICT;

CREATE INDEX work_narrative_claims_kind
    ON work_narrative_claims(project_id, kind, narrative_id, ordinal);

CREATE TABLE topic_revisions (
    project_id TEXT NOT NULL REFERENCES projects(id),
    id TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK (revision > 0),
    title TEXT NOT NULL,
    summary TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('proposed', 'accepted', 'retired')),
    creator TEXT NOT NULL,
    model TEXT,
    authority TEXT NOT NULL CHECK (authority IN ('caller_asserted', 'user_reported', 'model_suggested')),
    recorded_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version = 1),
    PRIMARY KEY(project_id, id, revision)
) STRICT;

CREATE TABLE topic_aliases (
    project_id TEXT NOT NULL REFERENCES projects(id),
    alias TEXT NOT NULL,
    topic_id TEXT NOT NULL,
    creator TEXT NOT NULL,
    recorded_at TEXT NOT NULL,
    PRIMARY KEY(project_id, alias)
) STRICT;

CREATE TABLE topic_links (
    id TEXT NOT NULL,
    project_id TEXT NOT NULL REFERENCES projects(id),
    topic_id TEXT NOT NULL,
    target_kind TEXT NOT NULL CHECK (target_kind IN ('roadmap_node', 'narrative', 'narrative_claim', 'decision', 'idea', 'card', 'track_plan', 'task', 'checkpoint', 'commit', 'file')),
    target_id TEXT NOT NULL,
    target_revision TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('proposed', 'accepted', 'retired')),
    creator TEXT NOT NULL,
    model TEXT,
    authority TEXT NOT NULL CHECK (authority IN ('caller_asserted', 'user_reported', 'model_suggested')),
    accepted_from TEXT,
    recorded_at TEXT NOT NULL,
    PRIMARY KEY(project_id, id),
    FOREIGN KEY(project_id, accepted_from) REFERENCES topic_links(project_id, id)
) STRICT;

CREATE INDEX topic_links_topic
    ON topic_links(project_id, topic_id, target_kind, target_id, recorded_at, id);
CREATE INDEX topic_links_target
    ON topic_links(project_id, target_kind, target_id, target_revision, recorded_at, id);

-- Derived full-text index. Unindexed columns carry exact references for drilldown.
CREATE VIRTUAL TABLE history_chunks USING fts5(
    title,
    body,
    project_id UNINDEXED,
    family UNINDEXED,
    ref UNINDEXED,
    kind UNINDEXED,
    recorded_at UNINDEXED,
    tokenize = 'porter unicode61'
);

CREATE TRIGGER work_narratives_claims_insert AFTER INSERT ON work_narratives
BEGIN
    INSERT INTO work_narrative_claims(project_id, narrative_id, ordinal, kind, label, text, authority)
    SELECT NEW.project_id, NEW.id, CAST(c.key AS INTEGER), json_extract(c.value, '$.kind'),
        COALESCE(json_extract(c.value, '$.label'), ''), json_extract(c.value, '$.text'), json_extract(c.value, '$.authority')
    FROM json_each(NEW.payload_json, '$.claims') c;
END;

CREATE TRIGGER work_narrative_claims_index AFTER INSERT ON work_narrative_claims
BEGIN
    INSERT INTO history_chunks(title, body, project_id, family, ref, kind, recorded_at)
    SELECT n.title, CASE WHEN NEW.label = '' THEN NEW.text ELSE NEW.label || ': ' || NEW.text END,
        NEW.project_id, 'narrative_claim', 'narrative:' || NEW.narrative_id || '#' || NEW.ordinal, NEW.kind, n.recorded_at
    FROM work_narratives n WHERE n.project_id = NEW.project_id AND n.id = NEW.narrative_id;
END;

CREATE TRIGGER decision_records_index AFTER INSERT ON decision_records
BEGIN
    INSERT INTO history_chunks(title, body, project_id, family, ref, kind, recorded_at)
    VALUES (NEW.title, NEW.rationale, NEW.project_id, 'decision', 'decision:' || NEW.id, NEW.disposition, NEW.recorded_at);
END;

CREATE TRIGGER ideas_index AFTER INSERT ON ideas
BEGIN
    INSERT INTO history_chunks(title, body, project_id, family, ref, kind, recorded_at)
    VALUES (NEW.title, NEW.body, NEW.project_id, 'idea', 'idea:' || NEW.id, 'idea', NEW.recorded_at);
END;

CREATE TRIGGER checkpoints_index AFTER INSERT ON checkpoints
BEGIN
    INSERT INTO history_chunks(title, body, project_id, family, ref, kind, recorded_at)
    VALUES (NEW.task_title, NEW.summary || char(10) || 'Next: ' || NEW.next_action, NEW.project_id, 'checkpoint', 'checkpoint:' || NEW.id, NEW.status, NEW.created_at);
END;

CREATE TRIGGER reference_cards_index AFTER INSERT ON reference_cards
BEGIN
    INSERT INTO history_chunks(title, body, project_id, family, ref, kind, recorded_at)
    VALUES (NEW.title, NEW.body, NEW.project_id, 'card', 'card:' || NEW.card_id || '@' || NEW.revision, NEW.status, NEW.recorded_at);
END;

-- Backfill existing records. The claim insert fires the claim index trigger.
INSERT INTO work_narrative_claims(project_id, narrative_id, ordinal, kind, label, text, authority)
SELECT n.project_id, n.id, CAST(c.key AS INTEGER), json_extract(c.value, '$.kind'),
    COALESCE(json_extract(c.value, '$.label'), ''), json_extract(c.value, '$.text'), json_extract(c.value, '$.authority')
FROM work_narratives n, json_each(n.payload_json, '$.claims') c;

INSERT INTO history_chunks(title, body, project_id, family, ref, kind, recorded_at)
SELECT title, rationale, project_id, 'decision', 'decision:' || id, disposition, recorded_at FROM decision_records;
INSERT INTO history_chunks(title, body, project_id, family, ref, kind, recorded_at)
SELECT title, body, project_id, 'idea', 'idea:' || id, 'idea', recorded_at FROM ideas;
INSERT INTO history_chunks(title, body, project_id, family, ref, kind, recorded_at)
SELECT task_title, summary || char(10) || 'Next: ' || next_action, project_id, 'checkpoint', 'checkpoint:' || id, status, created_at FROM checkpoints;
INSERT INTO history_chunks(title, body, project_id, family, ref, kind, recorded_at)
SELECT title, body, project_id, 'card', 'card:' || card_id || '@' || revision, status, recorded_at FROM reference_cards;
