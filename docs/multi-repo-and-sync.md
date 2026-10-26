<!-- continuity:roadmap-node continuity-multi-repo-sync -->
# Multi-repository projects, sync tiers, and coordination

Status: accepted direction, recorded 2026-09-27 after an engineer design
discussion. This document sets identity, topology, sync, and coordination
boundaries. It authorizes design and roadmap planning, not implementation of any
tier. Each execution capability named here (Git actions, network sync, leases)
still requires its own reviewed slice before it ships.

## Why

Continuity currently equates a project with one Git working tree: the project ID is
looked up by root path, `.llewm/continuity/` state lives at that root, and services such
as evidence, narratives, roadmap bindings, contracts, and track plans hold a single
`Root`. That breaks for common real-world setups:

- one product split across several intertwined repositories that are opened and
  developed together (for example a collector daemon, an API service, and remote
  agents in sibling repositories under one folder);
- one engineer using several machines;
- teams using several machines, usually in disjoint areas, occasionally
  overlapping; and
- one engineer running several agents in parallel worktrees.

A root path is also too fragile to be an identity: moving or recloning a checkout
must not create a new project.

Part of Llewm's value is keeping agent bookkeeping out of the code repository.
Project memory should not become a growing pile of dated Markdown files beside the
source.

## Revisions to accepted direction

This document deliberately revises parts of the
[federated project-memory contract](federated-memory.md) and
[architecture](architecture.md):

- **Authority.** In the Local tier the project-local database stays
  authoritative. In the History-sync tier, the shared immutable record stream is
  canonical for curated records, and each device's SQLite database becomes a
  rebuildable local index plus the authority for device-private data (raw events,
  sessions, usage, hook telemetry).
- **Git actions.** Continuity may manage its own memory repository, including
  commit, pull and push, when History sync is explicitly configured.
  It still never writes to or runs Git actions against the code repository unless
  a separate reviewed feature says so.
- **Networking.** Sync with a configured memory remote or a coordination service
  is permitted in the tiers that enable it. Networking stays off in the default
  Local tier.

Unchanged: lineage association stays explicit, records stay immutable and
content-hashed, import never silently replaces conflicting state, redaction happens
before export, and the live SQLite/WAL/SHM files are never a transport or
collaboration format.

## Identity

Three identities replace the root path:

| Identity | Meaning | Status |
|---|---|---|
| Project (lineage) | One body of shared memory: roadmap, decisions, narratives, plans | Exists (portable-memory lineage) |
| Repository | One Git history, stable across clones and moves | New: a stable repo ID, bound explicitly; root-commit hash and remote URL are matching hints only |
| Location | Where a repository or store currently lives on one machine | Becomes a re-resolvable last-seen value, never a key |

Local instance identity (one store on one machine) is unchanged. A machine-level
registry maps repository fingerprints to bound repo IDs and projects, so sessions
started inside any member repository resolve to the right project without path
guessing.

Every existing single-repository project gets exactly one repository record, so
current behavior is preserved. File bindings become `(repo_id, path)` and Git
evidence becomes `(repo_id, commit)`.

## Repository topology

No manifest means an **implicit single-repository project**. This is the default
and covers both ordinary repositories and monorepos, since a monorepo is one Git
history. Component scoping inside a monorepo is optional metadata on roadmap nodes
and bindings, not a separate mode.

An explicit project manifest is required for more than one repository, because
Continuity must not infer shared lineage from sibling directories, names, or remote
URLs. The manifest declares one of two modes:

- **unified:** one lineage and one shared store across several member
  repositories. A work narrative may carry commit anchors in several repositories.
  No member repository hosts the shared store, so none becomes privileged.
- **federated:** each repository keeps its own project and store; independently
  created members normally have different lineages, while members produced by an
  explicit split retain the source lineage so they can exchange the same portable
  history. The manifest declares a group so the metadata-only catalog can route
  between them and cross-project references resolve.

Implemented machine routing is a rebuildable metadata-only SQLite registry in the
Continuity global configuration directory. Opening a federated member records its
repository root-commit fingerprint, repository and project IDs, optional lineage,
current checkout and store paths, and manifest group/member. `llewm workspace
show` reads that registry from the group root and names members not yet registered
on the machine. Routes are filtered to current manifest members, a route whose
checkout no longer exists is flagged and refused by `workspace route`, and group
roots are stored symlink-resolved. It does not copy project records between member stores.

