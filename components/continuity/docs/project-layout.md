# Project state layout and migration

The default project footprint is:

```text
.llewm/
  project.toml         # layout_version = 1
  workspace.toml       # optional explicit multi-repository membership
  continuity/
    config.toml
    continuity.db
    adoption.json      # only when a trial was explicitly started
    backups/
```

Project IDs, lineage and observations remain in the existing Continuity database;
the root manifest describes layout, not a second identity authority. Future
components can use sibling directories when implemented.

## Keep local state out of Git

Exclude the whole project-root `.llewm/` directory **before** `llewm init`, a
usage baseline, or a trial. It contains machine-local configuration, identities,
observations, import bundles, receipts and backups. Ignoring only `*.db` or
`.llewm/continuity/` leaves other local state exposed. Portable knowledge sharing
uses the supported export or History-sync workflow.

For a project adopting Llewm, add this line to the root `.gitignore` and commit
that change with the project's normal workflow:

```gitignore
/.llewm/
```

This shares protection with other checkouts, including Windows, macOS and Linux.
For personal use in a shared repository where a tracked ignore change is
unwanted, add the same line to the repository's local exclude file instead.
Locate it with `git rev-parse --git-path info/exclude`; do not assume `.git` is a
directory, especially in linked worktrees. Local exclusions do not travel to
other clones, so each installation must verify them. Existing effective global
exclusions are also accepted; no duplicate rule is needed.

From the target Git root, verify both exclusion and tracked state:

```sh
git check-ignore --no-index -q -- .llewm/
git ls-files -- .llewm
```

The first command must exit successfully; the second must print no paths. These
commands work in PowerShell as well as POSIX shells. A successful ignore check
alone does not mean files are untracked. Preserve unrelated rules and existing
line endings when editing an ignore file; inspect the resulting diff.

If state is already tracked, keep the local data and review the affected paths
with the engineer before changing the index. An ignore rule does not remove
tracked files or copies in prior commits. Removing paths from the index and
handling any published sensitive data are separate reviewed actions; onboarding
must not delete state, untrack it automatically, or rewrite Git history.

`llewm init` warns about missing exclusions, tracked state or an unavailable Git
check without editing ignore files. `llewm doctor` reports the same warnings;
its JSON report includes `git_protection.ignored`, `tracked_files` and any check
error. These findings are advisory for existing installations. `trial start`
requires a successful whole-directory exclusion check and no tracked state
before creating trial files. Agent onboarding must establish and verify this
protection before creating state for **all three starting paths**, including
usage auditing, and report unresolved tracked-state findings separately.

`CONTINUITY_HOME` continues to name the component state directory itself, not
its `.llewm` parent. Global guidance, registry paths and managed memory clones
are unchanged. `CONTINUITY_*` environment variables and MCP names identify the
Continuity component and are not aliases for old executable names.

A linked worktree/submodule uses its owner's store unless it has a local project
footprint. Legacy state still establishes that scope so it can fail closed rather
than silently borrowing a different store. Unified/federated workspace manifests
now live at `.llewm/workspace.toml`.

## Preview a legacy move

Use the exact owning project/workspace root, not a component or member directory:

```sh
llewm --project-root /path/to/project project migrate-layout
```

Preview is read-only. Normal reads/init refuse an existing `.continuity/` store;
there is no fallback, auto-move, symlink alias or empty replacement initialization.
Do not simply rename the folder: explicit config paths, workspace membership,
active writers and machine registry routes also matter.

The legacy schema must already match the candidate. If needed, first use the
matching previous binary's separate verified-backup schema migration. Layout
migration refuses `CONTINUITY_HOME`, outside database/backup paths, memory clones
inside the old directory, symlinks/special files, and destination collisions.
It preserves unknown TOML keys but re-encodes config formatting; original config
bytes are retained. Explicit external storage needs a separately reviewed move.

## Apply with all writers drained

Preserve a confirmed checkpoint and exact installed binary/plugin/config receipts.
Pause sync and stop/drain old CLI, hook, MCP, detached jobs and peer writers through
the supported client lifecycle. The tool cannot discover/prove an idle process is
stopped. Never kill the agent's own MCP server before saving a supported handoff.
Restart coordination can require a person and another session.

Choose a **new backup directory on the same home-disk filesystem**, outside both
state directories. Do not use `/tmp`, sync it through a consumer file-sync service,
or put it inside the managed History-sync memory repository.

```sh
llewm --project-root /path/to/project project migrate-layout --apply \
  --writers-stopped --backup /home/USER/llewm-backups/project-layout-TIMESTAMP
```

The operation drains WAL (refusing busy SQLite readers), creates and integrity
checks a consistent `snapshot.db`, stages the component tree, rewrites only the
known database/backup paths and absolute backup pins inside legacy state, checks staged integrity and foreign keys, and raises
the staged writer floor. Existing SQLite rows, IDs, revisions and histories are
preserved. The original directory is retained as `BACKUP/source`; its bytes and
writer floor are retained for rollback. Original empty directories need not be
recreated in staging; no file data is discarded. The receipt inventories original
and staged file hashes. A cross-filesystem source retirement fails and restores
source rather than falling back to copying and deleting it.

`.llewm/project.toml` is created and any workspace manifest moves to the common
root. Unrelated sibling component state is preserved. A `layout.lock` blocks
normal opens while the multi-file move is incomplete. Ordinary failures restore
the source; interrupted moves keep the receipt and recovery paths.

Verify version/hash, `database status`, `doctor`, project/lineage identity and the
saved checkpoint before restarting writers or sync. The machine registry route
is refreshed by the next supported writable project open; the migration does not
edit the registry or a managed memory clone by hand. Start compatible clients and
verify actual MCP identity and checkpoint recovery, then restore prior sync only
when every active writer is compatible. Changing local paths does not change
portable record format or authorize a memory conversion/publication.

## Restore or recover an interrupted move

Before new work is written, or after an interruption:

```sh
llewm --project-root /path/to/project project restore-layout \
  --writers-stopped --backup /home/USER/llewm-backups/project-layout-TIMESTAMP
```

Restore validates the receipt/project paths and all current/original files. It
refuses a nonempty WAL, changed files, modified layout metadata or new knowledge.
It then restores the original layout and removes only verified generated layout
files/staging. Backups and the receipt remain. Use the prior compatible executable
and plugin pair afterward. Neither restoration nor a directory rename alone
reactivates an already running old client safely.

If the new store changed, keep **both stores** and reconcile the new work through
supported record export/import and review. Blind rollback would discard it and is
therefore refused. If `layout.lock` remains after a crash, inspect its exact backup
path and receipt, stop writers and use restore; do not delete the lock and run init.
A failure before a receipt exists leaves the original source in place; verify that
source and any retained snapshot before removing the abandoned operation lock.

This is a pre-release local migration, not an OS security boundary or native
platform qualification. Live machines still require the reviewed installation
procedure and grant. Public source/binary packaging must be updated to the exact
checked commit separately.

## Project relocation

Project IDs are opaque and permanent; a root path is the project's current
location. Worktrees use the main checkout's store and submodules use their
superproject's unless they have their own `.llewm/` directory. Moving a checkout
with its store records a relocation. Resolve an ambiguous match explicitly:

```sh
llewm project show
llewm project rebind PROJECT_ID      # the same project, moved
llewm project rebind --new           # an independent project
```
