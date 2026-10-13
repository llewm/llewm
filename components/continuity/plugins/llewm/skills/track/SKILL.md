---
name: track
description: Keep a roadmap track's plan persisted and current in Continuity. Use when starting work on a roadmap node, when the user approves a plan or slices, after each slice lands, and when a node is completed.
---

# Keep a track plan current

A roadmap node's plan must live in Continuity as a versioned track plan, not
only in a document, a checkpoint summary, or conversation. Use the connected
track-plan and roadmap tools, or the `llewm track-plan` and
`llewm roadmap` CLI; client prefixes vary.

1. **Start.** When work on a roadmap node begins, record the node as `active`
   if it is still `deferred`, `proposed`, or `accepted` (`roadmap node`), so the
   track shows under the current filters. Then record the plan as a revision
   with status `proposed`, naming the task and the node: that binds the task to
   the node. One item per slice from the node's contract, with stable IDs,
   positions, dependencies, and details. A model-created plan must name the
   model and stays `proposed`.
2. **Review.** Show the user the plan. Only after the user explicitly approves
   it, record the review with `continuity_track_plan_record` without items (CLI
   `track-plan record PLAN --creator <user>`) as `accepted`, or `active` once
   work starts. Never accept on your own judgment; if the user changes the plan,
   record a revised proposal first.
3. **Progress.** When a slice lands, append a revision that marks its item
   `completed`, with evidence such as `commit:<hash>`, `narrative:<id>`, and
   `decision:<id>`. Add or supersede items when the plan changes instead of
   editing documents alone. A changed plan needs review again.
4. **Finish.** Complete the node only when every item is `completed` or
   `superseded`, and record a plan revision with status `completed` in the same
   step.

Check `llewm work check` for `missing_track_plan`, `track_plan_unreviewed`,
and `track_plan_behind`, and resolve them before ending the task. Plans record
intent and progress; they never select, schedule, or authorize execution.

For a held lease on the write scope, pass the exact `lease_holder`,
`lease_session`, and `lease_token` to plan writes and human-review writes.
Renew before the safety deadline; stop if renewal fails. Do not fabricate tokens,
acquire unrelated scopes, or omit credentials to evade fencing. If deliberately
unleased, omit all three fields; the server enforces the scope's policy.
