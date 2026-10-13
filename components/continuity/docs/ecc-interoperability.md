<!-- continuity:roadmap-node continuity-ecc-interoperability -->
# ECC interoperability

Status: accepted product direction; no ECC adapter is implemented.

## Decision

Llewm should interoperate with ECC as an external local-first peer. It should not
embed ECC's runtime, mirror its skill catalog, or create a second canonical handoff
store. The first useful integration is a bounded read-only adapter for ECC Memory
Vault records, followed by explicit Llewm import planning and reviewed apply.

Continuity already makes handoffs first-class through structured checkpoints,
strict task lineage, checkpoint actions and transitions, track plans, joined track
inventory, targeted historical reads, and deterministic work/bootstrap views. A
`HANDOFF.md` file, an ECC handoff memory, or a client session summary is therefore
an input or projection of continuation state, not a competing source of truth.

Llewm can completely replace a manually maintained `HANDOFF.md` for machine-to-
machine continuation once the relevant client integration is installed and the
structured state is current. Repository handoff files may remain useful as a
human-readable, Git-reviewed orientation surface and compatibility fallback. Llewm
must not claim to replace ECC's broader harness configuration, skills, hooks,
security tooling, or workflow catalog.

## Integration boundary

Use the same out-of-process boundary as provider-neutral project-management
adapters:

```text
ECC CLI / local Memory MCP / lifecycle observations
                       |
             versioned ECC adapter
       detect -> snapshot -> validate -> plan
                       |
          explicit reviewed Llewm apply
                       |
 checkpoints | narratives | work items | audit
```

The adapter owns ECC installation discovery, supported ECC versions, paths,
schema interpretation, and process invocation. Continuity owns bounded canonical
envelopes, redaction, provenance, authority labels, deterministic plans,
idempotency, receipts, and collision handling. The adapter must not access or
mutate Continuity's SQLite database directly, and Continuity core must not load
ECC code or credentials in-process.

Prefer ECC's documented JSON CLI or local stdio MCP surfaces over direct vault
filesystem interpretation. A filesystem reader is a separately versioned fallback
and must retain ECC's bounds, symlink exclusions, incomplete-scan failures, and
secret-handling rules.

## Memory mapping

Every imported ECC memory begins as an external unreviewed observation regardless
of its title, kind, confidence, source harness, or apparent agreement with current
state. Mapping proposes an Llewm destination; it does not promote authority.

| ECC memory kind | Candidate Llewm projection |
| --- | --- |
| `handoff` | checkpoint or track-continuation proposal |
| `decision`, `fact` | evidence, narrative, or decision proposal |
| `lesson` | suggestion or procedure candidate |
| `preference` | guidance proposal requiring explicit promotion |
| `runbook` | routine or procedure candidate |
| `note`, `context` | bounded historical context |

A proposal preserves the ECC memory ID, scope, source and target harnesses,
content hash, observation time, ECC schema/version, adapter version, mapping-profile
version, and source reference. It classifies the result as append, link, duplicate,
conflict, or reject. A mapping never flattens all ECC kinds into checkpoints.

ECC confidence and repeated observation are evidence about a candidate, not human
approval. Imported instincts remain suggestions. Absence of a user correction is
not acceptance, cross-project recurrence is not global authority, and no imported
record becomes guidance, policy, a roadmap commitment, or execution permission
without its existing Llewm review transition.

## Handoff projections

Continuation state has three distinct layers:

1. Canonical structured state: task lifecycle, checkpoints, actions, track
   relationships, and attributable evidence.
2. Deterministic bounded views: bootstrap, work views, exact checkpoint show,
   and resumable-track inventory.
3. External projections: repository `HANDOFF.md`, ECC handoff memory, Git note, or
   client-specific session summary.

An exporter may render one selected current task and a bounded set of references
into ECC or Markdown. It must name the exact checkpoint and task tip, preserve
authority, record the projection hash and destination, and never imply that writing
the projection advanced or resumed the task. Import compares exact identities and
plans reconciliation rather than selecting the newest prose by timestamp.

## Operational patterns to adopt

Llewm should independently adopt these ECC patterns where they fit its boundaries:

- managed installation receipts with exact owned paths and hashes;
- dry-run, doctor, repair, and conservative uninstall behavior;
- declared and observed per-harness capability matrices;
- explicit limits and disable switches for optional startup context; and
- deterministic agent-configuration scanning as later Action Control work.

Installation ownership must be provable before removing a file. Capability
declaration does not grant permission, and a successful structural probe does not
establish broad host parity. Context accounting should extend Continuity's typed
bootstrap budgets rather than replace them with one undifferentiated character cap.

## Delivery order

1. Publish a version-pinned ECC capability and mapping profile with fixtures.
2. Detect ECC and run its read-only memory health surface without modifying either
   store.
3. Produce bounded snapshots and zero-write Llewm import plans.
4. Add reviewed request-keyed apply with exact source-hash preconditions and
   receipts.
5. Add optional outbound handoff projection only after round-trip identity,
   collision, redaction, and stale-source cases pass.
6. Generalize managed install receipts and evidence-backed harness conformance from
   observed ECC integration friction rather than coupling them to the adapter.

The first slice requires malformed-record, duplicate-ID, broken-link, symlink,
secret-shape, incomplete-scan, truncation, stale-source, conflicting-authority,
idempotent-replay, and no-write tests. User scope remains opt-in. No background
sync, transcript import, semantic ranking, automatic bootstrap injection, or ECC
write is part of that slice.

## Licensing and provenance

Verify the exact selected upstream version and license before copying schemas,
fixtures or substantial code. Prefer independently implementing a qualified public
contract, retaining any required upstream notices. Llewm original source uses MIT;
that does not replace upstream licensing requirements.

Upstream behavior and license were reviewed on 2026-09-23 from:

- <https://github.com/affaan-m/ECC/blob/main/docs/design/ecc-memory-vault.md>
- <https://github.com/affaan-m/ECC/blob/main/hooks/README.md>
- <https://github.com/affaan-m/ECC/blob/main/skills/continuous-learning-v2/SKILL.md>
- <https://github.com/affaan-m/ECC/blob/main/README.md>
- <https://github.com/affaan-m/ECC/blob/main/LICENSE>

These links are observations of an evolving upstream project, not permanent
provider guarantees. The adapter manifest must name the versions it actually
qualifies.