Changing mode later (splitting a unified project, merging federated ones) is an
explicit, reviewed operation built on portable export and import with lineage
checks. `llewm workspace change --mode MODE` is read-only: it identifies the
participating stores and exact portable record-set hashes and returns a confirmation
digest. `--apply --confirm DIGEST` rechecks that plan, validates and imports curated
records append-only, and changes the manifest only after every import succeeds. A
split creates or updates one same-lineage project store per member. A merge requires
all member stores and any existing workspace target to share one lineage; different
lineages and portable record collisions are refused rather than conflated. The old
stores remain available for recovery. Device-private raw events, sessions, and
usage samples are intentionally not copied by the curated profile. It is never
automatic.

When the working directory is not inside a Git repository but contains several, and
no manifest exists, Continuity reports the repositories it found and offers to
propose a manifest for confirmation. It does not guess.

Editor workspace files (such as VS Code `.code-workspace`) are not project
authority. `llewm workspace editor` previews one from the manifest and
`--write` creates it without replacing an existing file.

### Workspace setup

In the folder holding the repositories, preview the manifest, then explicitly save
it and initialize the shared store:

```sh
llewm workspace init
llewm workspace init --write
llewm init
```

For independent stores, use `llewm workspace init --mode federated --write`, then
run `llewm init` inside each member. `llewm workspace show` lists registered and
missing routes; `llewm workspace route MEMBER` selects a member by name,
repo/project/lineage ID, or fingerprint. An unlisted repository stays independent.

Preview a topology change with `llewm workspace change --mode federated` (or
`--mode unified`), then repeat it with `--apply --confirm SHA256` using the returned
digest. Changed store contents or identities require a fresh preview.

`llewm workspace editor` previews a VS Code workspace; `--write` saves it, and
`--path dev.code-workspace` selects its destination. This does not change project
authority.

Workspace paths are relative to the workspace root. `--commit` resolves in the
member containing the working directory; at the workspace root, select it with
`--repo MEMBER` (MCP uses `repo`). Narrative commit anchors retain the member in
`region_locator`, and roadmap sync watermarks retain each member's head.

## Sync tiers

Independent projects can also use a machine-local
[portfolio catalog](../components/continuity/docs/portfolio-history.md) for manual
routing to a private aggregate and separate group Git repositories. Each keeps its
own lineage; this does not filter members or topics out of a unified project.

Sync is independent of repository topology. Any topology can use any tier.

| Tier | Typical users | Enabled by | Moving parts |
|---|---|---|---|
| **Local** (default) | Everyone | Nothing | Stable identity; local leases between agents on one machine |
| **History sync** | Solo multi-machine; most teams | One config entry naming a memory remote | Managed memory repository, outbox, pull/push at hook points, import-time fork detection, sync status |
| **Coordination** | Teams with overlapping work; heavy multi-machine parallel agents | Explicit per-project opt-in | Cross-machine leases with fencing tokens |

Most projects should never need the Coordination tier. Teams that work in disjoint
areas or coordinate by other means only need eventual history consistency.

### History sync

Accepted design (decision `history-sync-design`, 2026-09-27):

- **Memory repository.** A dedicated Git repository, separate from code, that can
  serve many projects. Each immutable envelope is stored once:

  ```text
  v3/<lineage>/records/<record-type>/<identity-sha256>/<envelope-sha256>.json
  ```

  Writers only ever add files, and there is no shared index or manifest, so
  ordinary record-file merges avoid line conflicts. Two versions of one record identity land in
  different files; Continuity's import keeps them as a revision fork (or reports
  a collision for an unrevisioned record) instead of Git.
- **No SQLite in Git.** Git and Git LFS cannot merge a binary database, and LFS
  locking would serialize every writer. Exact SQLite snapshots remain a separate
  disaster-recovery path.
- **Configuration.** Each project names its remote in `.llewm/continuity/config.toml`
  (`[sync] remote = "..."`; untracked, so each machine opts in).
  `llewm sync init --remote URL` records it and clones once per remote under
  `~/.local/share/llewm/memory/`, shared by every project using that remote.
