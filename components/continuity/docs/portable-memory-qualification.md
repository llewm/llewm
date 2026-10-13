# Portable-memory qualification

The maintained local CLI fixture exercises isolated projects and explicit
lineage adoption through export, inspect, plan, apply, replay, and re-export.
It is source qualification, not proof that a live store, network peer, or native
platform has been upgraded.

## Reproduce

From `components/continuity/`:

```sh
go test ./cmd/llewm -run '^TestPortableMemoryTwoProjectCLIQualification$' -v
go test ./internal/portable
go test ./internal/memsync
```

The CLI fixture creates independent temporary projects and stores. Its emitted
measurements describe that run's seeded profile; historical record counts and
byte totals are not protocol constants or coverage of every later record family.

## Required observations

- Unchanged semantic exports have identical record bytes and record-set hashes;
  export identity/time metadata may differ.
- Inspect reports exact sizes/counts without loading record bodies.
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

## Limits

Cross-compilation does not qualify native replacement behavior or interactive
clients. A local bundle test does not qualify credentials, network reliability,
background sync, remote-only records, or exact live-store recovery.
Use [memory repository rules](memory-repository.md) for live sync and
history-preserving conversion, and [Git-reviewed exports](git-reviewed-portable-memory.md)
for explicit reviewable transport snapshots.

The metadata catalog and its commands were removed before release. Current
history search is backed by project records, not the retired catalog interface.
