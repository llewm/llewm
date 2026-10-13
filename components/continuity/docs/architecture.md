# Architecture

## Design

The system is an append-oriented activity ledger with derived continuity views.

1. Client hooks send lifecycle and tool events to `llewm hook`.
2. Client adapters translate provider payloads into canonical events.
3. Redaction runs before persistence.
4. SQLite stores canonical observations, task/checkpoint state, curated records and
   device-private usage. Repository/global guidance remains in its source files.
5. The bootstrap compiler selects a small current-state packet with stable references
   for deeper retrieval.
6. Hooks use lifecycle boundaries to inject or request that bounded packet, report
   freshness, and record payload-free observations where client behavior permits.
7. MCP exposes deterministic query surfaces for targeted expansion beyond the
   bootstrap.

Future host adapters, including the bounded AHP direction in the project-level
[agent host security design](../../../docs/agent-host-security.md), may supply
additional ordered session, tool, terminal, and changeset observations. They remain
provider adapters behind the canonical event boundary. A host event is evidence
that Llewm observed that path, not proof that every agent action was captured.
Continuity does not execute or block host actions.

The detailed-evidence tier is [work narratives](work-narratives.md): typed,
append-only claims anchored to commits, files, tasks, and checkpoints, kept
outside automatic bootstrap loading. Separate commit-narrative evidence records
were cut in 6e4b (migration 057).

Work items form an append-only proposal tier. Capturing or reading an idea
does not change the active checkpoint or roadmap. Metadata-only list and bounded
audit support deliberate review. An explicit `accepted` transition distinguishes
endorsed planned direction from completed integration; a later terminal disposition
is appended without rewriting the source idea or acceptance. Idea bodies are never
automatic bootstrap content.

Contracts are marked canonical sections of repository Markdown. A device-local
index records accepted section hashes so reads can report drift. The former
governed-contract record family and authority activation were removed. A contract
statement does not prove conformance or authorize execution. See [contracts](contracts.md).

ECC interoperability is a proposed external-adapter boundary. It would treat
external memories/handoffs as unreviewed proposals; no ECC adapter is shipped.
Continuity's structured checkpoint/task state remains the continuation source.
See [ECC interoperability](ecc-interoperability.md).

Repeated-friction candidates are derived read-only from a bounded window of
canonical tool observations. They group a redacted failure shape with a later
successful same-family retry in the same session, retain representative event
references, and keep cause explicitly unknown. Only user review dispositions are
persisted append-only. Candidate reads and dispositions never apply a workaround or
promote a procedure; see [the repeated-friction contract](work-items.md#repeated-friction).

Checkpoint delivery follows the same resolution model. The automatic startup tier
is a compact task kernel; completed work, changes, verification, attempts and
detailed evidence expand by stable reference. Dry-run and save compile the real
packet and separately account for mandatory guidance, kernel and optional detail.
They do not solve overflow by silently dropping guidance or raising the budget.

The system does not preserve or replay full pre-compaction model context. SQLite is
the durable source for curated semantic records and attributable observations;
conversation state remains disposable. Recovery starts narrow and expands through
explicit queries, so old context cannot silently crowd out current evidence.
If no checkpoint matches, bootstrap may expose only a bounded factual fallback:
payload-free event metadata, explicit machine-observed authority and uncertainty,
and directions to targeted checkpoint/freshness reads. It does not reconstruct a
task from activity.

## Trust boundaries

- Claims retain separate machine-observed, user-reported and caller-asserted
  authority. Observation of one path is not universal proof of correctness.
- Raw client payload retention is disabled by default.
- The controller never captures stdin or imports the full process environment.
- MCP exposes continuity data but no generic shell or remote-execution capability.
- Reviewed procedures remain inert; storing or showing one does not execute it.

## Stable boundaries

Provider adapters depend on the canonical event package; the canonical packages do not depend on providers. SQLite is behind repositories. External MCP schemas are versioned independently from database migrations.
