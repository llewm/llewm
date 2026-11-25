# Assigning and picking up work across agents and platforms

Choose by what the recipient needs to do. **Leave a task for later** with a Continuity work item. **Reserve a job for one eligible worker** with the shared-work coordinator. **Reach a running agent** through an agreed messaging channel. These can be combined: a message can point to an item or attempt, and a checkpoint can preserve progress between sessions.

| Situation | Mechanism | Where the recipient starts | What establishes progress |
| --- | --- | --- | --- |
| Resume the same task after a restart or on another host | Continuity checkpoint under a stable task ID; History sync for another host | Bootstrap, or `llewm brief task:ID` | Exact checkpoint tip, unresolved actions and verification |
| Leave Windows or macOS work for a later session, brokerless | Continuity work item with platform and purpose tags; configured History sync | Sync, list the platform tag, then open the item by ID | Worker publishes a task checkpoint or result narrative referencing the item |
| Several eligible workers may compete; dependencies, reassignment or review matter | Orchestration shared-work coordinator with an accepted plan and authenticated members | Worker MCP `work_queue` on configured plans, inspect the job, then `work_claim` | One current attempt per plan item; submit evidence and have a reviewer accept it |
| Exchange timely requests, blockers or replies between running sessions | Orchestration local broker on one machine, or shared project broker over HTTPS across machines | Local Codex `llewm-orchestrate broker-inbox SESSION_ID`; other local clients use broker pull/MCP. Remote clients use the configured shared-broker profile and pull/MCP | Explicit acknowledgment or reply tied to the message ID. Hooks and bounded waits help active clients notice messages |
| Exchange messages on one machine without installing a broker | Manual file mailbox in an agreed local folder | Open the agreed channel and poll published messages | Participants publish immutable acknowledgment and reply files |
| Delegate a bounded piece inside one active client session | Client-native subagent, where supported and authorized | Parent gives the child its scope and collects its result | Parent verifies and integrates the result; save a Continuity record if another session needs it |
| Repeat a deterministic check on several operating systems | CI runner matrix | Workflow run for the source commit, then the job for each OS | Per-platform conclusion and receipt. Use a native-host work item when installed state, hardware or investigation matters |

## Brokerless pickup in practice

An **async job** in this workflow is an ordinary tagged work item. The producer supplies a stable ID, target platform, source commit or retrievable ref, prerequisites, acceptance checks and where to return evidence. Tags are routing conventions. Any reader of the shared history can see the item. Use one named implementation owner when multiple hosts need to test the same fix.

For example, the Windows worker owns the baseline change and the macOS worker verifies the resulting commit. From a project configured for the same History sync lineage:

```sh
llewm sync
llewm item list --kind task --status pending --tag windows
llewm item show native-sync-qual-windows-20261125
```

On macOS, substitute `--tag macos` and open the returned ID. To find both sides of this example, list `--tag native-sync-qual`. For a new request, use `llewm item add --help` for capture fields, then sync. Each worker checks the requested source revision in Git, records pickup and results under its own task with the item ID, and syncs back. A dependent worker needs the tested Git ref as well as the history record. A successful sync confirms publication; a worker-authored response confirms pickup.

Two readers can see the same pending item. Use the coordinator if duplicate pickup would be costly: its queue filters by plan, platform and capabilities, and the claim transaction arbitrates competing workers. Assignment can target an engineer or leave the item open to eligible members. Reassignment fences the old attempt's authority to publish a current result; late contributions remain available for review. [Coordinator work cycle](../components/orchestration/README.md#manual-work-cycle)

## Find the right state first

- **What needs doing?** List tagged work items for async requests, read `llewm brief LABEL` for a known plan slice, or use the configured coordinator's worker queue for claimable jobs. `llewm resource show` lists recorded host and service references when the destination is unclear.
- **Who is active or can write?** `llewm presence --live` shows recent device-local session observations. `llewm lease list` shows time-limited scope holders. A lease is normally advisory; configured required scopes check a fencing token on supported record writes. Cross-machine leases require the configured shared lease remote. A lease does not claim a pending work item or lock a Git checkout.
- **Where is the actual result?** Use the task checkpoint or narrative for a synced handoff, the reviewed submission for a coordinator job, or the CI artifact for a workflow run. Source branches and commits travel through source Git. Use history search for prior rationale once an exact item, task, plan or receipt is insufficient.

Continuity owns durable project context. Orchestration provides the work coordinator and two distinct messaging brokers; a broker acknowledgment establishes receipt, while the coordinator tracks assignment and review. Agent clients and CI runners execute the work. Action Control validates policy inputs and Private Storage supplies filesystem primitives; neither has a pickup queue. For channel setup see the [local broker](../components/orchestration/docs/agent-broker.md), [shared broker](../components/orchestration/docs/shared-agent-broker.md), or [file mailbox](../components/orchestration/docs/file-mailbox.md).

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
qualify all concurrent-client behavior. The separate shared-work coordinator supports manually claimed jobs. Execution and source integration stay with the worker and reviewer.
See [coordination boundaries](../components/continuity/docs/roadmap-provenance-and-coordination.md).