- **Cycle.** Under a file lock: export envelopes not yet in the repository (the
  path is the content hash, so no watermark is needed), commit
  `continuity: <instance> +N records`, `pull --rebase`, import what arrived through
  the existing plan/apply path, then push. Git runs non-interactively
  (`GIT_TERMINAL_PROMPT=0`) with repository hooks disabled; authentication follows the
  memory repository's
  [rules of engagement](../components/continuity/docs/memory-repository.md#authentication):
  the user's existing Git credentials first, then an SSH key or credential helper
  scoped to the memory clone, and plaintext storage only as a
  confirmed last resort. Continuity never changes global Git configuration and never
  stores a secret itself. Wallet-backed askpass can retrieve saved credentials during
  explicit setup, but automatic sync disables it because it can also prompt.
  HTTPS setup therefore verifies access through a helper with askpass disabled.
- **Offline tolerance.** When the remote is unreachable, export and commit still
  happen locally and the push is retried later. An append-only sync-attempt record
  feeds status and bootstrap (last pull, unpushed records, remote reachability).
- **Triggers.** Automatic from the start: pull and import at session start with a
  bounded timeout (the session starts anyway and reports it), background push after
  checkpoints so hooks never wait on the network, and manual `llewm sync`.
- **Commit policy.** Continuity's automated memory-repository commits are exempt
  from the engineer's Git-note commit policy; they are self-describing machine
  records, not engineering work.
- **Coverage.** Decisions and topics become portable before sync ships, so synced
  history includes the decision ledger.
- **Device-private data stays local.** Raw events, sessions, usage records, and
  hook telemetry are not synced. The curated export profile defines what travels.

### Shared-folder and hybrid history

An optional [shared-folder transport](../components/continuity/docs/shared-folder-history.md)
uses immutable curated records in an existing provider-replicated directory. A
machine may configure one Git memory remote and several folder endpoints.
Explicit relay permission per destination allows a bridge to forward original
records, unresolved versions and supported opaque observations between transports.
Private local archives retain verified observations before semantic import.
Folder delivery is external and has no implied peer receipt or lease fencing;
the live SQLite database remains device-local.

### Temporary exchange before History-sync setup

An optional [temporary LAN exchange](../components/continuity/docs/lan-history.md)
can move curated bundles before a memory remote is configured. Explicit pairing
enables only the temporary connection; receiving does not associate a lineage,
import records, or enable automatic sync. Fresh installs explicitly join the
inspected source lineage and verify checkpoint recovery before creating new plans.

### Fork detection is baseline, not coordination

Concurrent heads occur without a second developer: one engineer who switches
machines before the first one pushed can advance the same task or plan twice. Import
therefore detects two revisions that claim the same parent, and treats the same ID
with a different hash as a collision (existing behavior). It surfaces both as
explicit resolution items and orders by parent links with instance ID as a
tiebreak, never by wall-clock time alone.

Resolution must stay light: keep both heads, propose the newer one, and let one
action accept it by recording a resolution record. Forks must not become a new
source of noise.

**Checkpoint forks.** A task's heads are its checkpoints (with a recorded context)
that no other checkpoint continues and no resolution rejected; more than one head
is a fork. Heads are ordered newest first (checkpoint ID breaks ties) and the newest
is proposed, never chosen automatically. The bootstrap describes the current task's
other heads and counts other forked tasks. `llewm fork resolve task:ID`
(optionally `--accept ID`) writes a continuation checkpoint from the accepted head
and a `fork_resolution` record naming the rejected heads; both sync, so the
continuation becomes the tip on every machine and every build, and all heads stay in
history. Continuing a rejected head later (on a machine that had not synced yet)
reopens the fork.

**Revision forks.** Two machines can write the same revision of a revisioned
record (a track plan, topic, procedure, resource, contract, roadmap node or
routine) before syncing. Import keeps the project's own version (a machine with
neither imports the proposed one, so later revisions have a predecessor), stores
every version whole as a device-local fork head, and no longer holds the import
watermark. Heads are ordered newest first with the envelope hash as tiebreak.
`llewm fork resolve TYPE:ID@REVISION` (optionally `--accept HASH`) appends
the accepted version as the record's next revision and a resolution naming the
rejected heads, in one transaction; every machine converges on that revision
whichever version it held. When the accepted head is the local one and later
local revisions already continue it, only the resolution is written. Resolutions
travel as `revision_fork_resolution`, not `fork_resolution`, so a build that knows
only checkpoint forks defers them instead of refusing its whole sync. Two
versions of an unrevisioned record remain a collision.

