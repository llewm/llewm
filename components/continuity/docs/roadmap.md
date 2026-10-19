# Continuity roadmap

Continuity is an implemented preview and the entire current Llewm MVP.
This page summarizes product boundaries and remaining qualification; exact
maintainer plans and task state belong in Continuity records.

## Implemented foundation

The component provides local Go/SQLite storage, attributable lifecycle observations,
bounded bootstraps, explicit checkpoints, work narratives, decisions, work items,
roadmap and plan reads, usage auditing, optional curated History sync, and a
read-only console. No daemon, cloud account, or model is required.

Start with [onboarding](onboarding.md), [installation](installation.md), and
[client integration](client-integrations.md). The [architecture](architecture.md)
describes the storage and interface boundaries.

<!-- continuity:roadmap-node continuity-switching -->
## Productive switching

Historical Linux trials met the initial engineering gate: two clean productive
handoffs in each of Codex-to-Codex, Claude-to-Claude, Codex-to-Claude, and
Claude-to-Codex. Repaired attempts were excluded from the clean count.
Those results qualify their recorded versions and modes, not current-client
reliability or every platform. Detailed trial records remain in the private
development archive.

The durable boundary is a structured checkpoint with exact task identity,
predecessor comparison, action reconciliation, and readback. See
[checkpoints](checkpoints.md) and [track continuity](track-continuity.md).
Broader repeat reliability and fresh-client qualification remain separate work.

<!-- continuity:roadmap-node continuity-compaction -->
## Compaction and recovery

Lifecycle hooks observe compaction boundaries; explicitly saved checkpoints
remain the recovery source. The client may discard transient conversation context.
Continuity does not replay the whole conversation, preserve hidden reasoning, or
automatically manufacture semantic checkpoints. Qualify exact recovery fields and
event delivery separately in each client mode.

<!-- continuity:roadmap-node continuity-automatic-preservation -->
### Preservation boundary

Automatic semantic checkpoint creation is not implemented. Optional reminders
suggest capture, and bounded history reads recover saved facts on demand.
Hook events are observations rather than proof that the agent understood the
context. See [client-hook conformance](client-hook-conformance.md).

## Concurrent tasks and projects

Stable task lineages, explicit selection, project/workspace identity, advisory
presence, and leases support coordination. Several saved tasks or unexpired leases
do not prove multiple writers. Use isolated worktrees for actual concurrent work.
Continuity does not create worktrees, schedule agents, reserve files, or merge work.
See [parallel tracks](../../../docs/parallel-tracks.md) and
[multi-repository projects](../../../docs/multi-repo-and-sync.md).

<!-- continuity:roadmap-node continuity-auditing -->
## Activity economics and procedures

Usage reports distinguish provider-reported tokens, estimated API-rate cost,
unpriced models, and byte-derived estimates. Audit-only baselines are available
without model-visible Continuity context. Reviewed procedures remain inert records;
mining or storing a recipe does not execute it. See [usage](usage.md) and
[resources and runbooks](resources-and-runbooks.md).

## Portable memory and current-work views

Curated portable records and optional History sync are separate from exact SQLite
backups and device-private usage/events. Format conversion is explicit and preserves
original observations and shared memory Git history.
See [memory repository rules](memory-repository.md).

Current-work checks join saved task, roadmap, plan, and action state without
repairing it. Canonical Markdown contracts remain repository sections.
See [project-state hygiene](project-state-hygiene.md),
[roadmap control](roadmap-control.md), and [contracts](contracts.md).

## Remaining delivery work

Complete source documentation cleanup before any clean-root creation or publication.
Then qualify remote pinned installs, native Windows/macOS client and console
behavior, and fresh-session onboarding. Cross-compilation is not native acceptance.
See [preview distribution](preview-distribution.md), [console](console.md),
[coverage](coverage.md), and [operational readiness](operational-readiness.md).

Provider adapters, ECC integration, orchestration, generic execution, and local
models remain deferred. See the [Llewm roadmap](../../../docs/roadmap.md).
