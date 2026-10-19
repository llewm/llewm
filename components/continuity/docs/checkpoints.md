# Checkpoint contract

Checkpoints are caller-supplied handoffs, not independent observations. Both CLI
and MCP writes use the same validation/redaction service before persistence.

They are also the canonical handoff primitive. Strict task lineage, checkpoint
actions and transitions, track inventory, and deterministic bootstrap/work reads
make continuation state first-class without requiring a `HANDOFF.md` file or a
provider-specific session-summary store. Such files and external handoff memories
remain useful human-readable projections, compatibility fallbacks, or import
sources; they must identify their source checkpoint/task tip and must not compete
with structured state through newest-file-wins behavior. See the accepted
[ECC interoperability direction](ecc-interoperability.md#handoff-projections).

## Required values and bounds

- Project identity (provided by the application), nonblank task, and nonblank next action.
- Status: `in_progress` (default), `completed`, `blocked`, or `paused`.
- At most 64 entries in each list, including attempts; string list entries must not be blank.
- Every attempt requires a description and outcome; its reason is optional.
- At most 32 KiB of serialized checkpoint JSON, checked before and after redaction.
- The exact prospective checkpoint must also compile with current guidance and
  unresolved actions inside the smaller project bootstrap budget. Both CLI and MCP
  save paths revalidate that condition and reject before persistence when it fails.

Invalid writes return errors without inserting a checkpoint. New checkpoints are
append-only; an existing checkpoint ID cannot be replaced. Existing stored records
remain readable without a historical rewrite.

<!-- continuity:roadmap-node continuity-task-lineage -->
## Strict checkpoint and task lineage

Strict continuation is a compare-and-append contract. For an existing task, the
predecessor must be that task's current tip in the active project. Missing,
cross-task or stale predecessors are refused before persistence; save rechecks
inside the transaction. New tasks have no predecessor. Historical fork/context
resolution determines the accepted tip without erasing rejected observations.

Every checkpoint, strict or not, confines `action_updates` and
`carry_forward_action_ids` to its own task (see task-scoped actions below), so no
caller can complete, supersede, or claim another task's action.

Every checkpoint requires a `task_id`, and every checkpoint after a task's first
requires `predecessor_checkpoint_id` naming the task's current tip, managed or
not. A save without one is refused with the tip to continue from. Two agents can
therefore never silently interleave one task's chain: the second writer to
continue a tip gets a stale-predecessor error and must read the newer checkpoint
first. Work on another agent's task (reviewing or repairing it) belongs under its
own `task_id`. Callers must not encode forks or rekeys by changing `task_id` on a
strict continuation. Fork resolution names the current tip and records the accepted
head as the checkpoint context's `based_on`.

A checkpoint may name `reviews_task_id`: another task this task reviews or
repairs. The reviewed task must have checkpoints and differ from the checkpoint's
own task. The link lives in the reviewing task's checkpoints and never changes the
reviewed task's chain. The reviewed task's bootstrap lists its reviewers with each
one's latest status and summary; the reviewing task's bootstrap names the reviewed
task and its current tip. Under budget pressure the list shrinks to task names.

API 32 and migration 022 add append-only explicit task lifecycle events:

- `start` creates a managed task, optionally adopting its exact existing tip;
- `fork` creates a new active task from an active source task and its exact tip,
  while leaving the source active;
- `rekey` creates a new active task from the exact source tip and terminalizes the
  source key;
- `supersede` terminalizes an active task in favor of another active managed task;
- `abandon` terminalizes an active task without a replacement.

Every lifecycle operation requires a project-scoped `request_key`. An identical
retry returns the original event; reuse with different normalized content is
rejected. Source-tip comparison and event insertion occur in one transaction.
Targets of fork and rekey must be unused, terminal operations are unique, and a
terminal task cannot accept another checkpoint. Once a task is managed, its first
checkpoint may establish the task-local root, but every later checkpoint must name
the exact current predecessor. This opt-in boundary preserves legacy histories
without letting a managed chain fall back to ambiguous append behavior.

Checkpoint writes also accept an optional `request_key`. An identical retry returns
the original checkpoint and reconstructed action result without another write;
different content under the same key is rejected. The key is checked before stale-
tip validation so a lost successful response can be retried safely.

`checkpoint task-lineage --task-id` returns one task's bounded
append-only event history, derived state, and current checkpoint tip. Event meaning
and reasons remain caller asserted; ordering, structural state, and the tip are
machine observed. The read does not resume, redirect, or mutate the task.

`checkpoint task-lineage` without `--task-id` returns a bounded,
task-ID-ordered, read-only report. For managed tasks, `orphan` means a task has more
than one task-local checkpoint root and `multiple_tip` means more than one
checkpoint has no strict child. Pre-lineage histories retain their root and tip
counts for exact inspection but are not labeled as resolvable modern lineage
anomalies. `terminal_selection` means task selection would return a checkpoint for a
terminal lifecycle or a managed active lifecycle whose selected checkpoint already
claims completion. These machine-observed structural findings do not infer intent,
repair lineage, or redirect selection.

`work check` reports the same managed strict-predecessor anomalies. `fork list`
follows recorded checkpoint contexts and excludes heads rejected by resolutions;
checkpoints predating contexts are omitted. Legacy counts remain available in
exact inventory and lineage reads without being presented as current forks.

Each nonterminal (`in_progress`, `paused`, or `blocked`) checkpoint creates a stable
checkpoint action from `next_action` in the `open` state. A `completed` checkpoint
retains its required completion-state `next_action` as handoff prose but does not
manufacture an unresolved action from it. This matches the original migration
backfill rule for historical completed checkpoints.

Actions are task-scoped: an action belongs to the task of the checkpoint that
created it (untasked checkpoints share one untasked group). Creating a later
checkpoint atomically appends a `carried_forward` transition to every unresolved
action of the same task; it never infers completion from replacement prose, and it
never touches another task's actions. Parallel agents working on different tasks
therefore never absorb or reconcile each other's next actions. MCP callers may include `action_updates` (`completed` or `superseded`, with
an optional reason) in the checkpoint dry run and save so those transitions and the
new checkpoint commit atomically. `carry_forward_action_ids` explicitly acknowledges
predecessors that genuinely remain open. A completed checkpoint is rejected unless
every unresolved predecessor from the same `task_id` (including the shared empty
legacy task ID) is either reconciled or explicitly acknowledged for carry-forward.
An action carried by a completed checkpoint stays with its task until someone
reconciles it under that task. The standalone `continuity_checkpoint_action_update`
remains available for lifecycle changes that do not accompany a new checkpoint;
with `task_id` it refuses an action owned by another task.

