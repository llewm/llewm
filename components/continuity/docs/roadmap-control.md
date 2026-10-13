<!-- continuity:roadmap-node continuity-roadmap-visibility -->
# Deterministic roadmap control

Continuity retains a project-scoped roadmap index so routine freshness checks do
not require a model. This is saved planning state and repository observation, not
automatic reinterpretation of roadmap prose.

## Records

Roadmap nodes have stable caller-chosen IDs and append-only integer revisions.
Each revision records a title, lifecycle status, priority from 0 (highest) to 100
(lowest), project-relative source path and optional source revision. Public writes
are labeled `caller_asserted`; creating a record does not prove user acceptance.

Node links attach a planning relationship to one node revision and another stable
node ID. The bounded vocabulary is `depends_on`, `blocks`, `parallel_with`,
`parent_of`, and `explores`. Links describe plan structure only; they do not create,
schedule, delegate, or execute tasks.

File bindings attach a node revision to a project-relative file or directory and
label the relationship as implementation, contract, test, documentation, or
verification. Directory bindings include Git-tracked and non-ignored untracked
files. Paths cannot be absolute or escape the project root. Regular files are
hashed up to 16 MiB; symlinks hash their link target without reading outside the
project. A status scan is capped at 500 current bindings and 2,000 distinct files.

A model-sync record identifies the reported client, model, optional attributed
invocation, reviewed nodes and reviewed files. It also captures current Git HEAD
and content hashes for all current bindings. The review claim is
`caller_asserted`; the Git/file watermark is separately labeled
`machine_observed`. At least one recorded node, reviewed node, reviewed file, and
model are required. A sync record does not invoke a model or edit any source.

Review routines have stable caller-chosen IDs and append-only revisions. Each
revision is `proposed`, `accepted`, or `superseded` and defines one or more
positive thresholds: commits, elapsed seconds, or changed bound files. The most
recently recorded accepted routine is effective. Acceptance is a caller assertion
with a required source reference; Continuity does not infer consent from prose or
promote a proposal itself.

## Deterministic status

`llewm roadmap view status` and MCP `continuity_roadmap_view` (view `status`) compare the latest
sync watermark with current repository state. They report:

- exact total commits from the saved Git commit to current HEAD;
- elapsed wall-clock seconds since the sync;
- working-tree dirty state;
- added, modified, or deleted bound files with before/after hashes;
- affected roadmap node IDs, revisions, and relationships; and
- `current`, `changed`, or `no_sync` freshness; and
- whether review is due under the effective accepted routine, with exact reasons.

Any commit, dirty working tree, or changed bound file produces `changed`.
`review_due` is separate: it becomes true when there is no prior model sync or
when any accepted threshold is met. Due reasons are `no_model_sync`,
`commit_threshold`, `elapsed_time_threshold`, and `changed_files_threshold`.
Without an accepted routine, status never claims review is due. Status always
states that roadmap meaning was not reevaluated.

`llewm roadmap view overview` and MCP `continuity_roadmap_view` (view `overview`) return a bounded
summary grouped into current focus, blocked, future, and completed nodes. They also
return node revisions recorded after the last model sync, planning relationships,
and the deterministic repository/review status. API 24 adds a per-node derived track
view: parents, subtasks, dependencies, unresolved dependencies, incoming blockers,
blocked targets, symmetric parallel work, and explorations. It also exposes each
accepted review trigger with its threshold, observed value when evaluable, and
`pending`, `due`, or `not_evaluated` state. These views add no relationships or
planning judgments. Each section is independently bounded and reports aggregate
truncation.

