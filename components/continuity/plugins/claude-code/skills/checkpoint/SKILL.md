---
name: checkpoint
description: Save a structured Continuity handoff when the user asks to checkpoint, preserve progress, or prepare to switch sessions, or when project instructions require a task-boundary checkpoint.
---

# Save a Continuity checkpoint

Use the connected Continuity MCP server's `continuity_checkpoint` (with and
without `dry_run`) and `continuity_bootstrap` tools; client prefixes may
vary. Before declaring either tool unavailable, inspect the client's complete or
lazy-loaded tool catalog and use its tool-search mechanism when one exists. The
initial prompt's visible tool list may be only a subset. If Continuity is
configured but its tools still cannot be loaded, inspect connection/runtime
status and report whether a client restart is required. Inspect available schemas
before calling them. The server selects the
project from its working directory; there is no project argument. If the project
is unclear, read the bootstrap and resolve any mismatch before writing. Select the intended task explicitly with `task_id`; retrieve its checkpoint with
`continuity_checkpoint_get` or its bootstrap with `task_id`. Multiple tasks can
coexist; selection does not grant a lease or permission to overwrite another tip.

Build the handoff from the current conversation and available work evidence.
Check current files or results only where needed to resolve uncertainty. Do not
repeat discovery or rerun checks merely to fill fields. If essential task context
is missing, ask for it instead of inventing a task.

## Handoff fields

- `task_id`: required. Continue the task you are working on, or choose a new,
  descriptive ID for new work, including reviewing or repairing another agent's
  task. Never save under another agent's task to report on it.
- `reviews_task_id`: when this task reviews or repairs another agent's task, name
  that task. Both tasks' bootstraps then show the link, so its agent reads your
  findings before continuing.
- `task`: the current objective, not merely "save a checkpoint".
- `status`: `in_progress`, `completed`, `blocked`, or `paused`. A session
  ending does not imply that its task is completed.
- `summary`: concise current state, important decisions, constraints, and
  remaining uncertainty. Include this required property.
- `completed`: work actually finished.
- `changes`: relevant changed file paths or artifacts; distinguish uncommitted
  work from committed or installed work when it matters.
- `verified`: checks actually run and their outcomes, with scope and attribution.
  Label user-reported or inherited results as such; put unrun checks and limits
  in the summary. These remain caller-asserted claims.
- `attempts`: relevant failed or rejected approaches as objects with
  `description`, `outcome`, and optional `reason`. Preserve what prevents a
  future agent from repeating them; distinguish known causes from guesses.
- `evidence_refs`: available source references, such as paths, commit IDs,
  `decision:<id>` and `narrative:<id>` records, or report identifiers. The server does not fetch or validate references, and
  references do not make a claim independently verified.
- `next_action`: a concrete recommended next step, or, when `status` is
  `completed`, a clear completion state such as "Task complete; await the next
  objective." It grants no new authority. Completed checkpoints are terminal and
  do not create an open action from this prose.
- `session_id`: the ID from the bootstrap's "This session" line, or another
  real client session ID; otherwise omit. It lets a resumed or compacted session
  keep its own task when several tasks are in progress.
