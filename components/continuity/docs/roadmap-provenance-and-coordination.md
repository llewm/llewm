<!-- continuity:roadmap-node continuity-roadmap-provenance -->
# Roadmap completeness, change provenance, and agent coordination

Continuity implements bounded graph coverage, task/plan joins, narrative source
anchors, expected-versus-observed file scope, milestone rollups and reviewed
release projections. Provider adapters and richer cross-agent overlap analysis
remain separate future work.

## Three distinct source relationships

- Expected scope is a reviewed roadmap binding to a file or directory.
- Observed change is attributable file/region evidence from a work episode.
- Semantic explanation is a narrative claim about why the work mattered and
  how it was verified.

A touch does not establish ownership, correctness, completion or user-visible
impact. Expected scope can precede implementation; observed changes can include
incidental or reverted work. Keep these sources separate.

## Graph and work reads

Roadmap overview, tree, coverage and exact history expose versioned nodes,
relationships, bindings and explicit omissions within bounded reads.
Task/checkpoint/plan state joins through [work views](project-state-hygiene.md);
it is not copied into another queue or inferred from roadmap prose.

Model-origin proposals are [work items](work-items.md), not a separate suggestion
store. Explicit reviewed plans, task bindings and strict checkpoint lineage
preserve intent and task identity. The former roadmap authority modes,
reconciliation planner and interruption/return ledger were removed.

## Scope coverage and source anchors

The coverage view compares a node revision's expected file/directory bindings with
file anchors of current narratives that also anchor that node.
Observed paths are bounded at 64 per returned node; directory bindings match
descendants. Missing expected touches and out-of-scope observed touches are
candidates for review, not diagnoses.

Source-region and symbol locators retain the producer's content hash and original
locator. A later inferred relocation must be labeled separately; it cannot rewrite
the historical anchor. File-level evidence remains necessary for documentation,
configuration, migrations and broad refactors.

Broad automated symbol/semantic overlap analysis is not implied by the file-level
coverage implementation. Verify the observed language and source bounds before
claiming a richer scope.

## Milestones and reviewed releases

Roadmap node kinds are initiative, milestone, work_item and research.
Caller-asserted target date/release/horizon and up to 64 exact evidence references
can accompany revisions. Milestone coverage reports direct-child status counts,
blockers and evidence; it does not infer effort or a subjective completion percentage.

Release revisions select exact completed roadmap-node revisions and work narratives
with bounded category/wording. A published record must match its latest reviewed
content hash. The deterministic Unreleased/changelog render and check surfaces are
read-only and do not publish externally or write a changelog file.
A release entry is a reviewed explanation, not a dump of every internal commit.

Dependency-ready reporting uses existing plan-ready and work-next views.
Ready state does not assign, schedule or authorize work.

<!-- continuity:roadmap-node continuity-pm-adapter-contract -->
## Provider-neutral project-management integration

The current [read-only interchange](pm-adapter-contract.md) inspects one explicitly
selected local snapshot/proposal packet against independently supplied provider/
instance/project identity. It validates and sanitizes the packet without opening
a project database, applying records, fetching pages or writing to a provider.
Schemas, fixtures and a standalone conformance runner qualify that boundary.

A future out-of-process adapter would own provider SDKs, credentials, authentication,
network delivery, version mapping and provider-native fields. Continuity would
consume bounded canonical observations/proposals through versioned interfaces.
Provider truth and proposed field authority remain attributed claims.

Future plan/apply/reconcile operations must preserve source revisions, ownership,
lossy mappings, incomplete coverage, deletion/tombstones, stale preconditions,
idempotency, retries and partial failures. Capability discovery is not permission.
Bidirectional writes and autonomous issue synchronization are not current features.

Prefer a small observed workflow before implementing an adapter. Do not load
third-party provider code or credentials into Continuity core.

<!-- continuity:roadmap-node llmem-agent-work-presence -->
## Advisory agent presence

Current device-local presence records short-lived session/task/worktree activity,
expires on reads and yields at supported lifecycle boundaries.
Leases separately express advisory scope claims, with optional project-required
write fencing. Neither proves that a process is alive or reserves files.

The roadmap marker above retains its original stable identity; it is not an old
executable or plugin name. Richer roadmap/path/symbol work-presence claims and
cross-client overlap traversal remain proposed. A future classifier should report
observed or possible overlap with evidence, never guarantee merge safety.

Continuity may recommend sequencing or isolated worktrees, but it does not create
them, block edits, schedule agents or merge branches. Stronger isolation belongs
to a separately authorized host or broker.
See [parallel tracks](../../../docs/parallel-tracks.md).

## Qualification and non-goals

Use isolated fixtures for bounds, redaction, attribution, stable ordering, ambiguity,
replay and exact source drift. Provider conformance and genuine concurrent-client
trials require their own versioned observations.
Structural tests do not qualify broad client delivery or native platform behavior.

No read automatically completes a roadmap node, publishes a release, edits sources,
assigns work, launches agents, invokes a model or grants execution permission.
