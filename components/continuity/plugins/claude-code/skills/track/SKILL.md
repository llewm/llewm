---
name: track
description: Keep a roadmap track's plan persisted and current in Continuity. Use when starting work on a roadmap node, when the user approves a plan or slices, after each slice lands, and when a node is completed.
---

# Keep a track plan current

Persist plans in Continuity, not only in files or conversation. Use the connected
track-plan/roadmap tools or the CLI. Read the full current revision before writing
and supply its exact `base_revision`; stale autonomous writes are refused. Read
`continuity_track_plan` with `policy: true` (or `track-plan policy --node ID`) to
inspect the effective delegation. If a proposal is pending, use the list read's
`effective_revision` to load operative intent for progress, while acknowledging
the latest revision with `base_revision`. Do not adopt proposed scope as progress. Keep
model identity on every model-authored revision; never impersonate human review.

1. **Start or revise intent.** Record the node active when work starts. Preserve
   stable item IDs, dependencies, scope (`detail`), and `acceptance_criteria`.
   New plans and substantive changes default to proposals. The configured
   `[plan_policy]` can delegate them globally or for particular tracks, with
   bounds on adding/removing work, ordering, dependencies, scope and criteria.
   Record your rationale in the summary. Treat policy changes as a separate user
   choice; never grant yourself delegation by editing configuration.
2. **Review when required.** Use the server's `plan_change` receipt and the read
   to distinguish progress, delegation, and a pending proposal. Show a substantive
   proposal and obtain explicit approval before recording a human review without
   items. Delegated changes need no extra review within the configured bounds.
   A proposal leaves the last operative baseline available to readiness reads.
3. **Record progress autonomously by default.** Keep intent fields unchanged;
   update item status, `progress_note`, and add evidence. Do not append progress
   history to `detail` or change acceptance criteria to declare success.
   Completion requires an attributable note explaining satisfaction of the
   criteria and evidence such as `commit:HASH` or `narrative:ID`. This is your
   observation, not independently verified truth. Default progress retains the
   baseline's accepted/active state without another approval prompt. A configured
   `progress = "review"` requires review even if intent is unchanged.
4. **Finish.** Complete the plan/node only when every item is completed or
   legitimately superseded. Superseding work changes intent and follows policy.
   Readiness does not authorize executing a newly unblocked action.

Inspect `llewm work check` and pending proposals. Resolve missing/behind plans
without fabricating a review. History preserves all previous revisions and their
parent/baseline/policy metadata; inspect `track-plan show ID --revision N` for
past intent. Plans never select, schedule, execute, or authorize actions.

Older installed writers may enforce proposal-only model revisions. Report that
runtime limitation and prepare an upgrade; do not omit model identity or invent
human approval to bypass it. The source skill does not upgrade a running server.

Carry exact credentials for a held scope lease; renew before expiry and stop
protected writes if renewal fails. Deliberately unleased writes omit credentials
and remain subject to the scope policy. Do not acquire unrelated scopes.