`llewm roadmap view render` and `continuity_roadmap_view` (view `render`)
(added at MCP API 25 as `roadmap render`) build a canonical hash over current node
revisions, bindings, relationships, tracks, and accepted trigger observations,
then return deterministic generated Markdown with a visible generated-file marker,
graph hash, renderer identity/hash, and body content hash. Nodes
are ordered by priority then ID; relationships are ordered by source, relationship,
then target. Rendering is read-only and writes only to command stdout or the MCP
response. To prevent elapsed time from invalidating an otherwise unchanged render
each second, projection trigger observations use stable `< threshold` or
`>= threshold` relations. The overview remains the exact numeric diagnostic; a
threshold crossing still changes the projection snapshot.

`llewm roadmap view check --path PATH` and `continuity_roadmap_view` (view `check`) recompute the
same projection and compare it byte-for-byte with one project-relative non-symlink
regular file. The result is `current`, `missing`, or `drifted` with expected and,
when present, actual hashes. Check never parses edits back into records and never
creates or overwrites a file.

`llewm roadmap review-packet` returns source-backed input when the effective
routine says review is due or when a caller explicitly requests a manual packet.
The packet labels its trigger as `scheduled`, `manual`, or `not_due`. A manual
packet does not change the routine, accept a proposal, or create a sync. Initial
and commit/time-triggered reviews include current non-superseded nodes in priority
order; a review with affected
nodes is narrowed to those nodes. Each distinct roadmap source includes its saved
source reference, current SHA-256 hash, redacted content, truncation state, and
`machine_observed` authority. Source content defaults to a 24 KiB aggregate byte
budget and cannot exceed 64 KiB; individual source files must be non-symlink
regular files no larger than 1 MiB. The packet reports its budget and included
content bytes.

## CLI workflow

```sh
llewm roadmap node \
  --id continuity.compaction --title "Compaction recovery" --status active \
  --priority 20 --source-path components/continuity/docs/roadmap.md --source-ref HEAD

llewm roadmap sync --routine \
  --id standard --title "Standard roadmap review" --status accepted \
  --commit-threshold 10 --elapsed-seconds-threshold 604800 \
  --changed-files-threshold 5 --source components/continuity/docs/roadmap.md \
  --source-ref HEAD

llewm roadmap link --kind file \
  --node continuity.compaction --path components/continuity/internal/bootstrap \
  --path-kind directory --relationship implementation

llewm roadmap node \
  --id continuity.portable-memory --title "Portable memory" --status deferred \
  --priority 50 --source-path docs/roadmap.md --source-ref HEAD

llewm roadmap link \
  --node continuity.compaction --target continuity.portable-memory \
  --relationship parallel_with

llewm roadmap view overview --output json

llewm roadmap view render > /tmp/roadmap-preview.md

llewm roadmap view check --path docs/generated-roadmap.md --output json

llewm roadmap review-packet --manual --max-bytes 24576

llewm roadmap sync \
  --client codex --model MODEL \
  --reviewed-node continuity.compaction \
  --reviewed-file components/continuity/docs/roadmap.md

llewm roadmap view status --output json

llewm roadmap review-packet --max-bytes 24576
```

MCP keeps `continuity_roadmap_node` and `continuity_roadmap_view` (views `status`,
`overview`, `render`, `check`); links, syncs, routines, and review packets are CLI
only since MCP API 98. Migration `004_roadmap_control.sql` adds the
index and watermarks; migration `005_roadmap_review_routines.sql` adds explicit routines.
Migration `008_roadmap_links.sql` adds planning relationships. The initial overview,
relationship, and manual-packet surface advanced the MCP API to version 14; derived
track and trigger visibility advances it to version 24 without another roadmap
migration. Canonical snapshot/render/check advances it to version 25, also without
a migration. Migration `072_roadmap_milestones.sql` and MCP API 112 add validated
node kinds, optional milestone target metadata, exact evidence references, and
bounded direct-child milestone rollups to the coverage view.
Migration `073_reviewed_releases.sql` and MCP API 113 add append-only reviewed
release revisions, exact node/narrative selections, and deterministic draft
changelog render/check views. Publication remains an explicit reviewed record
transition and no command publishes externally.
Migration `074_stable_revision_fork_heads.sql`, MCP API 114, and writer API 114
separate envelope observations from content-keyed revision heads and make new
revision resolutions name stable payload hashes. Existing schema-1 resolutions
remain byte-stable and receive deterministic schema-2 normalizations during
migration.

