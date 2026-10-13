<!-- continuity:roadmap-node continuity-track-llewm -->
# Track continuity

Continuity preserves explicit task identity, ordered checkpoint lineages, reviewed
plans, joined work views and attributable proposals. These are descriptive state,
not an execution queue. A completed tangent must not silently replace the intended
task or turn an unreviewed proposal into an assignment.

## State boundaries

- Roadmap nodes describe planning state.
- Managed tasks describe ordered checkpoint lineage and lifecycle.
- A checkpoint's next action describes its selected immediate continuation.
- Checkpoint actions retain explicit unresolved same-task obligations.
- Work items preserve ideas, bugs, tasks and research questions with attribution.

Captured, accepted, ready, active and resumable are distinct states.
None grants execution permission. User-visible model suggestions are idea work
items with model-suggested authority; never capture hidden reasoning or transcripts.

<!-- continuity:roadmap-node continuity-track-plans -->
## Versioned track plans and work items

A track plan has a stable ID, roadmap node, optional task, title/summary, status,
creator/model authority and a complete ordered item snapshot. Items retain IDs,
details, statuses, parents, dependencies, evidence and linked work items.
Changing a plan appends another whole-plan revision rather than rewriting history.

Model-authored revisions stay proposed. Human review through
`llewm track-plan record PLAN --creator REVIEWER --status active` or MCP
`continuity_track_plan_record` without items appends the reviewed revision.
Review is an explicit user choice, not an inference from work beginning.
Completion requires all items to be completed or superseded.

`llewm track-plan show` reads outlines, one item, full detail, history or
dependency-satisfied ready items. These reads neither select work nor execute it.
A plan naming an existing task and node can establish their explicit binding.
Joined views report missing, unreviewed and inconsistent plan/binding state.

## Optional current-plan view

`llewm track-plan show --render` produces deterministic Markdown with an exact
source-snapshot hash and body hash. `llewm track-plan check` compares its optional
project-relative destination without writing. Neither operation changes authority,
accepts a plan, advances an item or makes the output an instruction source.
Projection checks run only on explicit request; ordinary work views do not require
the file or report it as missing or drifted.
This source repository ignores its own generated current-plan projection.
See [current-work views](project-state-hygiene.md).

<!-- continuity:roadmap-node continuity-track-transitions -->
## Task identity and transitions

A task advancing a roadmap node should use an explicit binding when their IDs
differ. Do not infer that relationship from a branch name or similar titles.

The current lifecycle uses `llewm checkpoint task-transition` for deliberate
start, fork, rekey, supersede or abandon operations. Operations require exact
identities and request keys; applicable source-tip checks reject stale continuation.
Use the command's help and [checkpoint contract](checkpoints.md) for its fields.

The separate interruption/return ledger was removed before release.
An intended return should remain explicit in checkpoint state and reviewed plans.
Completing a tangent does not select or mutate its source task.

<!-- continuity:roadmap-node continuity-track-reconciliation -->
## Legacy lineage

Historical task records remain readable. Inspect their exact roots and tips through
`llewm checkpoint task-lineage`; do not invent order from timestamps, prose or
task titles. The retired reconciliation planner is not a current command.

Corrections and explicit reviewed lifecycle operations preserve original records.
To review or repair another agent's work, use a separate task with
`reviews_task_id`, rather than writing to that agent's tip or actions.

<!-- continuity:roadmap-node continuity-track-inventory -->
## Joined inventory

`llewm work inventory` joins roadmap, task, checkpoint, plan, unresolved-action
and work-item state without a model. Filters include all, everything, current focus,
active, resumable, blocked, anomalous and stale. Counts, bounds and exact references
make omissions and ambiguity visible.

Resumable is a structural classification, not an assignment.
Missing checkpoints, ambiguous bindings, terminal-state differences and stale
evidence stay visible; the read does not repair them.
Use `llewm work`, `work check`, `work list` and exact `work show`
for the corresponding operational views.

<!-- continuity:roadmap-node continuity-continuation-routing -->
## Session selection

Explicit selection wins. Otherwise bootstrap considers the session's live task
presence, its last checkpointed task, the current branch and then the newest task.
Branch/newest selection prefers unfinished tasks and can require explicit choice
when several recent tasks are in progress. Use the returned selection explanation.

A default selection is not proof of ownership or current source state.
Continue from the selected exact checkpoint, read required instructions and inspect
relevant Git facts. Reuse relevant history already loaded; search narrowly before
diagnosing existing behavior.

Advisory presence and leases support coordination but do not prove process liveness
or lock files. See [parallel tracks](../../../docs/parallel-tracks.md) and
[client integrations](client-integrations.md).

## Qualification and non-goals

Qualify exact-origin capture, ambiguity, stale tips, redaction, replay and bounded
reads with isolated fixtures. Fresh-client interruption/return behavior and broad
concurrent reliability need separate observations. Historical trial diaries remain
private rather than serving as current installation instructions.

Continuity does not infer priorities, assign agents, schedule work, create worktrees,
merge branches, execute a plan, or silently disposition a suggestion.
