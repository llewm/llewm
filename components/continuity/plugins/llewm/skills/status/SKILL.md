---
name: status
description: Report the saved Continuity task, checkpoint, and next action when the user asks where work stands. Does not resume work or provide an activity/cost audit.
---

# Show saved task status

Call the connected Continuity server's `continuity_bootstrap` once for the latest
saved state, selecting the requested task when known; tool prefixes vary by client.
An unchanged-packet notice means the SessionStart packet is still current. Use
that packet if it remains in context; otherwise fetch with `full: true`, including
after compaction. Do not treat a deduplication notice as a missing checkpoint. There is no separate MCP status tool.
Confirm the returned project matches the user's current project. If it does not,
report the mismatch without modifying configuration or writing a checkpoint.

Summarize the checkpoint ID and recorded time, task/status, completed work, next
action, important failed attempts or blockers, and verification claims. Include
only fields present and relevant. Distinguish caller-reported verification from
independently checked results; evidence references are not automatically verified.
If no checkpoint exists, say so. If the bootstrap says "Task selection
needed", list the in-progress tasks it names and fetch the one the user means
with `task_id`; do not report one of them as the current task. A completed task needs no invented next task.

This is saved-state status, not a live repository review. Do not scan files, run
verification commands, or call audit to fill missing fields unless the user also
requests those checks or governing instructions require them. Do not execute the
next action, save a replacement checkpoint, commit, or install anything as part
of a status request.

If MCP retrieval fails, report the error. You may summarize an injected bootstrap
as previously supplied context, explicitly stating that its freshness could not
be confirmed. Do not present cached context as a successful live retrieval or
invent a status from unrelated project documentation.
