-- commit_author_revocations supersedes commit_authors observations that later
-- revalidation found unsupported by commit-creating reflog evidence, such as
-- pre-fix attributions of pulled or fast-forwarded history. The observation
-- rows stay untouched; readers skip revoked ones. Device-local, like
-- commit_authors.
CREATE TABLE commit_author_revocations (
    project_id TEXT NOT NULL REFERENCES projects(id),
    member TEXT NOT NULL,
    commit_sha TEXT NOT NULL,
    session_id TEXT NOT NULL,
    reason TEXT NOT NULL,
    revoked_at TEXT NOT NULL,
    PRIMARY KEY (project_id, member, commit_sha, session_id)
) STRICT;
