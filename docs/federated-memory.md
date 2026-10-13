# Federated project memory and retrieval efficiency

Continuity keeps operational state local and moves curated immutable records
through explicit transport boundaries. This guide describes current behavior and
separates it from proposed retrieval and transport layers.

## Local stores and shared history

The Local tier uses the project store as the operational source. With optional
History sync, a dedicated memory Git repository carries the shared curated record
stream; local SQLite projects materialize that history alongside device-private
events and usage. Sync is configured explicitly and then follows its documented
automatic lifecycle. It does not publish memory through the source repository.

A machine-local registry routes project/workspace identities and store locations.
It does not hold all project bodies or act as a global semantic database.
The old portable metadata catalog and its CLI were removed.
Cross-project semantic search is not implied by registry routing.

See [multi-repository projects](multi-repo-and-sync.md),
[portable memory](../components/continuity/docs/portable-memory-contract.md), and
[memory repository rules](../components/continuity/docs/memory-repository.md).

## Identities and integrity

Keep lineage, local project/instance, record, actor/source and transport identities
distinct. Lineage association is explicit; a matching directory or remote name
does not authorize joining histories. Portable IDs are scoped by lineage/type/
revision and protected by content/envelope hashes.

Same identity and content is idempotent. Conflicting content is a visible collision
or fork requiring the record family's explicit resolution path. Import must not
silently overwrite observations or choose a winner from timestamps alone.

## Portable record stream

Current complete bundles contain a canonical manifest and ordered record stream
with version/profile identity, counts, hashes, references and omissions.
Validation precedes semantic writes. Transactional apply preserves attribution and
dependencies; replay adds no semantic records. Device-local fields are remapped
without inventing approval or source verification.

Incremental export watermarks, separately stored large evidence bodies and
authenticated object/peer transports are future designs, not current bundle
features. Exact SQLite backup is disaster recovery, not the collaboration format.

<!-- continuity:roadmap-node continuity-git-note-cutover -->
## Work narratives and Git-note cutover

[Work narratives](../components/continuity/docs/work-narratives.md) preserve typed,
attributable claims at meaningful boundaries; commits are optional anchors.
Recording a commit-linked narrative also renders its Git note unless explicitly
skipped. This is the current integration, not a pending policy cutover.

Historical Git notes can be imported through an explicit plan/hash/apply workflow.
The importer is a narrow selected source, not automatic reconstruction of knowledge
from every commit. Imported claims retain their provenance and must be verified
against current source before being used as current facts.

Policies about when to narrate or attach notes remain the engineer/project's
instructions. A storage design document does not replace those policies.

## Transport profiles

| Operation | Purpose | Boundary |
| --- | --- | --- |
| Database backup | Exact consistent operational snapshot | Private recovery artifact, integrity checked |
| Local portable bundle | Curated selected-profile transfer | Explicit export, inspect, plan and apply |
| Git-reviewed export | Inert reviewable snapshot | Produces files, performs no Git/network actions |
| History sync | Managed shared curated history | Explicit setup; documented lifecycle and authentication |

Current Git-reviewed exports and History sync use their respective format
identities; neither exposes the live database. Do not conflate an inert export
with automatic sync or source publication.
See [Git-reviewed memory](../components/continuity/docs/git-reviewed-portable-memory.md).

## Multi-resolution retrieval

The automatic bootstrap delivers a compact task kernel and applicable guidance.
Detailed checkpoints, claims and evidence expand through stable references and
bounded queries. Mandatory guidance and optional detail have separate byte
accounting; an overflow must not silently weaken instruction precedence.

History search narrows deterministic metadata before exact bodies are expanded.
Derived search indexes can be rebuilt from canonical records. A historical claim
is evidence of earlier intent, not instruction authority or proof of current state.

Future model-generated summaries, semantic ranking, embeddings and high-volume
source archives require explicit privacy, provenance, retention and evaluation
contracts. None is required by the current deterministic reads.

## Model and effort attribution

Actor invocations preserve client/model identity and separately reported requested
or effective effort when available. Unknown values remain unknown; client claims
are not authenticated identity. Never infer effort from model name, response
quality, latency or reasoning-token counts.

Usage tokens, estimated costs and byte-derived estimates stay separately labeled.
Raw usage rows remain device-local; curated completed-session summaries can sync.
See [usage](../components/continuity/docs/usage.md).

## Discovery friction

Repeated tool failure/recovery shapes are available through
[work-item friction reads](../components/continuity/docs/work-items.md#repeated-friction).
They do not establish repeated discovery or an avoidable cause.

A future discovery report would need bounded stable-source/hash references, counts,
bytes and session boundaries without retaining prompts or tool bodies.
Fresh verification can be necessary even when a file is unchanged.
Measure task correctness and maintenance cost alongside usage; fewer reads alone
are not evidence of efficiency. Current source fixtures explore grouping without
establishing live collection or causal savings.

## Authority boundaries

Repository guidance and canonical Markdown contracts retain their own authority.
Roadmap nodes, accepted plans, task lineages and decisions are distinct saved
records. Read-only projections do not activate instructions or execute work.
The former roadmap authority modes and plan/generate/activate interfaces were
removed before release; they are not setup steps.
