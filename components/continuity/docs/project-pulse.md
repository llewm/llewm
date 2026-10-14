# Deterministic project pulse

`llewm work` returns saved checkpoint state, deterministic roadmap freshness and
bounded structural routing. It invokes no model, repairs no records and does not
reevaluate repository files for meaning. A recommended next action is a saved
claim or routing suggestion, not authority to execute it.

Explicit checkpoint/task/session selection wins. Without one, the pulse retains a
nonterminal structurally current project tip, or inspects resumable tracks.
One candidate can be recommended; several require selection; none produces
`no_candidate`. This pulse routing is distinct from the bootstrap's session-aware
selection rules. Neither invents task intent from activity.

Checkpoint and roadmap state retain their reported authority. Current Git and
bound-file observations are separately machine-observed. Node reads are capped at
20; routing candidates at 10, with counts and truncation explicit.

```sh
llewm work
llewm work --output json
llewm work --task-id TASK_ID
llewm work --session SESSION_ID
llewm work --summary
```

Multiple selectors are rejected; a selector with no match does not substitute an
unrelated task. `--summary` provides the joined work/plan/finding view.
MCP `continuity_work` exposes the corresponding work views.
See [track continuity](track-continuity.md), [checkpoints](checkpoints.md) and
[current-work views](project-state-hygiene.md).

Plan progress and substantive revisions follow [plan agency](plan-agency.md):
progress is autonomous by default; configured delegation controls intent changes.
Readiness remains separate from action authorization.
