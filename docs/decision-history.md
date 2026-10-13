# Decision history and roadmap nodes

Continuity implements roadmap revisions and bindings, work items, decisions,
typed backlinks, bounded history queries and timeline/console browsing.
These records preserve how work evolved without turning a proposal into an
execution grant.

## Record boundaries

Roadmap nodes have stable IDs independent of wording or bullet order.
Relationships describe hierarchy, dependencies, alternatives and reviewed scope.
Model review watermarks and observed source freshness remain separate.

Work items retain ideas, bugs, tasks and research questions with attribution and
append-only disposition. Acceptance is planning state; it does not assign an
agent or prove implementation. Several records may inform one decision or task
without being merged by prose similarity.

The decision ledger records an explicit human disposition and review source.
It does not change linked task, roadmap, plan or work-item state. Those changes
use their own operations. Typed backlinks retain exact source/revision references;
missing and unverified targets stay visible.
See [decisions and backlinks](../components/continuity/docs/decisions-and-backlinks.md)
and [work items](../components/continuity/docs/work-items.md).

## History retrieval

Narrative claims have stable chunk references, kinds, labels and per-claim authority.
Deterministic search narrows metadata; exact bounded expansion loads selected bodies.
Topics and explicit links organize history without model-inferred membership.

A paginated timeline and read-only console provide views of existing records.
They do not duplicate the canonical history or imply causal order from timestamps.
See [narrative history](../components/continuity/docs/narrative-graph.md) and
[console](../components/continuity/docs/console.md).

## Agent attribution, inspection, and replay

Distinguish the actor performing an action, the client reporting an observation
and the person/model reviewing it. Client/session/model fields are claims with
provenance, not authenticated identity. A model name is not a unique actor.
Unknown identity and missing source artifacts remain explicit.

Playback means browsing recorded facts without running an action.
A future re-analysis workflow would select a bounded fixed evidence packet and
append attributed findings; agreement between models sharing inputs is not
independent evidence. A future re-execution workflow would require separately
authorized isolation, new action/results and links to the originals.
Neither occurs merely by traversing history.

General analysis-run objects, orchestration, semantic ranking and replay execution
remain future designs. The current decision/history interfaces do not require
embeddings, a vector service or a graph database.

## Current-state hygiene

Roadmap lifecycle, task/checkpoint status and plan progress are separate.
Joined [work views](../components/continuity/docs/project-state-hygiene.md)
report exact ambiguities or inconsistencies without repairing them.
A terminal checkpoint does not silently complete the broader roadmap node.

Preserve original observations and append corrections or superseding interpretations.
Current source/runtime evidence verifies current behavior; saved history explains
earlier intent and limits. See [the roadmap](roadmap.md) for product direction.
