-- worktree_head_observations is the last HEAD a hook saw in each linked
-- worktree, keyed by the worktree's path. A linked worktree has its own HEAD
-- and HEAD reflog, so comparing it with the primary checkout's
-- head_observations row credited the wrong commits. head_observations keeps
-- the primary checkout's HEAD unchanged, so a binary from before this
-- migration still writes it. Device-local, like head_observations; the path
-- never leaves this machine.
CREATE TABLE worktree_head_observations (
    project_id TEXT NOT NULL REFERENCES projects(id),
    member TEXT NOT NULL,
    worktree TEXT NOT NULL CHECK (worktree <> ''),
    head TEXT NOT NULL,
    observed_at TEXT NOT NULL,
    PRIMARY KEY (project_id, member, worktree)
) STRICT;
