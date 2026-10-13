# Git-reviewed portable-memory profile

Status: implemented optional transport profile. It reuses the qualified curated-v1
semantic bundle and does not change record authority, lineage, redaction, import,
or collision rules.

## Boundary

The CLI transport selector `git-reviewed-v1` publishes immutable review material inside the selected project
repository. Continuity creates files only. It does not run `git add`, commit, merge,
push, pull, fetch, or any network command. The result reports
`review_required: true` and `git_actions_performed: 0`; a human or separately
authorized workflow decides whether anything is tracked.

The export root must already exist as a real, non-symlinked project subdirectory.
It cannot be the project root or live `.git/` or `.continuity/` state. The live
SQLite database, WAL/SHM files, backups, raw events/sessions, usage samples,
reference source bodies, roadmap sync-file snapshots, and derived friction
candidates are not copied into this profile.

## Layout and identity

Given an export root such as `portable-memory`, publication creates:

```text
portable-memory/
  v3/
    <32-hex-lineage>/
      <record-set-sha256>/
        manifest.json
        transport.json
        records/
          <record-type>/
            <identity-sha256>.json
```

The snapshot directory is addressed by the canonical bundle's semantic
`record_set_sha256`. Publishing the same semantic set at the same root is a visible
no-replace refusal. A changed set receives a different directory, so an earlier
reviewed snapshot is not rewritten.

Each record file contains exactly one canonical portable envelope plus a trailing
newline. Its path partitions by record type and hashes the stable tuple
`(record_type, record_id, record_revision)` so arbitrary record IDs cannot become
paths. `transport.json` is canonical JSON that declares transport format/version,
the manifest hash, record count, ordered record paths, and envelope hashes.
`manifest.json` is unchanged from the local-directory bundle.

Reading reconstructs the canonical ordered `records.jsonl` bytes in memory and
runs the existing strict bundle validator. Readers reject noncanonical descriptors,
path traversal, symlinked parents or files, unknown files, mixed aggregate and
partitioned layouts, count/hash/path mismatches, tampering, unsupported versions,
and bundle-bound violations. No aggregate `records.jsonl` is tracked.

## Explicit workflow

1. Create a deliberate in-repository export root, for example
   `mkdir portable-memory`.
2. Publish without Git side effects:

   ```text
   llewm memory export \
     --transport git-reviewed-v1 \
     --directory portable-memory
   ```

3. Use the returned content-addressed `directory` for `memory inspect`. Review the
   manifest omissions, redaction profile, record metadata and bodies, repository
   status, and exact files before choosing whether to stage them.
4. Perform any `git add`, commit, review, merge, or distribution outside
   Continuity under the repository's existing human and policy controls.
5. In a receiving checkout, run `memory inspect`, then zero-write `memory plan`.
   Apply only with the exact inspected lineage and selected local project ID.
   The descriptor uses `llewm-git-reviewed-export` version 2 and transport identity
   `git_reviewed_v2`; the CLI selector names the export profile, not its wire version.

Git distribution does not establish lineage, trust, privacy approval, record
authority, or permission to import. A checkout with missing or altered files fails
validation. Same identity and envelope hash remains idempotent; same lineage,
record identity, and different envelope hash remains a collision that denies the
whole apply without semantic writes. Git merge conflicts are resolved explicitly
before Continuity validation; Continuity never chooses a branch or overwrites a
conflicting record.

## Qualification

Disposable-repository tests prove deterministic layout/descriptor bytes,
partitioned round trip into the existing bundle validator, no-replace publication,
tamper and unexpected-file refusal, mixed-layout refusal, in-project/symlink
boundaries, untracked review state with an unchanged Git index, absence of seeded
secrets/absolute roots/live-store files, public inspect/plan/apply recovery,
stable semantic re-export and same-lineage collision refusal without writes.
The metadata catalog and its drilldown commands were removed before release.

This profile does not implement incremental export, deduplicated repository-wide
object storage, Git LFS, SQLite snapshots, automatic retention, background sync,
network transport, automatic commits/pushes, or policy enforcement.
