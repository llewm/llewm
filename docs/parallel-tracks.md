# Parallel development tracks

Continuity supports several durable tasks without turning their plans into an
execution queue. Use explicit task identity and current Git facts to select work.
A saved checkpoint, roadmap entry, presence observation, or lease is not permission
to start an agent or execute a next action.

## Sequential handoff

1. Save a structured checkpoint under a stable task ID with the objective, completed
   work, attempts, verification, and concrete next action.
2. Reconcile completed or superseded predecessor actions; deliberately carry only
   genuinely unfinished obligations.
3. A fresh session uses the supplied bootstrap, verifies project/task identity and
   current Git state, and continues the same task from its exact tip.
4. If selection is ambiguous, choose the intended task explicitly rather than
   replacing another task's state.
5. Complete a task only when its work and required checks are done. A terminal
   checkpoint does not create another open action from its follow-up prose.

When a task advances a roadmap node, bind them explicitly if their IDs differ.
Roadmap status and task status remain separate; finishing one does not automatically
finish the other. See [track continuity](../components/continuity/docs/track-continuity.md)
and [checkpoints](../components/continuity/docs/checkpoints.md).

## Actual concurrency

Use one writing agent per task. For genuinely concurrent implementation, use
isolated Git worktrees or clones and stage explicit paths. Several open tasks,
unexpired leases, or recent presence observations alone do not prove multiple writers.
One agent working through tasks sequentially can keep the current checkout.

`llewm presence --live` and MCP `continuity_presence` report bounded device-local
session/task/worktree observations. Presence expires and is advisory; it is not a
process-liveness proof, lock, or durable task assignment. Leases are also advisory
unless a project explicitly requires fencing for a write scope.

Coordinate actual overlapping work before editing. Strict predecessor comparison
protects the checkpoint tip; it cannot prevent simultaneous uncoordinated file edits.
Never checkpoint under another agent's task merely to review or repair it.

After integration, remove a dedicated worktree and local branch only when the task
is integrated, the worktree is clean, the branch has no unique commits, and no live
writer uses it. Otherwise retain it and state the reason.

## Recovery and boundaries

Historical context may explain why a task exists, but current source/runtime
observations establish current behavior. Search narrowly, expand relevant references,
and preserve the distinction between the task doing a review and its subject.

Wrong selection, stale state, ambiguous bindings, and unresolved actions are reasons
to reconcile facts, not to rewrite append-only history. Use explicit task lifecycle
operations for a deliberate fork, rekey, supersession, or abandonment.

Historical sequential cross-client trials informed this workflow; they do not
qualify all concurrent-client behavior. A future optional coordinator must not
silently schedule work, start agents, create worktrees, or enforce execution.
See [coordination boundaries](../components/continuity/docs/roadmap-provenance-and-coordination.md).