**Supersession forks.** Decisions, commit narratives, and work narratives are not
revised; a correction is a new record that supersedes the old one. Two machines
can each supersede the same record before syncing. Both successors are stored on
every machine (later records may reference either), so one record has two
successors: a fork named `decision:ID`, `evidence:ID`, or `narrative:ID` after the
superseded record. Its heads are the tips of its successor chains that no
resolution rejected, newest first with the ID as tiebreak; a fork nested inside an
open one is reported only as part of it. Until resolved, every head reads as
current. `llewm fork resolve FAMILY:ID` (optionally `--accept ID`) writes only
a resolution naming the rejected heads; each machine then reads them as superseded
by the accepted head, and continuing a rejected head on a machine that had not
synced reopens the fork. Locally a record that is no longer current still cannot be
superseded. Resolutions travel as `supersession_fork_resolution`.

**Memory ahead of code.** A synced checkpoint can describe code this clone does not
have yet. Each checkpoint therefore records, machine-observed, the HEAD commit of
every workspace member and the checkpoint of the same task it continued (its
explicit predecessor, else the task's tip), as a separate `checkpoint_context`
record so older builds defer it instead of refusing the checkpoint. The bootstrap
and `llewm work` compare the current checkpoint's commits with the local
checkouts: a commit missing from the clone reads "Memory is ahead of code ... Pull
first"; a commit the clone has but HEAD does not contain reads "Code is behind or
off the checkpoint". The session-start roadmap scan reports
`freshness=watermark_unavailable` instead of disappearing when its last review
commit is not in the clone.

### Code branches

Accepted direction (decision `branch-aware-selection`, 2026-09-30). Tasks, not
branches, are the unit of memory. Code branches are short-lived, renamed, and
squashed, while memory must outlive them, so the memory repository stays on one
branch and no record is keyed by a branch name.

Today the bootstrap resumes the project-wide newest checkpoint, so an agent in one
worktree or branch can resume another branch's task, locally or after a sync, and
the code-off-checkpoint warning then fires for the wrong reason. Two slices address
this:

- **Branch-aware session selection (5a2).** The checkpoint context also records
  each member's branch (or detached HEAD) beside its commit, machine-observed.
  Session task selection takes, in order: an explicit selection; the task with
  live presence from this session; the task last checkpointed by this session; the
  newest unfinished task whose latest checkpoint was on this checkout's current
  branch; then the project-wide newest unfinished checkpoint. Completed tasks,
  closed task lineages, and tasks whose current roadmap node is not open work are
  available by explicit selection but never chosen implicitly. The bootstrap
  names the rule that chose the task and lists other active tasks (other branches
  or hosts) in one line. The branch is a hint, never a key. Implemented: branches
  travel as their own `checkpoint_branches` record so older builds defer them; the
  presence rule uses the caller's session (the SessionStart hook's, or MCP
  `current_session_id`); hosts are not yet named, since checkpoints record no instance.
- **Squash-merge detection (4e).** When a recorded commit is in the clone but not
  in HEAD, Continuity checks whether its changes landed there anyway and reports
  that instead of off the checkpoint. A commit counts as landed in a target when
  every commit since the merge base has a patch-id equivalent in it (`git cherry`:
  rebase or cherry-pick), or when merging the commit into the target would leave
  the target's tree unchanged (`git merge-tree --write-tree`: a squash, including
  one that also took later commits). HEAD is checked first, then the default
  branch (`origin/HEAD`, else a local `main` or `master`), which the warning then
  names. The check is conservative: a squash whose later commits rewrote the same
  lines conflicts and is still reported as off the checkpoint, and a commit that
  is missing from the clone (its branch deleted after the squash, never fetched)
  still reads as memory ahead of code. A record linking narratives to the merged
  commit is a later follow-up.

Git notes on the code repository are a courtesy copy: they travel only when
`refs/notes/*` is pushed and do not follow a squash. Work narratives in the memory
repository are the durable record.

### Coordination

- **Leases, not locks.** A lease names a scope (track, task, roadmap node, plan
  acceptance; optionally a repository or file glob), a holder (actor, instance,
  session), an expiry, and renewal. Expired leases free themselves when a holder
  crashes or disconnects.
- **Fencing tokens.** Every grant carries a token number that only increases.
  Records written under a lease carry that token, so import can flag writes made
  after the holder's lease was lost.
- **Local margin.** A holder treats its lease as valid only until expiry minus a
  locally measured safety margin, and never assumes a lease it could not renew.
- **Coarse by default.** Track and task scope is the normal grain. Git branches and
  worktrees already coordinate code, so file leases are optional and advisory.
- **Per-scope policy.** `required` scopes refuse to proceed without a lease;
  `advisory` scopes proceed marked unleased, and conflicts surface through fork
  detection on reconnect.
