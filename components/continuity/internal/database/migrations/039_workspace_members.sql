-- A unified workspace project records each manifest member as a repository with a
-- path relative to the workspace folder. The primary repository keeps tracking the
-- project root's location. Both tables are rebuilt because SQLite cannot alter a
-- CHECK constraint; locations are rebuilt first-referenced so the rename carries
-- the foreign key.
CREATE TABLE repositories_v2 (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    role TEXT NOT NULL CHECK (role IN ('primary', 'member')),
    member_path TEXT CHECK ((role = 'primary') = (member_path IS NULL)),
    created_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version = 1)
) STRICT;

CREATE TABLE repository_locations_v2 (
    id INTEGER PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    repository_id TEXT NOT NULL REFERENCES repositories_v2(id),
    path TEXT NOT NULL,
    root_commit TEXT,
    source TEXT NOT NULL CHECK (source IN ('backfill', 'observed', 'relocated', 'rebound')),
    observed_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version = 1)
) STRICT;

INSERT INTO repositories_v2(id, project_id, role, member_path, created_at, schema_version)
SELECT id, project_id, role, NULL, created_at, schema_version FROM repositories;

INSERT INTO repository_locations_v2(id, project_id, repository_id, path, root_commit, source, observed_at, schema_version)
SELECT id, project_id, repository_id, path, root_commit, source, observed_at, schema_version FROM repository_locations;

DROP TABLE repository_locations;
DROP TABLE repositories;
ALTER TABLE repositories_v2 RENAME TO repositories;
ALTER TABLE repository_locations_v2 RENAME TO repository_locations;

CREATE UNIQUE INDEX repositories_one_primary ON repositories(project_id) WHERE role = 'primary';
CREATE UNIQUE INDEX repositories_member_path ON repositories(project_id, member_path) WHERE role = 'member';
CREATE INDEX repository_locations_repository ON repository_locations(repository_id, id);
CREATE INDEX repository_locations_root_commit ON repository_locations(root_commit) WHERE root_commit IS NOT NULL;

CREATE TRIGGER repositories_no_update BEFORE UPDATE ON repositories BEGIN SELECT RAISE(ABORT,'repositories are append-only'); END;
CREATE TRIGGER repositories_no_delete BEFORE DELETE ON repositories BEGIN SELECT RAISE(ABORT,'repositories are append-only'); END;
CREATE TRIGGER repository_locations_no_update BEFORE UPDATE ON repository_locations BEGIN SELECT RAISE(ABORT,'repository locations are append-only'); END;
CREATE TRIGGER repository_locations_no_delete BEFORE DELETE ON repository_locations BEGIN SELECT RAISE(ABORT,'repository locations are append-only'); END;
