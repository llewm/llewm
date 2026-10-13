-- Project IDs stop being derived from the root path. Each project gets one primary
-- repository, and repository locations become append-only observations so a moved
-- checkout can be recognized instead of silently becoming a new empty project.
-- projects.root remains the current location and is no longer an identity.
CREATE TABLE repositories (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    role TEXT NOT NULL CHECK (role = 'primary'),
    created_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version = 1)
) STRICT;

CREATE UNIQUE INDEX repositories_one_primary ON repositories(project_id) WHERE role = 'primary';

CREATE TABLE repository_locations (
    id INTEGER PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    repository_id TEXT NOT NULL REFERENCES repositories(id),
    path TEXT NOT NULL,
    root_commit TEXT,
    source TEXT NOT NULL CHECK (source IN ('backfill', 'observed', 'relocated', 'rebound')),
    observed_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version = 1)
) STRICT;

CREATE INDEX repository_locations_repository ON repository_locations(repository_id, id);
CREATE INDEX repository_locations_root_commit ON repository_locations(root_commit) WHERE root_commit IS NOT NULL;

CREATE TRIGGER repositories_no_update BEFORE UPDATE ON repositories BEGIN SELECT RAISE(ABORT,'repositories are append-only'); END;
CREATE TRIGGER repositories_no_delete BEFORE DELETE ON repositories BEGIN SELECT RAISE(ABORT,'repositories are append-only'); END;
CREATE TRIGGER repository_locations_no_update BEFORE UPDATE ON repository_locations BEGIN SELECT RAISE(ABORT,'repository locations are append-only'); END;
CREATE TRIGGER repository_locations_no_delete BEFORE DELETE ON repository_locations BEGIN SELECT RAISE(ABORT,'repository locations are append-only'); END;

-- The root commit cannot be computed in SQL; the first open at a location observes it.
INSERT INTO repositories(id, project_id, role, created_at, schema_version)
SELECT 'repo-' || lower(hex(randomblob(8))), id, 'primary', created_at, 1 FROM projects;

INSERT INTO repository_locations(project_id, repository_id, path, root_commit, source, observed_at, schema_version)
SELECT p.id, r.id, p.root, NULL, 'backfill', p.created_at, 1
FROM projects p JOIN repositories r ON r.project_id = p.id AND r.role = 'primary';
