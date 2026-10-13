<!-- continuity:roadmap-node continuity-narrative-graph -->
# Searchable narrative graph

Continuity implements deterministic history search, bounded exact expansion,
curated topics and typed relationships. Work narratives are the primary semantic
records. The [Git-note integration](../../../docs/federated-memory.md#work-narratives-and-git-note-cutover)
is an explicit selected import or commit-note rendering path.

## Claim chunks

Each narrative claim has a stable `narrative:ID#ORDINAL` reference, kind, optional
label, text, authority and content hash. The payload remains canonical; derived
claim/index rows are maintained with its write. Kinds include intent, outcome,
constraint, alternative, verification, decision, next_action, limitation, interface
and note. A claim label preserves its heading without changing its authority.

Exact expansion can load one claim rather than an entire narrative.
File/region anchors retain source hashes and report current/missing/drifted state.
Recorded-time and occurrence-time fields remain distinct.

## Topics

Roadmap-node topics include their `parent_of` descendants.
Curated topics have stable IDs, append-only revisions, bounded summaries and aliases.
Explicit proposed/accepted links join supported records; model-created links remain
proposed until reviewed. Current targets include roadmap nodes, narratives/claims,
decisions, work items, plans, tasks, checkpoints, commits and files.

Membership names its derivation: explicit link, node anchor, task binding or
exclusive file binding. Shared paths do not automatically place every commit under
every topic. These are deterministic stored relationships, not similarity-inferred
meaning or ownership.

## Search index

The rebuildable FTS5 index stores bounded searchable chunks from narrative claims,
decisions, work items, checkpoints, procedures and resources. Exact references,
family/kind, title and time support selective retrieval.
`llewm history reindex` rebuilds derived rows from canonical records.

Raw transcripts, retired cards and separate evidence records are not current
history families. Topics and their explicit links travel through the curated
portable profile; derived search indexes are rebuilt locally.

## History queries

```sh
llewm history search "build cache" --limit 5 --max-bytes 3000
llewm history show narrative:NARRATIVE_ID --max-bytes 6000
```

MCP `continuity_history_search` and `continuity_history_show` provide typed
equivalents. Search supports text, topic, kinds/families, linked references and time
filters within explicit count/byte bounds. Text uses lexical ranking; without text,
history is chronological. Results return metadata/snippets and zero body loads.
Counts, offsets and truncation distinguish partial results from exhaustion.

Show expands up to 20 exact references within a byte budget, preserving labels,
authority and source links. Unsupported or unresolved references fail honestly.
History explains prior intent; verify it against current source/runtime evidence.

## Git-note import

`llewm narrative import-git-notes --plan` reads selected Git notes without
semantic writes and returns an exact plan hash. Apply recomputes that plan and
requires `--apply --plan-sha256 HASH`; changed input is refused.

The importer maps recognized headings to typed claims, preserves original labels
and caller-asserted authority, and anchors the commit and bounded changed paths.
Already imported unchanged notes are idempotent; changed notes append superseding
imports. A commit already directly narrated is reported so import does not duplicate
its meaning. Unparseable notes remain visible rather than becoming invented claims.

This is a narrow historical source, not bulk semantic reconstruction of every
pre-adoption commit. Use command help for the selected range and filters.

## Capture and navigation

Record a concise work narrative after the project's chosen meaningful boundaries,
including explicit verification and limits. Record ledger decisions only for
explicit human choices. Checkpoints keep the small resume state and references;
they do not replace detailed narratives or become research transcripts.

The [console](console.md) browses work items, topics/history and exact references
through the same bounded reads. Browsing history does not select a task.
See [work narratives](work-narratives.md),
[decisions/backlinks](decisions-and-backlinks.md) and
[retrieval qualification](evidence-qualification.md).
