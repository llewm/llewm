<!-- continuity:roadmap-node continuity-project-state-hygiene -->
# Project-state hygiene and current-work views

Current-work reads compare saved records without repairing them or selecting a
new task. Task lifecycle, roadmap status, plan-item state, work-item disposition,
and observed Git changes describe different facts. A mismatch needs reviewed
interpretation, not an automatic choice of which record is correct.

## Read surfaces

```sh
llewm work
llewm work --summary
llewm work check --scope current --output json
llewm work list --view next
llewm work show task:TASK_ID
llewm work inventory
```

MCP `continuity_work` provides the matching bounded views. CLI `--help`
describes the filters, limits, and supported output for each read.
`node:` and `task:` prefixes disambiguate exact detail. Several active tasks
may require explicit selection; an inventory is not an assignment.

## Authority by question

| Question | Source |
| --- | --- |
| What work should this session resume? | Selected task's checkpoint and explicit user direction |
| What is planned? | Roadmap and reviewed track-plan revisions |
| What remains unresolved? | Same-task checkpoint actions |
| What was proposed? | Work items and their attributable dispositions |
| Why was a choice made? | Decisions and bounded narrative history |
| What changed in source? | Git and observed file-change provenance |
| What happened historically? | Append-only records and observations |

Repository guidance retains its instruction authority. A generated view, contract,
historical claim, or suggestion does not change instruction precedence.

## Findings

Findings name exact records or files and report deterministic comparisons:
missing or ambiguous bindings, stale plan evidence, terminal tasks with open
actions and roadmap/task lifecycle differences.
They do not complete a plan item, bind a task, disposition a work item, revise a
roadmap, or repair lineage. Preserve their authority and truncation when reporting.

A completed task can legitimately leave a broader roadmap direction unfinished.
Missing pre-adoption semantic history does not imply that earlier work never
happened. Keep original observations and append attributable corrections.

## Optional current-plan projection

`llewm track-plan show --render` emits a deterministic view of latest
accepted/active plan revisions with source-snapshot and body hashes.
`llewm track-plan check --path PATH` compares exact bytes without writing.
The default comparison path is
`components/continuity/docs/current-plan.md`; it is an optional informational
artifact, not a required public source file or an instruction source.

Projection checking is opt-in: run `llewm track-plan check` explicitly when an
export needs verification. Ordinary work reads, including `work check`, do not
probe this file or report its absence or drift. This repository ignores its own
generated current-plan file to avoid publishing private task state. It does not
ship a frozen maintainer plan as public guidance.

## Lifecycle and console

Hooks record observations and may surface explicitly enabled bounded advisories.
They do not infer semantic task completion, revise a plan, or execute a next action.
Checkpoint action reconciliation and strict predecessor checks protect saved
handoffs independently of a client's memory of the workflow.

The [console](console.md) browses the same bounded saved state, with explicit
navigation and a CLI fallback. Browsing a checkpoint does not resume it.
See [CLI output](cli-output-contract.md), [track continuity](track-continuity.md),
and [roadmap control](roadmap-control.md).