Use `llewm release record --entries-json ...` to append a draft, reviewed,
published, or superseded revision. Each entry names one exact completed roadmap
node revision and one or more work narratives, plus a reviewed category and
wording. A published revision is accepted only when its content hash exactly
matches the latest reviewed revision for the same release ID.

`llewm release view unreleased`, `history`, `render`, and `check` are bounded
reads. MCP exposes the write as `continuity_release_record` and folds the reads
into `continuity_roadmap_view` as `unreleased`, `releases`, `changelog`, and
`changelog_check` to keep the tool catalog within its size budget. Render/check
never write `CHANGELOG.md` and never publish externally.

## SessionStart lifecycle scan

The `SessionStart` hook handler can run the deterministic status comparison above
before delivering the bootstrap, but only when explicitly enabled in the local
project `.continuity/config.toml`:

```toml
[roadmap_scan]
on_session_start = false
```

The field defaults to `false` through Go zero values, matching the checkpoint
reminder's opt-in shape (see [client integrations](client-integrations.md)). When
enabled, the handler calls `Roadmap.Status` after the bootstrap packet is compiled
and appends its deterministic `LifecycleContext` line to the SessionStart
`additionalContext`, but only for `changed` freshness or an explicit `review_due`
reason; `current` or not-due state adds nothing. The appended line repeats the
freshness, `review_due`, due reasons, changed bound file count, and affected node
count, states that roadmap meaning was not reevaluated and that no model was
invoked or sync created, and mentions the bounded review packet only when review is
due. This is the same bounded wording `LifecycleContext` uses elsewhere, so the
line is identical whether it reaches the model through a manual `roadmap view status`
call or this opt-in scan.

The scan is best-effort and isolated from bootstrap delivery: any error from
`Roadmap.Status` (for example, a Git command failing) is swallowed and the
SessionStart hook still returns the compiled bootstrap unchanged. A failed scan is
silent rather than surfaced, matching the disabled/not-due case from the caller's
perspective; it does not retry, block startup, or fall back to a cached result.

## Boundaries and next work

The current implementation uses narrowly scoped read-only Git commands for HEAD,
status, commit counting, and tracked/non-ignored file enumeration. It does not
expose a generic command surface. It does not watch continuously, call a model,
parse roadmap prose, decide whether documentation is semantically correct, edit
files, or accept a proposal on the user's behalf.

Bounded source-backed packets for scheduled and explicitly manual reviews, planning
relationships, grouped overview, derived track/trigger visibility, graph coverage,
node history, and bounded tree reads are implemented. Canonical graph hashing and
read-only render/check are also implemented; explicit file generation remains a
separate future write.
The intended human/model flow is
`overview → packet → discussion → accepted revisions → sync`; only the last two
steps append accepted planning state and a review watermark. Roadmap authority
migration was removed in 6e1. The opt-in
SessionStart scan above and an optional file watcher can reuse the same hash
comparison after scan-based recovery behavior is established. Model review should
consume a bounded affected-node packet and append a proposed or accepted revision
separately.

The [roadmap completeness, change provenance, and agent
coordination](roadmap-provenance-and-coordination.md) track is complete. Its final
phase added work-episode scope comparison, milestone and completion-evidence
rollups, and reviewed release/changelog projections. Advisory multi-agent presence
and leases remain coordination observations rather than locks, assignments,
scheduling, or execution authority.

Stable revision-fork identity and explicit database backup/retention are implemented.
Their source tests and migration receipts qualify different boundaries; installation
and live recovery still require exact versioned verification. Provider adapters,
orchestration and generic execution remain deferred. A roadmap entry grants no
authority to run them.
