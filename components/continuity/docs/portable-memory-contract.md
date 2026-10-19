<!-- continuity:roadmap-node continuity-portable-memory -->
# Portable memory contract

Curated portable records transfer attributable project history without placing a
live SQLite database in Git. Exact database backup, local bundle export/import,
Git-reviewed export and optional History sync are separate operations.
None authorizes execution of a stored plan, procedure or checkpoint next action.

## Format 3

The current format is 3: manifests use `llewm-portable-memory`, envelopes use
`llewm-portable-record`, and the managed memory repository uses a `v3/`
partition. Normal import reads the current format. An explicit converter handles
validated historical observations; it is not a general old-client sync mode.

Conversion preserves record identity, lineage, payloads, content hashes, references,
and retained fork resolutions. Original format-2 observations remain in memory Git
history. See [format-3 conversion](memory-repository.md#format-3-conversion).
Do not manually rewrite the managed clone or its history.

Git-reviewed exports use `llewm-git-reviewed-export`, descriptor version 2 and
transport kind `git_reviewed_v2`. See [Git-reviewed memory](git-reviewed-portable-memory.md).

## Identity and integrity

Portable identity is scoped by lineage, record type, stable ID and revision.
Project IDs and paths are device-local; importing into an explicitly associated
project recomputes local fields without changing portable semantic identity.
An independent lineage may reuse a local record ID.

A complete bundle contains a bounded manifest and canonical `records.jsonl`.
The manifest identifies export, lineage, source instance, time, source schema,
selection/redaction profiles, per-family counts, record-stream and record-set hashes,
and declared omissions.

`records_sha256` covers the exact record stream. `record_set_sha256` covers
the ordered envelope hashes, each followed by a newline. Records sort by type,
ID, then numeric revision. Stable semantic records keep the same record-set hash
even when export IDs and creation times differ.

Each envelope carries format/schema versions, lineage/type/ID/revision/time,
authority, payload content hash, envelope hash, optional bounded routing index,
references, and canonical payload. The index is part of the integrity boundary;
it cannot assert routing facts different from the validated payload.

## Current record families

The codec supports checkpoints/actions/contexts/branches, fork resolutions,
roadmap nodes/bindings/links/review routines/syncs/releases, work narratives and
supersession, work items/dispositions/links, track plans, actor invocations,
decisions, topics/aliases/links, procedures, resources/notes, pins, coverage cells,
lease stamps/stale-write resolutions, and usage-session summaries.

Per-family adapters validate supported schemas, indexes and dependencies.
Retired governed-contract records remain historical observations rather than
current operational families. Canonical contracts are repository Markdown;
their accepted-hash index is device-local.

Raw events, sessions, per-request usage samples, database/WAL/SHM files, credentials,
backups, transcripts and raw archives are excluded. Derived indexes are rebuilt,
not treated as portable authority. Consult the manifest's omissions rather than
assuming every local table or source body is included.

## Privacy and bounds

Redact recursively before persistence/export and replace source-root and
project-local identifiers with explicit markers. Pattern redaction is bounded
protection, not proof that arbitrary free text is safe to share. Keep export
destinations private and review the selected data.

Current codec limits are:

| Item | Maximum |
| --- | ---: |
| Record payload | 128 KiB |
| Routing index | 16 KiB |
| Manifest | 1 MiB |
| Record stream | 64 MiB |
| Record count | 100,000 |

Canonical encoding and pre-write validation reject malformed input, unknown
required versions/schemas, duplicate identities, count/hash mismatches, invalid
references and oversized fields. The current export is complete; timestamps are
not incremental export cursors.

## Local workflow

```sh
llewm memory export --directory /private/export
llewm memory inspect --directory /private/export
llewm memory plan --directory /private/export
llewm memory apply --directory /private/export \
  --confirm-lineage LINEAGE_ID --confirm-project PROJECT_ID
```

Use a new export directory and the exact lineage/project identities reported by
the selected project and plan. Inspect the command's help before applying.
Export publishes a complete directory without overwriting an existing destination.
Inspect and plan do not mutate semantic state.

Planning validates the bundle, lineage and dependencies, then classifies records
as append, already present, collision, invalid or unsupported.
Apply is transactional: validation, dependency, collision, storage or receipt
failure must not leave a partial semantic import. Replay appends no semantic
records; an explicit import may append another receipt.
Source-local observations do not certify destination file state or client behavior.

Imported checkpoints preserve their original actions and transitions rather than
calling the live checkpoint service to invent new actions. Imported decisions,
plans, recipes and resolutions preserve attribution; import does not select work,
accept a proposal, activate instructions or execute anything.

## Qualification

The maintained local CLI fixture exercises isolated projects and explicit
lineage adoption through export, inspect, plan, apply, replay, and re-export.
It is source qualification, not proof that a live store, network peer, or native
platform has been upgraded.

### Reproduce

From `components/continuity/`:

```sh
go test ./cmd/llewm -run '^TestPortableMemoryTwoProjectCLIQualification$' -v
go test ./internal/portable
go test ./internal/memsync
```

The CLI fixture creates independent temporary projects and stores. Its emitted
measurements describe that run's seeded profile; historical record counts and
byte totals are not protocol constants or coverage of every later record family.

### Required observations

- Unchanged semantic exports have identical record bytes and record-set hashes;
  export identity/time metadata may differ.
- Inspect reports exact sizes/counts without loading record bodies; verify exact
  load accounting for bounded reads.
- Same-lineage planning is write-free; apply imports the selected records and
  records a receipt. Replay adds no semantic records.
- Re-export preserves semantic bytes and hashes across project-local identities.
- A different lineage is refused before writes, even when local record IDs overlap.
- A same-identity/different-content collision prevents partial apply.
- Seeded secrets and absolute project paths are redacted before export.
- Invalid canonical JSON, unsupported schemas, duplicates, dependency errors,
  tampering and oversized input fail before semantic writes.
- Raw events, sessions and usage samples remain excluded; the manifest states
  omissions. Device-local contract indexes are not portable contracts.

Family-specific tests cover later records, references and fork-resolution
semantics. Conversion qualification must separately prove preserved payloads,
content hashes, identities, settled forks, retained historical observations,
idempotence and fresh-machine recovery.

### Limits

Cross-compilation does not qualify native replacement behavior or interactive
clients. A local bundle test does not qualify credentials, network reliability,
background sync, remote-only records, or exact live-store recovery.
Use [memory repository rules](memory-repository.md) for live sync and
history-preserving conversion, and [Git-reviewed exports](git-reviewed-portable-memory.md)
for explicit reviewable transport snapshots.
