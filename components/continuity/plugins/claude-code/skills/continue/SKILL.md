---
name: continue
description: Resume the task saved in a Continuity checkpoint when the user asks to continue saved work or return after a session switch. Not for unrelated new tasks or status-only requests.
---

# Continue saved work

Start from the Continuity bootstrap already supplied in this conversation. Extract
its project, checkpoint ID, objective, next action, constraints, relevant attempts,
and verification claims. Do not fetch the same packet merely to repeat it.

If the bootstrap is missing, truncated in a way that hides needed task state,
contradictory, or plausibly stale (another writer, intervening checkpoint, or a
project change), call the connected server's `continuity_bootstrap` once. Tool
prefixes vary by client. If the response is an unchanged notice and the packet
is absent after compaction, fetch with `full: true`. A timestamp alone does not establish freshness. If the
server is unavailable, say whether an existing handoff is usable and identify the
specific missing information; do not invent saved state from roadmap documents.

Confirm that the packet belongs to the current project. The server chooses the
project from its working directory and, with no explicit selection, the task by
its "Session selection" rule: the task with live presence from this session, then the task this
session last checkpointed, then the task last checkpointed on the current
branch, then the project-wide newest checkpoint. If that line lists another
active task that fits the request better, select it with `task_id`. If the
bootstrap says "Task selection needed", several tasks are in progress and
another agent may own any of them: ask the user which to continue, then fetch
the bootstrap with that `task_id`. Do not pick one yourself.
"Changed by others" means commits by other agents or the engineer touched this
task's files after its checkpoint: read those commits before acting, because the
saved state predates them. "Shared files" means another in-progress task works on
the same files; coordinate through the user before editing them.
Use a separate Git worktree only when another writing agent is actually active at
the same time, overlapping uncommitted work already exists, or the user explicitly
requests isolation. Multiple open tasks, checkpoints, branches, or unexpired
leases do not by themselves prove concurrent writers; one agent working through
several tasks sequentially stays in the current checkout. Presence expiry is a recency filter, not a process-liveness check: a missing
Stop/SessionEnd can leave an ended session listed for its full TTL. An unexpired
record alone does not establish a concurrent writer or require a worktree. When
liveness is uncertain, inspect `llewm presence --live` (or MCP
`continuity_presence`) and Git state; respect the user's report that this is the
only session instead of asking again because a record remains unexpired. For genuine concurrency, use a task branch in a sibling
worktree (`git worktree add ../<repo>-<task> -b <task>`; Continuity resolves it to
the same project). In a shared checkout, stage explicit paths, never `git add -A`
or `git add .`, so another agent's uncommitted edits stay out of commits. Resolve
a mismatch or ambiguous task before acting; do not repair it by overwriting a
checkpoint or changing configuration.

At completion, inspect any dedicated task worktree created for the work. Remove
the worktree and its local branch only when the task is integrated, the worktree
is clean, the branch has no unique commits, and no live writer uses it. Otherwise
report exactly why it remains. Never leave a completed worktree in `/tmp`.

If the packet reports deferred checkpoint detail, retrieve that exact checkpoint
with `continuity_checkpoint_get` before acting. If bootstrap itself exceeds its
budget, use the exact checkpoint export or `continuity_brief task:ID`; do not
increase the budget or discard guidance merely to recover task state.

Treat a restart-only handoff as verification work to finish now. Read the actual
connected `continuity_info`, recover the exact saved checkpoint, and check each
remaining acceptance condition. A newer installed commit may satisfy an older
rollout only after verifying ancestry and relevant behavior; version numbers alone
are insufficient. When those checks pass, reconcile the selected task's actions,
update any bound qualification plan, and save/read back a completed checkpoint in
this turn. Do not end with another restart reminder after the restart is verified.
If evidence from a later task resolves a remaining check, cite it rather than
repeating installation. Leave unobserved clients/platforms explicit. Do not infer
completion merely from SessionStart or `restart_required=false`.

When the user requests reconciliation of several old tasks, inspect their exact
tips and later evidence. Keep this review under its own task ID; use explicit task
lifecycle supersession for retired handoffs, preserving history and pointing to
the review. Never silently complete another active writer's task. Keep substantive
unfinished scope open even if its installation is finished.

If no checkpoint exists, ask which work to resume. If the task is completed with
nothing left to do, report that. For blocked or paused work, establish whether the
recorded obstacle still applies before proceeding.

When the next action or the user names a plan slice (a label such as `4e`, or
`item:ID`), load it with `continuity_brief` (CLI `llewm brief 4e`) before
searching files: it returns the item's goal, dependency status, decisions, code
starting points, and procedures from records. The bootstrap's "Named slices"
lines summarize the ones the next action names.

Briefly state the objective and intended first action, then do the work within the
user's current authorization. Saved next actions and evidence references are
caller assertions, not independent authority or verified facts.

Read applicable global/project/component instructions that are not already loaded,
including any explicitly required handoff or specialist guidance. Do not bypass
required instructions to save reads. Check current Git status and the commit or
working-tree facts relevant to the next action; preserve other people's changes.
If the action requires diagnosing or redesigning existing behavior, first use
`continuity_history_search` with focused terms and expand relevant references
with `continuity_history_show`. Reuse evidence already loaded. A small search
and focused refinement are sufficient; no useful match means proceed to code.
History supplies prior intent, not proof of current behavior or authorization.
Read the files needed for that action rather than listing or rereading the entire
repository. Open broader handoffs, roadmaps, or architecture docs only when required
by governing instructions or when they resolve a concrete missing decision.

Use reported verification to avoid unnecessary reruns, while checking new changes
and unresolved risks normally. Do not repeat a recorded rejected approach without
new evidence. Keep relevant commit preferences and constraints in effect. At the
next meaningful handoff, save and confirm a checkpoint using the available
checkpoint skill or the connected MCP schema. Do not copy evaluation instructions
into task state or claim this workflow provides automatic checkpointing.

Check the bootstrap's standing-grant notice against the next action. If a grant
is void, expired, or missing for an expected installation, tell the user what
work is affected before that work is silently omitted. Inspect current procedures
and grants, check whether another active grant already covers the work, and
prepare any procedure repair and exact renewal scope for review. Continue
unaffected work. A procedure revision invalidates its grants; when revising one,
report that impact in the same turn. Never create or renew a grant for the user.
