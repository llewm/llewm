# Activity retention proposal

Status: deferred design for [issue 24](https://github.com/llewm/llewm/issues/24).
No activity deletion, retention configuration, or automatic compaction is
implemented by this proposal. The component's append-only observation contract
still applies. Enabling destructive retention requires an explicit reviewed
contract change and recovery policy.

## Current dependencies

| Data | Current consumers and constraints | Proposed treatment |
| --- | --- | --- |
| `events` | Checkpoint freshness, activity auditing, friction analysis and source references | Protect every referenced event and every open session; initially inventory only |
| `presence_events` | Advisory lifecycle history and latest session state | Protect the latest state for each session, live sessions and explicit references; initially inventory only |
| `actor_invocations` | Attribution parents, evidence creators, narrative creators, roadmap bindings and plan authority; included in portable history | Exclude from device-local pruning entirely |
| `usage_requests`, `usage_tool_calls`, `usage_tool_results` | Exact usage reports, comparisons, overhead estimates and session summary generation | Protect unfinalized sessions; require aggregate-aware reports and ingestion deduplication before any deletion |
| `usage_sources` | File ingestion offsets and matching state | Retain; raw-row deletion must not reset offsets or allow replay to count usage twice |
| `history_sync_changes` | Delta export cursors, generation guards and retained-envelope verification | Exclude from activity pruning; needs a separate journal protocol with a retention floor and verified full-replay fallback |
| Curated records and retained portable envelopes | Immutable identities, provenance, import/export and recovery proofs | Never prune through activity retention |

The source inventory is in migrations `001_initial`, `002_attribution_incidents`,
`004_roadmap_control`, `015_evidence_invocation`, `018_work_narratives`,
`019_roadmap_authority`, `027_track_plans`, `063_usage_ingestion`,
`071_agent_presence` and `086_history_change_journal`, together with later
migrations. Current usage reporting reads raw request/tool rows; the existing
session summaries alone are not a transparent replacement. Current sync cursors
query journal sequences; removing rows solely by age would invalidate their
assumptions. Foreign keys alone do not enumerate semantic references stored in
JSON, evidence references and history envelopes.

## Proposed preview contract

Start with a read-only inventory. A future `database prune --dry-run` would
report the exact project/store identity, schema, cutoff, policy version, source
snapshot, candidate IDs/counts, protection reasons and estimated reusable pages.
An absent or disabled policy selects no rows. It must never change ingestion
offsets, sync cursors, registry routes, plugins or backup retention.

Candidate selection must use an explicit table allowlist and a complete typed
reference closure. Protect rows referenced by curated records, evidence,
checkpoint attempts/actions, attribution, usage summaries and their provenance,
and retained portable envelopes. Preserve referenced parents recursively. If a
record version, reference kind or ownership cannot be interpreted, refuse that
candidate set instead of assuming it is unreferenced. No SQL cascade may expand
the reviewed set. Preview must distinguish counts of rows, logical bytes and
estimated recoverable disk space.

Usage deletion additionally requires exact, reproducible totals by client,
model, session, date and confidence class, including cache classes, reasoning
tokens, tool results and first-request overhead. Reports must merge retained raw
rows with archived aggregates without overlap, while comparisons must explicitly
report unavailable detail. Reingestion of an old or rotated source must remain
idempotent after raw request IDs are removed; offsets alone cannot provide that
guarantee. Until this is implemented and tested, preview protects all usage rows.

## Explicit apply and recovery

Only after contract approval, an apply operation could consume an exact preview
digest plus store identity. Under a reserved writer transaction it would
revalidate the snapshot, cutoff, protected-reference closure and complete ID set.
Any change invalidates the preview. It would first create and verify a
non-overwriting backup, delete only the exact reviewed rows transactionally, run
foreign-key and aggregate checks, and append a receipt containing the policy,
counts and backup identity. The receipt must not copy deleted sensitive content.

Rollback before commit restores the transaction. Recovery after commit requires
the verified backup using the supported restoration procedure, with clients
stopped and the current store retained for review; a blind live-file replacement
could lose newer work. Test interrupted backup, deletion and receipt failures,
changed snapshots, protected cross-table references and exact report equality.

Compaction is a separate explicit operation after logical deletion. Ordinary
SQLite deletion reuses pages without promising a smaller file. Compaction needs
space for a second copy and must preserve a recoverable original across crashes;
it must not run inside hooks. No default automatic `VACUUM` is proposed.

Existing backups, remote copies and portable history retain their own contents.
Activity retention cannot erase those copies or serve as retroactive secret
remediation. Historical-secret cleanup requires its own reviewed recovery policy.

## Decision still required

Choose whether the append-only contract should permit opt-in deletion of
unreferenced device-local activity after verified aggregation and backup, or
whether exact observations must remain available in an immutable archive.
Until that choice and the reference/aggregation protocol are approved,
destructive pruning remains blocked. This proposal does not grant execution
authority or change the adopting project's workflow.