- **Automatic session leases.** A checkpoint saved with a `session_id` (and no
  explicit lease claim) takes or renews a four-hour advisory lease on its task for
  that session, and a completed checkpoint releases it. The grant is local and
  best-effort: a save never waits on the network or fails over a lease, and a
  later sync publishes it. When another session holds the task, the save still
  proceeds and its receipt's `session_lease` names the holder. These leases do not
  participate in liveness or default task selection.
- **Leases are not presence.** A task, checkpoint, branch, or unexpired lease is
  durable coordination state, not proof that another writer process is alive.
  Sequential work by one agent stays in one checkout.
- **Device-local presence.** Hooks and attributable stdio MCP calls append
  short-lived client/session presence with exact task, roadmap, checkout,
  worktree, branch, and invocation identity when known. Active claims expire after
  ten minutes without refresh; Stop/disconnect yields immediately and session end
  closes them. Bootstrap skips tasks with another live session present, while
  `llewm presence` and MCP `continuity_presence` expose bounded exact-filtered
  live/history reads and conservative overlap labels. Paths and claims never sync.
- **Commit attribution.** Each hook reads HEAD from the checkout's Git files (no
  subprocess) and keeps the last HEAD it saw. When HEAD moved during a tool call
  whose input ran a commit-creating Git command, the new commits are recorded as
  made by that session and its task (`commit_authors`, device-local); any other
  move only updates the baseline, so commits made by hand are never attributed
  to an agent. Narrative coverage labels attributed commits with their task, so
  an agent narrates only its own work.
- **Changed underneath and shared files.** A task's footprint is the files in
  its checkpoints' `changes`, its narratives' file anchors, and its attributed
  commits. When a task is bootstrapped, "Changed by others" names commits since
  its latest checkpoint, not attributed to the task, that changed footprint files;
  "Shared files" names footprint files of other `in_progress` tasks checkpointed
  in the last 24 hours. Both are advisory, bounded, and the first content the
  bootstrap budget trims (the change notice shrinks to a count, never vanishes).
- **One worktree per parallel agent.** Continuity coordinates records, not the
  working tree. Agents actually writing at the same time use separate Git
  worktrees and branches (Continuity resolves worktrees to the same project).
  Multiple open tasks or leases alone do not create worktrees. In a shared
  checkout, stage explicit paths so one agent's commit never sweeps in another's
  uncommitted edits. A completed isolated worktree is cleanup-due only when it is
  clean, fully integrated, and unused; otherwise retain it and report why. The
  continue skill carries the full rule; the root `AGENTS.md` keeps a one-line
  reminder, since its text counts against every bootstrap's byte budget.
- **Backends.** Local leases live in the device database. Implemented cross-machine
  leases use Git refs with `--force-with-lease` as an atomic compare-and-swap.
  A separate coordination-service backend remains future work.

Leases ship in three slices (decision `leases-slice-split`):

1. **Local leases** (implemented, migration 050): scopes `task:ID`,
   `roadmap_node:ID`, and `track_plan:ID` in the device database; `llewm lease
   acquire|renew|release|list|show` and MCP `continuity_lease` (listing is CLI only since MCP API 98).
   Acquire is an atomic compare-and-set that grants the next token, renews the
   caller's own lease, or names the current holder. A lapsed lease cannot be
   renewed. Holders rely on a lease until `valid_until`, expiry minus a margin of a
   tenth of the TTL (at least 5 s and twice the measured write time). The bootstrap
   names held leases on the current task's scopes. Agents take leases explicitly,
   and they are advisory only.
2. **Fencing and per-scope policy** (implemented, migration 058): a checkpoint
   with a `task_id` is fenced by `task:ID`, and a plan revision (including a
   review) by `track_plan:ID`. The writer presents its lease as holder, optional
   session, and token (`--lease-holder/--lease-session/--lease-token`, MCP
   `lease_holder/lease_session/lease_token`); the check runs in the write's
   transaction, so a refused write leaves nothing behind. The write records a
   `lease_stamps` row: `leased` (the claim names the scope's current, unexpired
   grant), `unleased` (no claim), or `lapsed` (a claim that no longer holds:
   another token, holder, released, or expired). The `[leases] required` list in
   `config.toml` names the required scopes (`task:ID`, `track_plan:ID`,
   `roadmap_node:ID`, or `KIND:*`); a required scope refuses a write that is not
   `leased` with a "lease required" error, and every other scope is advisory and
   keeps the write with its stamp. The CLI notes a `lapsed` stamp on stderr, and
   checkpoint and plan results carry the stamp. Stamps are append-only; slice 3
   makes the ones with a token travel. Roadmap-node leases stay informational: no
   write is fenced by one.
