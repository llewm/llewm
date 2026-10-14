# Plan progress and agency

Progress and changes to intent have different review rules. By default an agent
can update status, add evidence and record `progress_note` on an operative plan
without another approval. Its model identity remains recorded. The revision keeps
the approved baseline's accepted/active status; it is not a fabricated human review.

Changing scope (`title`/`detail`), acceptance criteria, ordering, dependencies,
parent relationships, or the set of work items changes intent. Those revisions
default to proposals. Removing or superseding work also changes intent.
A pending proposal does not displace the last operative plan for readiness reads.

`acceptance_criteria` is planned intent; `progress_note` explains observed progress.
Completing an item requires a note and evidence references explaining satisfaction
of the criteria. These are attributable assertions, not independent validation.
Completion may unblock dependencies, but never grants permission to execute them.

## Configure delegation

The engineer's global `config.toml` and the project's `.continuity/config.toml`
support `[plan_policy]`. Project settings override global defaults; exact roadmap
node IDs can override the default rule. Configuration is local policy, not a
portable execution grant. Agents must not change it to grant themselves agency
without an explicit user instruction.

Defaults need no configuration:

```toml
[plan_policy]
mode = "review"
progress = "auto"
```

Allow an agent to add work and adjust dependencies on one track, while keeping
scope, ordering, removals and acceptance criteria reviewed:

```toml
[plan_policy.tracks."continuity-distribution-console-ide"]
mode = "bounded"
allow = ["add_items", "dependencies"]
```

The supported substantive change types are `create_plan`, `add_items`,
`remove_items`, `reorder_items`, `dependencies`, `scope`, and
`acceptance_criteria`. Every detected type must be allowed; a mixed revision
does not escape review because one part is delegated. Invalid policies fail
closed. Scope includes task/node bindings, provenance work-item sets, and removal
of existing evidence. Adding evidence is progress.

For unrestricted **planning** delegation:

```toml
[plan_policy]
mode = "agent"
```

Set `progress = "review"` to require progress review too, independently of the
substantive delegation mode. Per-track fields inherit unspecified global/project
fields; an explicit empty `allow = []` clears inherited bounds.

## Record and inspect

Inspect effective policy with `llewm track-plan policy --node NODE_ID`, or
MCP `continuity_track_plan` with `policy: true` and `roadmap_node_id`.
Read the full plan before updating it. Supply the exact latest revision as
`base_revision`; autonomous writes with missing or stale baselines are refused.
Use `model` on every agent-authored write. No policy makes it acceptable to omit
model identity or name the person as creator for an agent observation.

Send the full item set through `continuity_track_plan_record`, or JSON to
`llewm track-plan record --file PATH`. Keep `detail` unchanged for progress;
put the result in `progress_note` and add `evidence_refs`. A proposed input that
qualifies as autonomous progress inherits the operative status. A permitted
substantive revision becomes active and is marked delegated. Otherwise it stays
proposed; claiming an accepted/active substantive revision outside policy is refused.
When a proposal is pending, list reads identify `effective_revision` and
`effective_status`. Load that operative snapshot for progress and acknowledge
the latest revision with `base_revision`. Progress cannot adopt unreviewed intent.

Receipts and reads include `plan_change`: parent, comparison and approved baseline revision, change
kind/categories, and the governing policy snapshot. The authorship authority
remains `model_suggested` for model-authored records; disposition is expressed
separately by status and change kind. History is append-only. Inspect an earlier
snapshot with `llewm track-plan show ID --revision N --detail`; intent restored
from an earlier snapshot still follows the current review policy.

Portable sync preserves progress/delegation metadata and validates it against
the parent revision. Imported policy snapshots describe historical decisions;
they do not change the receiving machine's policy or authorize execution.
Existing records retain their original portable payloads/hashes.

## Upgrade boundary

This interface requires MCP/writer API 121 and database migration
`075_plan_agency.sql`. Older writers cannot apply these semantics. Follow the
[backup-gated upgrade](installation.md#migration-bearing-updates), restart the
client, and verify the running MCP identity. Updating source or skills does not
upgrade an already-running server. No live store or client is upgraded merely
by implementing this feature.
