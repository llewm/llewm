# Decisions and backlinks

A decision is an attributable human disposition of a concrete choice. It is not
a narrative interpretation, work item, roadmap transition, or execution grant.
Do not infer approval from work starting or from a plan becoming ready.

## Record boundary

A project-scoped immutable decision contains a stable ID, bounded title/rationale,
disposition, actor, authority, recorded time, and optional predecessor.
A correction appends a new decision naming the one it supersedes.
User-reported authority requires an exact review source; the service cannot
independently authenticate the actor's claim. Redact before persistence.

Links are typed: `subject`, `supports`, `opposes`, `resulted_in`, and
`supersedes`. They identify exact target kinds/IDs and optional revisions.
Missing or unverified targets stay visible. A link does not change its target,
instruction authority, task selection, plan readiness, or execution policy.

## Reads and capture

`llewm decision record --file FILE` accepts a bounded JSON object; MCP
`continuity_decision_record` accepts typed fields. Search and exact expansion use
`llewm history search --family decision` and
`llewm history show decision:ID`.
Use `--help` for linked-target filters and output selectors.

Backlinks query exact stored relationships in four families:

| Family | Indexed relationships |
| --- | --- |
| Decision | Typed decision links |
| Narrative | Task, checkpoint, roadmap-node and commit anchors |
| Work item | Unversioned roadmap-node, task and checkpoint context |
| Track plan | Unversioned roadmap-node/task scope and exact work-item membership |

Retired card and suggestion record families are not current backlink sources.
Prose similarity and timestamp proximity do not create links.
Decision targets can report resolved or missing; other relationships are labeled
unverified rather than claiming validation of the target's meaning.

A revision filter matches exact target revisions. Without one, matching stored
revision links remain visible and each row retains its revision.
Counts and offset pages come from one read-only transaction, load no source
bodies, and use a stable order on an unchanged database. Pages are not frozen
across concurrent writes. Included families and truncation make omissions explicit.

Decisions and their ordered links travel through the current curated portable
profile. Import preserves attribution; it does not enact the recorded decision.
See [portable memory](portable-memory-contract.md),
[history retrieval](narrative-graph.md), and [console](console.md).