3. **Cross-machine leases** (implemented, migration 061; decision
   `cross-machine-lease-design`): opt in per machine with `[leases] remote = true`
   in `config.toml`, which needs `[sync] remote`. Each scope's grants become a
   chain of commits on `refs/continuity/leases/<lineage>/<kind>/<scope hash>` in
   the memory repository, written with `git push --force-with-lease` naming the
   tip that was read, so the remote grants a scope to at most one of two racing
   machines (the loser rereads and gets the conflict). Every `lease`
   acquire, renew, and release then fetches the lease refs, mirrors them into the
   device tables, decides against the mirror, writes the remote, and records the
   event locally; `lease list` and `lease show` read the mirror, which also
   refreshes on every sync. Tokens continue from the highest seen here or on the
   remote. The same holder name on another machine is a different holder, and an
   expired lease of another instance keeps its scope for a takeover grace
   (`[leases] skew`, default `30s`) before anyone else may take it, allowing for
   clock differences; the holder's own margin still applies. When the remote
   cannot be reached, an advisory scope gets a local-only lease with a warning
   (`on_remote` false), and the next sync publishes it unless the remote granted
   the scope a newer token meanwhile, which the sync notes; a required scope
   refuses. Fence stamps that carry a token travel as `lease_stamp` records with
   the writing instance. A checkpoint or plan revision stamped with a token after
   a newer grant of its scope existed is a **stale write**, derived from the
   stamps and grants a machine has, whichever arrives first: `llewm fork
   list` and the bootstrap fork line show it as `stale:KIND:ID`, and `llewm fork resolve stale:KIND:ID` records whether it yields to the newer lease
   (`--accept lease`, proposed) or stands (`--accept write`), as a
   `stale_write_resolution` that syncs. The record stays in history either way.
   Wall clocks order a stamp against a grant, so a large clock difference can
   misjudge a write made right at a takeover. A service backend and file-glob
   leases are deferred.

## Online-first, offline-tolerant

Agent work usually needs a network, but not necessarily the sync remote or
coordination service. Local models, split network paths, server downtime, and human
work without an agent all occur. So:

- local writes never depend on a remote;
- bootstrap and status report last sync time, unpushed record count, remote
  reachability, and lease state honestly;
- on reconnect, Continuity pushes the outbox, then pulls and imports, and reports
  forks and stale-token writes for explicit resolution.

## Code-repository hygiene

By default Continuity writes nothing into the code repository. Handoffs, roadmap
renders, narratives, and similar projections are optional renders produced on
request. This also fits open-source and fork workflows: outside contributors see a
clean code repository and simply run in the Local tier, while maintainers share
memory through the access-controlled memory repository.

This source repository keeps its own task projections and development trial logs
out of the public snapshot. Maintained documentation describes the interfaces and
qualification limits; original handoffs, plans and logs remain recoverable through
private Continuity records and the original source-history archive.

## Setups covered

| Setup | Topology | Tier |
|---|---|---|
| Solo, one machine | single | Local |
| Solo, several machines | single or multi-repo | History sync |
| Team, disjoint areas | any | History sync |
| Team, overlapping work | any | Coordination |
| One engineer, parallel agents on one machine | any | Local (local leases) |
| Monorepo | single, no manifest | any |
| Intertwined service repositories | unified manifest | any |
| Loosely related repositories | federated manifest | any |
| Open-source project with outside contributors | any | maintainers sync; contributors Local |

## Implemented topology and coordination

Stable project/repository identity, unified and federated workspace manifests,
member-qualified commit anchors, portable curated history, automatic sync,
record/code fork detection, advisory leases and device-local presence are implemented.
Reviewed workspace split/merge and editor-workspace generation are also available.
They retain explicit identities, preconditions and private data boundaries.

No saved topology, lease or proposal starts an agent, creates a worktree or executes
a task. Verify native platforms and the exact live sync endpoints separately from
source fixtures. See [the roadmap](roadmap.md) for remaining product direction.

## Operational note

A live Continuity database inside a file-sync folder (for example Syncthing or
Dropbox) can be copied mid-transaction or diverge across devices, and WAL/SHM files
can arrive out of step with the main file. Exclude `.llewm/continuity/*.db*` from file
sync; History sync is the supported cross-device path.