`continuity_checkpoint_actions` (CLI `checkpoint actions`) lists one task's
unresolved actions with `task_id`, or every task's with `all`, each labeled with
its `task_id`. Without either it refuses when actions span several tasks, so a
caller cannot mistake another agent's actions for its own. Bootstrap renders the
selected task's predecessor actions separately from the latest checkpoint's
immediate text and counts other tasks' unresolved actions in one line. Checkpoint
creation returns both carried and reconciled actions plus whether it created a new
open action.
For pre-lifecycle history, migration backfill leaves only the latest checkpoint
action unresolved. Older checkpoint prose is retained as actions but explicitly
superseded because historical task status cannot prove that each next action stayed
open; this avoids inventing a large unresolved backlog.

<!-- continuity:roadmap-node continuity-checkpoint-readiness -->
## MCP input

Both client plugins include a [checkpoint skill](client-integration-reference.md#checkpoint-skill)
that builds this structured payload and checks the saved bootstrap.

`continuity_checkpoint` accepts `task`, `summary`, `next_action`, optional `status`,
stable `task_id`, `predecessor_checkpoint_id`, `request_key`, and `session_id`, plus `completed`, `changes`, `verified`, `attempts`,
`evidence_refs`, atomic `action_updates`, and explicit `carry_forward_action_ids`.
The SDK requires the `summary` property, but it may be empty.
When a saved checkpoint has a stable `task_id`, the Markdown bootstrap prints it
beside the task title; the exact selected checkpoint ID is printed above the task.
A receiver can use those two references for strict same-task continuation without
recovering the task ID from prose or guessing from the task title.

For prospective byte accounting, `continuity_checkpoint` with `dry_run` accepts the same structured shape
and performs the same validation and redaction in memory. It compiles the candidate
with the current engineer guidance, project guidance, projected carried-forward
actions, and the same notices used by saved bootstraps, then reports whether both
JSON and Markdown fit the configured bootstrap budget. Selection accounting uses
the larger current explicit-readback or automatic-startup notice in each format.
Exact totals are accompanied by Markdown section costs for the base packet
(including notices, selection, pins, and named slices),
global guidance, project guidance, checkpoint kernel, optional checkpoint detail,
and the projected predecessor-action state after the supplied dispositions. It
reports current/projected unresolved counts, reconciled and explicitly carried
counts, whether the checkpoint would create a new action, and up to 64 projected
predecessors with an explicit truncation flag. The result has
`prospective_caller_asserted` authority and
does not persist a checkpoint, action, or observation. The write surface repeats
this validation for the exact payload it receives, so direct callers cannot bypass
the startup budget or rely on a preflight performed for different content.
Readiness describes the current snapshot: later changes to guidance, notices,
other tasks, or the reading session's selection can change the packet size.
On read, if those changes exceed the budget, the compiler first drops advisory
expansions, then defers whole completed/changes/verified/evidence-reference fields
in that order. `checkpoint_details_omitted` and a Markdown notice name the deferred
fields and exact checkpoint retrieval. The saved checkpoint is unchanged. Task
identity, status, summary, next action, prior attempts and unresolved actions stay
in the packet; global/project guidance retains its existing protection. If this
minimum still cannot fit, the error reports both serialized sizes and directs the
caller to exact checkpoint retrieval. New saves still require the complete
prospective packet to fit; this recovery path does not weaken write validation.

Restart checks remain attributable task acceptance checks. SessionStart does not
prove completion. The built-in `resume-completion` rule and matching continue,
checkpoint and update skills require the resumed agent to verify actual runtime
and checkpoint recovery, reconcile satisfied actions and qualification plans, and
save completion in the same turn. Later rollout evidence may satisfy earlier
checks after ancestry and behavior review. Explicit user-requested cross-task
cleanup uses a separate review task and lifecycle supersession, preserving old
handoffs rather than writing another agent's checkpoint. Substantive deferred
platform/client work remains open.


`attempts` is an array of objects with `description`, `outcome`, and optional
`reason`. `evidence_refs` is a list of source references such as recorded event IDs,
file locations, or report identifiers. References are not fetched or independently
checked and do not promote a claim to machine-observed evidence.

`verified` remains backward-compatible, but its entries are explicitly rendered as
verification claims. New writes have `authority: caller_asserted`; callers cannot
set machine-observed authority through this service. Legacy handoffs receive the
same conservative framing in Markdown even if they lack an authority field.

The bootstrap displays completed work, changed artifacts, verification claims,
evidence references, and attempts alongside task state. All output remains under
the existing bootstrap byte budget. No actions run automatically from a checkpoint.

The CLI checkpoint adapter has the same persistence semantics as MCP. In addition
to the task kernel and lease flags, `checkpoint create` accepts repeatable
`--completed`, `--change`, `--verified`, and `--evidence-ref` values; structured
`--attempts-json` and `--action-updates-json` arrays; and repeatable
`--carry-forward-action` IDs. The dry run and save both apply those action
dispositions atomically through the same application service as MCP. A save with
`--output json` returns the checkpoint ID, carried and reconciled actions, whether
a next action was created, the lease stamp when present, and unnarrated commits.
Both routes accept a request key for idempotent checkpoint retries. Explicit task
lifecycle operations are exposed as `checkpoint task-transition`; the deterministic
report is `checkpoint task-lineage`.
Startup counts omit actions from explicitly retired task lineages; exact task/all-action
reads retain their unchanged historical state. Supersession is not fabricated
action completion. Lifecycle records currently remain device-local and are not
transported by curated memory sync.

Both routes enforce persistence bounds, completed-task reconciliation, and the
compiled bootstrap budget on every save; preflight remains useful for section-level
diagnostics before retrying an oversized candidate.

## Research-capture advisory

MCP API 108 adds `research_capture` to checkpoint dry-run and save receipts when the
checkpoint's session has substantial bounded discovery activity since its
predecessor (or since session start for a first checkpoint). The CLI JSON receipt
returns the same object; text output prints one compact line.

The derived report reads only already-redacted hook observations. It classifies
bounded history queries, repository reads/searches, and web research by tool name
or a small deterministic command vocabulary. It does not load tool-result content.
The report counts task-anchored narratives, task-linked decisions, originating
work items, checkpoint evidence references, and whether the checkpoint contains
completed, verification, or attempt detail.

`covered` means those cheap structural signals show reusable output;
`review_recommended` means substantial discovery produced none of them. Light
discovery emits no report. API 132 adds structured source-citation coverage when
observed web discovery crosses the threshold: `cited_claims`, `uncited_findings`
and `source_observations`. A plain evidence reference, unrelated record or
checkpoint detail cannot alone mark web coverage complete; uncited outcome or
verification claims request review. Source coverage does not judge truth. The threshold is deliberately conservative: at least
six discovery operations across two kinds, or ten operations of one kind.
The advisory is best effort and never blocks or changes the checkpoint. It cannot
decide whether a record is correct, complete, useful, or excessive; its purpose is
only to expose a suspicious mismatch for human or agent review.

## Explicit checkpoint selection

`status`, `bootstrap`, and `next` accept one optional selector: `--checkpoint-id`,
`--task-id`, or `--session`. MCP `continuity_bootstrap` and `continuity_work`
accept the corresponding `checkpoint_id`, `task_id`, or `session_id`. Exact
checkpoint selection returns that project-owned snapshot; task and session
selection return the newest matching checkpoint using stable `(created_at, id)`
ordering. A selector with no match returns no checkpoint, and multiple selectors
are rejected instead of guessing. Omitting all selectors preserves the legacy
project-latest read for existing clients.

When no checkpoint matches, bootstrap emits a bounded factual recovery fallback
instead of inventing task state. It labels the missing selection, reports only a
small newest-first window of machine-observed event metadata, states explicitly
that activity cannot establish task intent or completion, and directs callers to
`checkpoint show` (by ID or `--query`) and `work freshness` for targeted expansion. Event payloads and
transcript content are never included. This fallback advances the MCP API to 17
without a database migration.

Task IDs are caller-managed stable labels and required for new writes.
Historical taskless checkpoints remain readable. They do not create, rename, or mutate rows in the scaffolded
`tasks` table. Session and task selection remain project-scoped. Migration 007
adds the checkpoint task column and task/session lookup indexes; migration 021
adds strict checkpoint-to-checkpoint lineage and its indexes; migration 022 adds
checkpoint request receipts and append-only task lifecycle events. Historical
payloads remain unchanged and readable. Portable curated export does not yet carry
task lifecycle events or restore managed-lineage state; imported checkpoint history
therefore remains unmanaged until a separately designed portable record family is
implemented and qualified.

Project selection is a separate process boundary. The root-level CLI flag
`--project-root PATH` selects the containing Git project before configuration or
storage is opened, so checkpoint commands and `llewm mcp` do not depend on
the launcher's current directory. An MCP process stays bound to that project;
task/session/checkpoint selectors cannot cross it. A missing path or a path outside
a Git project fails instead of falling back to another saved project.

## Freshness and observed activity

`llewm work freshness` reports the selected checkpoint's age and the
number of project events received after it. `--limit` bounds a newest-first list
of payload-free event observations to 1–100 entries (20 by default); the result
states when more observations exist. The command accepts the same exact, task, or
session selector as other checkpoint reads. MCP exposes the same report as
`continuity_work` (view `freshness`).

If no checkpoint matches, freshness keeps `events_since_checkpoint` at zero and
reports the total project event count separately as `events_observed`, with the
same bounded newest-first metadata window. This is project activity available for
recovery triage, not evidence of a task, its status, or its next action. The added
field and no-checkpoint semantics advance the freshness result schema to version 2.

Callers may optionally provide `--remind-after-seconds` and/or
`--remind-after-events`; MCP uses `remind_after_seconds` and
`remind_after_events`. Zero leaves that threshold disabled. When at least one
threshold is enabled, the report sets `reminder_evaluated` and deterministically
returns `checkpoint_recommended` with zero or more reasons from `no_checkpoint`,
`age_threshold`, and `event_threshold`. A missing selected checkpoint recommends
creating one. Thresholds are caller policy, not defaults chosen by Continuity.

The report labels the checkpoint as caller asserted and the event ledger as
machine observed. Activity remains project-scoped because lifecycle hooks do not
reliably supply task IDs. `current` means no later event was observed;
`activity_since_checkpoint` means at least one was observed; `no_checkpoint` is
explicit. These states and reminder results do not infer task progress, request or
create a checkpoint, or create semantic state. The caller decides whether and how
to surface a recommendation. Freshness first advanced the MCP API to version 13;
explicit reminder evaluation advances it to version 15 without a database migration.

The hook integration can opt in through `[checkpoint_reminder]` in the local project
configuration, using `remind_after_seconds` and `remind_after_events`. Positive
values evaluate after each successful post-tool observation. Hook reminders are
session-scoped: age and event counts start at the latest checkpoint of the
session's task (the task it leases, else the task it last checkpointed), or at the
session's first observed event when it has none, and only that session's events
count. Another agent's checkpoint or activity therefore neither resets nor
triggers this session's reminder. A deterministic derived event atomically claims
at most one reminder response per session and checkpoint, so concurrent hooks and
later events do not repeat it. A newer checkpoint of the session's task rearms the
edge. The claim is recorded as `checkpointreminderrequested` with no payload; it
proves neither client delivery nor model receipt. Defaults remain zero, and the hook
does no freshness read when both are disabled.

## Historical find, inspect, and reuse

`llewm checkpoint show --query TEXT` searches only the active project's saved
checkpoint fields with deterministic literal matching. `%` and `_` are treated as
text, not SQL wildcards. Results contain stable checkpoint IDs, timestamps, task,
status, a short excerpt, and caller-asserted authority. `--limit` is 1–100 and an
opaque `--cursor` continues the stable newest-first `(created_at, id)` order.
No matches return an empty result; they never fall back to the latest checkpoint.

`llewm checkpoint show ID` inspects one exact project checkpoint and
emits a reusable Markdown packet. `--output json` wraps that same packet with its ID,
timestamp, authority, requested byte budget, and truncation outcome. The Markdown
budget defaults to 16 KiB and is constrained to 512 bytes–32 KiB. Inspection does
not update the latest checkpoint, resume the old task, or authorize its saved next
action.

MCP exposes the same reads as `continuity_checkpoint_get` (`query` or `id`). Both identify saved content as caller asserted and
state that no model ranked or interpreted results. Search is intentionally lexical;
there is no FTS index, embedding model, or semantic ranking in this slice. The
history tools advanced the MCP API to version 11; explicit selection advances it
to version 12 and schema migration 007. Freshness reporting advances the API to 13
without changing the schema.

## Redaction

Redaction runs before writing either indexed SQL columns or the JSON payload.
It covers recognized credential assignments (including quoted values), bearer
headers, and HTTP URL passwords. Nested metadata is sanitized recursively; known
credential, stdin, and environment keys have their entire values replaced.
Caller-owned input collections are not mutated.

This is deterministic pattern-based redaction, not a guarantee that arbitrary
secret text is recognizable. Do not place credentials, raw stdin, or environment
dumps in handoffs. Historical records are not retroactively rewritten by this change.

## Restart-safe checkpoint recovery

MCP API 107 keeps a write-incompatible upgrade from turning a required restart
into checkpoint data loss. If the running MCP process is below the database's
writer floor, `continuity_checkpoint` does not write SQLite with obsolete
semantics. Instead it validates and redacts the structured checkpoint and action
reconciliation, writes a bounded device-local envelope under Continuity's state
directory with mode `0600`, and returns `deferred: true` plus a recovery ID. It
does not capture transcript text, stdin, environment data, or generic context.

After restart, `llewm checkpoint recovery list` shows pending metadata and
`llewm checkpoint recovery apply ID` saves through the current checkpoint
path. Apply retains the envelope on any validation, write, or readback failure;
it removes the envelope only after the saved checkpoint is read back by ID.
Applying a recovered checkpoint preserves its historical session attribution but
does not revive that session's live presence or renew its advisory task lease.
`llewm checkpoint recovery discard ID` is the only explicit unsaved-discard
operation. Pending IDs appear in bootstrap until applied or discarded. Reusing a
request key with different staged content is rejected, while an exact retry
returns the original recovery envelope.

## Verification status

Service tests check invalid-write rejection, database-level secret removal,
append-only behavior, legacy reads, literal matching, stable paging, project
isolation, no-match handling, bounded export, task/session selection, stale no-match
behavior, ambiguous-selector rejection, freshness states, activity bounds, and
authority labels. CLI and real stdio MCP tests exercise validation, redaction,
find, inspect, freshness, stale-writer staging, and confirmed recovery apply; MCP round-trips attempts and source references. Golden
bootstraps cover verification labels and the restored handoff fields.

After installing an updated binary, restart clients so their MCP processes reload
its input schema. Updated live client round-trips and productive continuation are
separate checks from these automated tests.