- `predecessor_checkpoint_id`: required once the task has checkpoints: the exact
  current tip you continued (the bootstrap's checkpoint ID for that task). Omit it
  only for a new task. A save that names a stale tip is refused because another
  writer continued the task; read the new tip and reconcile with that work
  instead of retrying blindly. Do not change `task_id` to simulate a fork.
- `request_key`: on servers that expose it, create one opaque key for this exact
  checkpoint attempt and reuse it unchanged for the dry run, the save, and any retry.
  Never reuse the key for revised content.

Use empty arrays or omit optional lists when there is nothing to report.
Keep task and next action nonblank, list entries nonblank, and every list at
64 entries or fewer. The serialized checkpoint limit is 32 KiB; aim for a much
smaller handoff because instructions also consume the bootstrap budget.
Summarize work rather than copying transcripts, raw tool output, stdin,
credentials, or environment dumps. Redaction is pattern-based, not a guarantee.

Before completing a task, check whether the user explicitly approved, rejected,
or deferred any concrete choice during it. Record each with the `decision` skill
and reference it, or state in the summary that there were none. Record a
narrative for each commit with the `narrative` skill. If the task is bound to a
roadmap node, confirm with the `track` skill that its track plan is persisted,
reviewed, and current, and report any `track_plan` findings from
`llewm work check`.

## Lease credentials

A save with `session_id` takes or renews this session's advisory lease on the
task automatically, and a completed save releases it. If the receipt's
`session_lease` says another session holds the task, that agent may still be
working on it: tell the user and coordinate before continuing its work.

When holding an explicit lease for this task, carry its exact `lease_holder`,
`lease_session`, and `lease_token` on the dry run and save. Renew before the
lease's safety deadline; stop protected writes if renewal fails. Never invent a
token, silently drop expired credentials, or acquire unrelated scopes. An
intentionally unleased write omits all three fields and remains subject to the
scope's policy.

## Save and confirm

Before completing a task, call `continuity_checkpoint_actions` with your
`task_id` and reconcile every listed predecessor. Actions belong to the task
whose checkpoint created them; never complete, supersede, or carry another
task's action, which may be another agent's open work. If the call refuses
because several tasks have actions, pass `task_id`; use `all` only to report.
Pass `task_id` to `continuity_checkpoint_action_update` as well. Include `completed` or `superseded` dispositions in
`action_updates` on both the dry run and the save. Put a genuinely unfinished predecessor
in `carry_forward_action_ids` only deliberately; do not use carry-forward to avoid
deciding whether completed work is done. The write is rejected if a completed
checkpoint silently carries same-task work.

Call `continuity_checkpoint` with `dry_run: true` and the exact structured
handoff and action lifecycle fields first. A dry run saves nothing. Continue only when both serialized formats fit the reported budget.
When the dry run returns `research_capture.state: review_recommended`, inspect
the work already done before saving. If it established reusable knowledge, record
the appropriate narrative, decision, resource, or work item, or add missing
evidence and structured attempt/verification detail to the checkpoint. If it did
not establish anything durable, do not create filler merely to clear the advisory;
make that limitation explicit in the checkpoint. `covered` is a structural signal,
not a quality judgment.
If readiness is false, use its section byte accounting to shorten optional detail
while preserving the task kernel, important failed attempts, and stable references;
do not raise the budget or silently discard higher-authority guidance. Dry-run the
revised payload once more. If `dry_run` is rejected because the MCP process is
older, retain the concise-payload discipline below and report that readiness was not
checked before persistence.

After a ready result, call `continuity_checkpoint` once with that same structured
handoff, request key, and action lifecycle fields. Treat it as saved
only when the tool reports success and returns a checkpoint ID. Then call
`continuity_bootstrap` with the explicit task and `full: true` and check that it contains that ID and the essential
task state, attempts, and next action. For a completed checkpoint, call
`continuity_checkpoint_actions` with `task_id` once more and confirm that no stale same-task action
remains. Report the ID and whether readback and lifecycle verification passed.

If validation rejects the write, correct the reported issue and use a new request
key for the revised content. If a response is lost or ambiguous, retry the exact
payload with the same request key; a current server returns the original receipt
without another write. On an older server without request keys, inspect the
bootstrap before retrying to avoid an unnecessary duplicate. Records are append-only. If readback fails
because of size despite a ready dry run, report the mismatch and inspect current
state before writing another append-only record; do not raise budgets or delete
history. If readback shows a different checkpoint, report
the mismatch instead of overwriting another writer's work.

If `continuity_checkpoint` returns `deferred: true`, the checkpoint was not saved,
but the server did preserve the exact validated, redacted structured handoff in a
device-local recovery envelope. This confirmed stale-writer path is part of the
authorized checkpoint operation and does not require a second permission request.
Report the recovery ID, restart the client, run
`llewm checkpoint recovery apply ID`, and require its JSON receipt to confirm
both `readback_verified: true` and `recovery_removed: true`. If apply fails, the
envelope remains pending; inspect `llewm checkpoint recovery list` and do not
discard it unless the user explicitly asks. Do not also create the checkpoint
directly, because that can duplicate the staged append-only handoff.

If MCP is genuinely unavailable after that discovery, prefer restoring the
connection or restarting a stale client. Because losing a required checkpoint is
a continuity failure, a CLI fallback is permitted only when the user explicitly
authorizes it and the current CLI exposes the same structured checkpoint and
action-reconciliation semantics as MCP:

- inspect `llewm checkpoint create --help` and `checkpoint actions --help`
  instead of assuming an old CLI shape;
- reconcile predecessor actions explicitly before the save, or carry genuinely
  unfinished work only when the CLI's behavior is visible in the dry run;
- supply completed work, changes, verification, attempts, evidence references,
  action updates, and carried action IDs through their dedicated CLI flags; never
  collapse structured fields into the summary;
- use `--dry-run`, the exact predecessor, and one request key for dry run/save;
  request the structured save receipt with `--output json`, then read back the
  checkpoint and compile an explicit bootstrap selecting it.

If the installed CLI lacks any required field or receipt detail, do not use it as
a fallback; report the unsaved handoff and restore or restart MCP. A CLI-created
checkpoint is not degraded when it passes the same dry-run, atomic reconciliation,
save-receipt, and readback checks. An older MCP process may need a client restart
after a binary update. A saved checkpoint with failed readback is saved but not
confirmed usable.

Saving a handoff does not execute its next action, install plugins, commit work,
or configure automatic checkpointing. Continue only within the active request.
Do not silently invent a task fork or rekey. When task identity genuinely changes,
use the server's explicit lifecycle operation only when that semantic transition is
part of the authorized work; otherwise report the ambiguity.
