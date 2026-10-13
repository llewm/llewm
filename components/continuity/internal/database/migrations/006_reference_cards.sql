CREATE TABLE reference_cards (
    project_id TEXT NOT NULL,
    card_id TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK (revision > 0),
    title TEXT NOT NULL,
    body TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('proposed', 'accepted', 'superseded', 'retired')),
    profile TEXT NOT NULL CHECK (profile IN ('memory-jog', 'syntax', 'doc-section', 'general')),
    creator TEXT NOT NULL,
    model TEXT,
    authority TEXT NOT NULL,
    recorded_at TEXT NOT NULL,
    confirmed_at TEXT,
    schema_version INTEGER NOT NULL,
    PRIMARY KEY(project_id, card_id, revision),
    FOREIGN KEY(project_id) REFERENCES projects(id)
) STRICT;

CREATE TABLE reference_card_tags (
    project_id TEXT NOT NULL,
    card_id TEXT NOT NULL,
    card_revision INTEGER NOT NULL,
    tag TEXT NOT NULL,
    PRIMARY KEY(project_id, card_id, card_revision, tag),
    FOREIGN KEY(project_id, card_id, card_revision)
        REFERENCES reference_cards(project_id, card_id, revision)
) STRICT;

CREATE TABLE reference_card_sources (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    card_id TEXT NOT NULL,
    card_revision INTEGER NOT NULL,
    path TEXT NOT NULL,
    source_ref TEXT,
    sha256 TEXT NOT NULL,
    authority TEXT NOT NULL,
    FOREIGN KEY(project_id, card_id, card_revision)
        REFERENCES reference_cards(project_id, card_id, revision)
) STRICT;

CREATE TABLE reference_card_links (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    card_id TEXT NOT NULL,
    card_revision INTEGER NOT NULL,
    link_type TEXT NOT NULL CHECK (link_type IN ('supports', 'explains', 'depends_on', 'supersedes', 'applies_to')),
    target_kind TEXT NOT NULL CHECK (target_kind IN ('roadmap_node', 'file', 'decision', 'routine', 'procedure', 'card')),
    target_id TEXT NOT NULL,
    target_revision TEXT,
    status TEXT NOT NULL CHECK (status IN ('proposed', 'accepted')),
    creator TEXT NOT NULL,
    model TEXT,
    accepted_from TEXT,
    authority TEXT NOT NULL,
    recorded_at TEXT NOT NULL,
    FOREIGN KEY(project_id, card_id, card_revision)
        REFERENCES reference_cards(project_id, card_id, revision),
    FOREIGN KEY(accepted_from) REFERENCES reference_card_links(id)
) STRICT;

CREATE INDEX reference_cards_project_status
    ON reference_cards(project_id, status, card_id, revision DESC);
CREATE INDEX reference_card_links_source
    ON reference_card_links(project_id, card_id, card_revision, link_type, target_kind, target_id);
