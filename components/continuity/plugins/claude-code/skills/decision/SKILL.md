---
name: decision
description: Record an explicit user decision in the Continuity decision ledger. Use only when the user explicitly approves, rejects, or defers a concrete choice, or asks to record a decision; never infer one from work starting or a plan being ready.
---

# Record a ledger decision

The decision ledger holds attributable, reviewed dispositions of concrete choices.
It is not a narrative, work item, roadmap transition, or execution grant. Use the
connected `continuity_decision_record` tool; client prefixes vary.

Record a decision only when the user has explicitly approved, rejected, or
deferred a specific choice in this conversation, or asks you to record one. Do not
infer decisions from silence, from starting work, from plan readiness, or from your
own implementation choices; those belong in narrative `decision` claims.

For each decision:

- Use a stable, descriptive ID and a title that states the choice.
- Put the rationale in the user's terms; summarize faithfully without adding
  reasons the user did not give.
- Set `disposition` to `accepted`, `rejected`, or `deferred`, `actor` to the
  user's identity, `actor_type` to `human`, `authority` to `user_reported`, and
  `review_source` to an exact source such as the client session or checkpoint.
- Link exact targets with `subject`, `supports`, `opposes`, or `resulted_in`:
  roadmap nodes with revision, tasks, checkpoints, commits, track plans, work items,
  narratives, or earlier decisions.
- To change a decision, record a new one with `supersedes_id`; never rewrite it.

A decision changes no linked record's state. Perform any roadmap, plan, or task
change through its own explicit operation. Report the recorded IDs, and include
them in the task's checkpoint `evidence_refs`.
