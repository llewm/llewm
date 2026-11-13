-- Builds before commit 922c22f attributed pulled history, or commits another
-- session made earlier, to whichever session next ran Git. Mark every existing
-- project so the next hook revalidates the attributions observed before this
-- migration against reflog evidence, once, and appends revocations for the
-- unsupported ones. Rows observed afterwards come from fixed builds and are
-- never revalidated, so later reflog expiry cannot revoke them. A database
-- created after this migration has no rows to repair and gets no marker.
CREATE TABLE commit_author_revalidations_pending (
    project_id TEXT PRIMARY KEY REFERENCES projects(id),
    cutoff TEXT NOT NULL
) STRICT;
INSERT INTO commit_author_revalidations_pending(project_id,cutoff)
SELECT id, strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM projects;
