# Memory repository rules of engagement

The memory repository is the Git repository that History sync uses to move curated
Continuity records between machines (see [multi-repository projects and
sync](../../../docs/multi-repo-and-sync.md#history-sync)). It is a special case. It is
not a code repository, and the project rules for code repositories (commit policy,
Git notes, work narratives, roadmap freshness, review) do not apply to it. These
rules do.

<!-- continuity:contract memory-repository -->
## Rules in brief

kind: promise
scope: the History-sync memory repository, its clones, and the sync init and sync commands
review: when sync authentication, the memory repository layout, or the commit policy changes

- Only Continuity writes to the memory repository and its clones; people and
  agents never edit, commit, rebase, reset, or force-push them by hand, and
  rewriting or wiping memory history needs a recorded engineer decision.
- Sync never runs Git commands against, or changes the configuration, hooks, or
  history of, a code repository; the memory clone is never a workspace member or
  inside a project checkout.
- Every Git command Continuity runs drops inherited variables that select a
  repository, index, or object store or inject configuration; sync commands also
  pin settings that could rewrite envelope bytes, start programs, or run
  maintenance.
- Before cloning and on every sync, the URL Git would contact for fetch and push
  must equal the configured remote; otherwise sync refuses.
- Invariants are not configurable; deliberate settings are named, validated, and
  clamped, and each non-default one is named in sync status and the session
  bootstrap; only the user's own network environment passes through by design.
- Memory commits are automated, self-describing, and run with hooks and signing
  disabled; they are exempt from the engineer's commit-note and narrative policy.
- The repository holds only curated, redacted, immutable envelopes (and, with
  cross-machine leases, lease refs): no SQLite, no device-private data, and no
  credentials.
- Syncs from hooks and checkpoints disable Git/SSH askpass and terminal prompts;
  available credential helpers and SSH agents can still supply credentials.
  Explicit `sync --prompt` may use configured askpass; only interactive `sync init` may
  prompt on the terminal, and it confirms non-interactive access before saving
  configuration.
- Authentication uses the user's existing Git credentials, an SSH key for memory
  syncs, or a credential helper configured in the memory clone only. Askpass can
  retrieve saved wallet credentials or obtain a credential during setup, but
  automatic sync disables it because it can also prompt. Plaintext storage is last.
- Unencrypted credential storage is used only when explicitly requested,
  explained, and confirmed.
- Continuity never changes global or system Git configuration, and a secret never
  enters project configuration, the database, the memory repository, the remote
  URL, logs, or error text.
- Session-start sync runs in the background; the bootstrap uses last imported history;
  offline work is committed locally and pushed later; conflicting record versions
  are reported, never merged silently.

## Ownership

[Portfolio federation](portfolio-history.md) can explicitly route independent
project lineages to a private aggregate and separate group Git repositories.
Each managed clone follows this same ownership contract. Git repository readers
can read every lineage stored there; lineage directories do not enforce access.

- Continuity owns the memory clone and every Git operation in it: export, commit,
  fetch, rebase, import, and push, under a file lock. People and agents do not edit,
  commit to, rebase, reset, or force-push the memory repository or its clones by hand.
  Every bootstrap of a project with sync states this as the built-in
  `memory-repository` rule, naming the clone path, so it needs no copy in
  `AGENTS.md`.
- Rewriting or wiping memory history requires an explicit engineer decision
  recorded in the decision ledger (for example `wipe-memory-repo-unstable-narratives`).
  After a wipe, every machine re-exports.
- The clone lives under `~/.local/share/llewm/memory/`, one per remote, shared by
  every project on the machine that uses that remote. It is never a workspace member
  and never sits inside a project checkout.

## Separation from code

- Continuity never runs sync Git commands against a code repository. A code
  repository's configuration, hooks, and history are never changed by sync.
- Sync Git commands, like every Git command Continuity runs (`internal/gitenv`),
  are isolated from the calling environment. Variables that
  select a repository, index, or object store, or inject configuration
  (`GIT_DIR`, `GIT_WORK_TREE`, `GIT_INDEX_FILE`, `GIT_CONFIG_PARAMETERS`,
  `GIT_CONFIG_COUNT` and its key/value pairs, and the rest of Git's
  `rev-parse --local-env-vars`, plus `GIT_NAMESPACE` and `GIT_QUARANTINE_PATH`)
  are dropped. A Git hook or wrapper that runs Continuity therefore cannot
  redirect a sync into a code repository.
- Settings that could rewrite envelope bytes, start programs, or run maintenance
  are pinned for every sync command: `core.hooksPath` (disabled), `commit.gpgsign`,
  `core.autocrlf`, `core.fsmonitor`, `gc.auto`, and `maintenance.auto`.
- Before cloning and on every sync, the URL Git would actually contact for fetch
  and push (after `url.*.insteadOf` and `pushInsteadOf` rewriting and any push URL)
  must equal the configured remote; otherwise Continuity refuses.
- The memory repository can be ahead of a machine's code checkout: records may
  describe commits and files that the checkout has not pulled yet. Detecting that is
  part of the fork-detection slice.

## Commits and content

### Unchanged syncs

Migration 081 adds device-local completion proofs for Git sync. SQL triggers
change a durable token whenever canonical records, export dependencies, or the
derived inventory change. Sync-attempt logs, raw telemetry, and other excluded
bookkeeping do not invalidate the token. Migration 086 replaces the database-wide
token with project-scoped generations and a device-local change journal. Each
tracked write records its table, primary-key identity and operation in the same
transaction; updates retain both old and new identities. Sequence numbers describe
ingestion order, not event time. Tokens combine the project's generation with a
global generation for changes whose ownership is global or not yet resolved.
Changes in another known project no longer invalidate the proof. Missing tokens
fail closed; journal edits or deletions invalidate proofs globally. The journal
contains no record bodies and is not portable history or a peer receipt.

Migration 087 adds durable, checksummed consumer cursors. Reads are bounded by
an ingestion sequence in one database snapshot; acknowledgement compares that
snapshot's generation atomically, after successful materialization. Interrupted
consumers replay their unacknowledged changes. Missing or damaged cursors, global
changes, archive changes and oversized deltas require complete validation.
Local capture consumes index-only changes without rebuilding canonical exports;
Git and folder sync share this capture path. Qualified append-only canonical
changes select their operational identities and exact child rows, using the same
encoders and retained-byte overlay as complete export. Narrative and roadmap
actor dependencies include their invocation ancestors. The export takes a writer
snapshot, checks the observed journal position before loading, retains the selected
envelopes, and returns a receipt including its own retention writes atomically.
Capture acknowledges this receipt only after durable publication and the final
database/archive guards. Interrupted retention or publication therefore replays
through complete export. Updates, deletions, retained-envelope changes, recovery
graphs, unknown families and more than 128 selected identities in a table retain
complete validation; journal reads are additionally bounded to 4096 changes.
Capture may advance its archive stamp across its own new
record/provenance files only after accounting for the exact additions, validating
their bytes, and checking that all pre-existing files retain their metadata
identities. Unexpected changes, including concurrent foreign provenance, decline
the optimization. Parent directories may change timestamps and size but must
retain their device, inode and mode. Tracking starts only before an actual file
creation; platforms without change-time identities retain complete validation.
This does not yet enable affected-dependency import replay or journal pruning.
Existing proofs are discarded by
migration 086; capture-v2 does not reuse earlier capture proofs.
Migration 082 narrows attribution tracking: raw invocation activity invalidates
only when the invocation is already retained or referenced by indexed history.
Adding a referring record invalidates independently. The generation also records
the last invalidating trigger name for device-local diagnosis.

After a successful, stable cycle with no pending semantic or delivery work,
later cycles can reuse its proof. They still verify the configured remote,
fetch, compare the lineage's v2/v3/v4 subtree identities, require a clean tracked
working tree, and check local database and archive state again. An unrelated
lineage's commit does not require record replay. A missing or invalidated proof,
changed policy or build, local edits, pending push, or uncertain state runs the
full workflow. Explicit conversion always runs the full workflow.

On Linux, the archive guard walks metadata (device, inode, mode, size, mtime,
and ctime), including provenance. It does not open or decode record contents;
even same-size edits with restored mtime invalidate the proof. Status files are
excluded. Platforms without this change identity retain full validation.
This still costs a metadata walk, rather than constant work independent of
archive size. Folder endpoints can reuse a local completion proof too: the
configured marker/lineage, endpoint contents, archive metadata, database token,
build and relay policy must remain unchanged. Missing, changed or uncertain
state takes the complete validated scan and publication-repair path. Folder
results retain `unchanged` and `duration_ms`, visible through status and TUI
Health. They still report peer receipt as unknown: local filesystem stability
is not a provider delivery acknowledgement. The coordinator can also reuse a completed local export/capture proof while
its database and archive inputs remain stable. A capture with unresolved actor
dependencies is never cached.

Guarded mixed imports select the affected typed dependency closure, including
earlier dependents of newly arriving targets. Selectors distinguish exact
revisions, any revision, and the prior revisions used to validate a published
release. Plan predecessors and comparison revisions come from canonical payloads,
not informational wire references. The canonical importer validates the selected
records; the writer transaction still compares the complete local state before
renewing reuse evidence. Unknown or recovery-sensitive dependency rules retain
the full importer. Migration 088 persists typed selectors with forward/reverse
indexes and no target foreign key, preserving unresolved dependencies. A checksum
binds each complete selector set and its support flag to an immutable envelope
hash and interpretation version. Missing, stale or damaged sets are decoded and
replaced atomically; cache access failures use the full importer. These are
disposable derived checksums, not authentication or semantic import receipts.
Closure traversal discovers earlier dependents through SQLite's typed reverse
index and reads forward selectors only for reached records. Nested prior/any
selectors consume each matching target once, and reverse range lookups fetch
only newly matching bounds. Selector verification releases each bounded batch
before loading the next instead of retaining the complete edge graph. Verification
and traversal share one SQLite snapshot; warm reads leave the writer available,
while repairs restart under a writer transaction and recheck the snapshot.
Snapshot identity lookups retain positions into the validated envelopes rather
than copying envelopes. Exact forks and range selectors share revision lists,
sorted only when reached; consuming a range leaves exact membership available.
Complete selector checks and snapshot identity assembly still visit old history;
this is not a complete-cycle scaling guarantee.

The immediate sync JSON result includes `unchanged: true` when this proof skips
export, archive content reads, block processing, and semantic replay. Migration
083 retains that observed mode and fork counters in sync attempts. `sync status`
and the TUI Health view show the last recorded mode and duration; older attempts
have unknown mode rather than being inferred from their transfer counts. Proofs are
not portable import receipts and are never evidence of another peer's receipt.

Migration 084 excludes coordination grants, renewals/releases, and remote lease
mirror receipts from the history generation token. They use separate lease refs;
an unchanged history cycle still reconciles them. Lease stamps on history writes
and stale-write resolutions remain canonical exported records and invalidate the
history proof. Receiving or publishing a coordination lease alone must not force
export or semantic replay on the next cycle.

Migration 085 also excludes semantic replay sets, tips, and their membership.
These disposable caches are rechecked against canonical state before semantic
reuse; refreshing or losing them does not change transport inputs. Block,
record, reference, and endpoint publication inventories remain tracked so damage
and pending delivery still invalidate completed transport proofs.

For independent additions to a previously proven mixed set, verification can
reuse the import planner's complete local snapshot when every incoming record
is already present. The same SQLite connection stays pinned through planning
and writer-lock acquisition; any intervening commit invalidates reuse through
`data_version` and takes the reference fallback. Membership, canonical bytes,
references, recovery state and fork checks still apply. Appends continue to
verify fresh post-write state; this snapshot is never reused across imports.
Additions may also include plan revisions, resources and notes,
and checkpoint records, contexts, branches, actions and transitions. The canonical
planner validates every addition and its dependencies. Appends renew the proof
from a fresh canonical snapshot inside the same writer transaction; any mismatch
rolls back all additions before the complete importer runs. An old
record referring to an added ID still forces fallback, including a resource
revision referenced by an older note; these guards are deliberately conservative.

Local import snapshots preserve stable block membership for both published
records and records received through other destinations. Residual records use
a device-local `local-snapshot:` membership in the derived publication catalog,
so one foreign arrival adds a block for the new records instead of repacking
old history. This membership never grants transport provenance or relay
permission. Snapshots still validate archive bytes and indexed references;
damaged inventories follow the existing repair path. Existing duplicate derived
rows are not pruned by this change.

Tracked-path discovery reuses immutable Git tree nodes across locked block
cycles. The tree is taken from the index, including staged additions and
deletions, rather than assuming HEAD describes interrupted publication state.
Raw tree objects are cached locally under the archive's `status/git-trees`.
Each read verifies the complete bytes against the selected Git SHA-1 or SHA-256
object ID before parsing membership. Only missing, changed or damaged nodes
are fetched from Git again; cache writes are optional. Uncertain
or unmerged indexes and discovery limits fall back to the complete tracked-path
listing. Paths use NUL delimiters. Every discovered block still undergoes the
ordinary working-file and content validation, so a tree cache cannot conceal
missing or corrupt working files. Cache contents are bounded per object and
root-relative file access confines local I/O. Cached node reads, path enumeration
and complete content validation still visit old history. This reduces repeated
Git discovery; it does not establish fixed-delta scaling or replace full recovery.

Folder publication writes bounded, content-addressed arrival segments after
publishing its authorized blocks. The validated catalog from discovery supplies
existing membership; only newly observed block identities enter new segments,
with at most 128 block identities per segment. Segment roots are immutable and
their union describes the observed endpoint block set. Late or backdated records
append in the current publication batch rather than reopening old time buckets.
There is no global sequence, mutable latest root, or last-writer selection.
Concurrent publishers can announce overlapping batches without losing records.
A retry after interruption rediscovers completed segments and publishes the
remaining membership. The private discovery catalog is bound to the same open
root and lineage; a fresh process rediscovers it. Unknown or damaged metadata
falls back to cataloging the complete supplied block inventory. This does not
remove damaged older roots or replace independent complete block discovery.

Manifest leaves contain at most 128 sorted block identities; radix branches
contain at most sixteen children, each bound to its hash prefix. For complete
snapshot manifests, a late arrival changes only its ancestors and leaf,
preserving unrelated subtree identities. This
catalog does not repartition existing blocks or sort arrivals by their recorded
timestamps. It contains no record payloads and grants no relay permission.

Manifest nodes live at `v4/<lineage>/manifests/<hash-prefix>/<hash>.manifest`.
Children are durably published before parents, followed by an immutable root
announcement at `v4/<lineage>/manifest-roots/<hash>.root`. Independent writers
retain separate snapshot roots; interrupted publication can leave unannounced
nodes, and retry reuses their exact bytes. Nothing is removed or overwritten.
An announcement identifies a snapshot, not the endpoint's latest or complete
inventory, external delivery, or a semantic import receipt.

The manifest reader validates lineage, canonical framing, hashes, radix positions
and traversal bounds. Its delta operation skips equal subtree hashes only when
the caller already holds the previous validated catalog; missing or damaged
metadata is an error, never an empty inventory. Block bytes still require their
own validation. Root discovery selects all observed immutable announcements and
unions their catalogs; it never chooses a snapshot by publication time or file
modification time. Shared manifest nodes are validated once per catalog read.
Missing announcements, unexpected root entries, damaged nodes, exceeded bounds
or an observed root-set change fail the whole read without a partial inventory.
Interrupted staging files are ignored. Even a valid union can omit block files
whose writer has not announced a root, so it is not proof of endpoint coverage.

Folder sync retains a disposable local catalog coverage receipt only after a
complete valid block-directory scan exactly matches the manifest union and the
directory identity remains stable. The receipt is scoped to the project,
lineage, endpoint, root set and directory device/inode/mode/size/mtime/ctime;
it lives in local archive status and is never transported. On Linux, unchanged
membership can reuse the catalog instead of enumerating the flat block
directory. Adding, removing or renaming any entry invalidates this receipt.
Every block's bytes and derived index contents are still validated, including
after an in-place edit that preserves mtime. Unknown, damaged, changing or
unsupported metadata takes the complete scan path. Independent inventory
rebuilds always scan the endpoint directly.

Indexed block reads and archive publication plans keep private copies of their
verified envelopes. Import reuses each copy when it reconstructs the supplied
block bytes exactly, avoiding
a second inventory query and envelope decode during the same cycle. Exported
parsed fields cannot change that copy. Blocks without a matching copy use the
ordinary inventory validation and repair path. Later scans still check the
inventory; dependency selectors and local semantic state keep their separate
transaction guards. This reuse does not prove delivery or semantic acceptance.

This is local directory-membership evidence, not external delivery, provenance
or semantic import evidence. Manifest traversal, block reads and complete import
remain. Folder publication now consumes the discovered membership delta, but
discovery still traverses the complete root union. Incremental graph discovery
and complete-cycle scaling qualification remain
unfinished. These segments alone make no end-to-end speed or scaling claim
and do not convert block format 4.

Curated exports include a referenced creator invocation and its transitive
parent invocations, loaded in bounded batches. Unrelated invocation activity
is excluded. This preserves the attribution dependencies needed by a fresh
destination.

Writer API 124 retains each canonical envelope in an append-only local table
at its first memory export or successful import (migration 076). Later exports
reuse that form, including after a root move. Imported prose is already portable;
the destination's roots and project ID must not redact it a second time. Import
planning compares retained envelopes too. This prevents new transport variants;
it does not settle historical collisions or revision forks. Keep automatic sync
paused during recovery, and preserve exact procedure definitions and grants.

Writer API 125 adds migration 077 and proof-carrying `redaction_recovery`
records for historical checkpoint, narrative, procedure, and track-plan
re-exports. With automatic sync paused and all writers upgraded, preview each
pair of tracked envelope hashes:

```sh
llewm sync repair-redaction --original ORIGINAL_SHA256 --derived DERIVED_SHA256 \
  --redacted-root /absolute/peer/project --redacted-project-id project-PEER_ID \
  --creator NAME --reason 'Historical peer re-export'
```

Review the original and derived envelopes in the JSON result, then repeat the
same command with `--apply --expect-recovery RECOVERY_ID`. Application appends
the exact proof locally; ordinary sync transports it. It never edits memory
files, operational records, procedure revisions, or standing grants. Existing
operational procedure arguments must still be verified separately before use.
The proof must reproduce the entire derived envelope using only the declared
additional root/ID substitutions. Unrelated changes, reverse substitutions,
ambiguous corrections, and chains refuse. Both original and derived observations
remain available. Fork projections exclude only the proved derived content;
exports and fresh imports use the original transport form. Sync imports these
proofs before interpreting the affected historical identities. Require repeated
zero-collision/fork/unpushed receipts on every peer and fresh recovery before
restoring the previous automatic-sync setting.

- Memory commits are automated and self-describing (`continuity: <instance> +N
  records`). They run with repository hooks and signing disabled and a fallback
  committer identity when the user has none. They are exempt from the engineer's
  Git-note and work-narrative commit policy, because they are machine records, not
  engineering work.
- The repository holds curated, redacted, immutable envelopes in content-addressed
  blocks at `v4/<lineage>/blocks/<block-sha256>.block`. There is no
  SQLite, no device-private data (raw events, sessions, usage, hook telemetry), and no
  credentials.
- A project that opts in to cross-machine leases (`[leases] remote = true`) also
  keeps one ref per lease scope at
  `refs/continuity/leases/<lineage>/<scope kind>/<sha256 of scope ID, 32 hex>`.
  Each points at a chain of commits with the empty tree, one per grant event
  (acquire, renew, release), with the event as JSON in the message. Continuity
  writes them only with `git push --force-with-lease=<ref>:<tip read>`, so the
  remote applies at most one of two racing writes. They live outside `refs/heads`,
  so the history branch, its rebase, and its pushes never touch them, and the
  remote must accept pushes to custom refs. Like the history branch, they are
  never edited or deleted by hand.

## Format 4 conversion

Format 4 packs the existing canonical envelopes into immutable, bounded blocks.
Ordinary Git sync refuses older tracked records. Before adopting a format-4 binary:

The format-4 build advances the local writer floor to API 135. Existing older
client processes must be restarted before they can write to the upgraded store.
This local fence does not stop a different device's sync process; stop or upgrade
every writer on the lineage before conversion.

1. Verify a consistent backup of the operational database and preserve the memory
   history. Confirm that every existing writer is stopped and will either upgrade
   or be retired, including disposable test machines.
2. Preserve global guidance and the configured memory clone. New defaults use
   `llewm` instead of `llmem` in their configuration/data namespace. An existing
   old-default directory refuses silent adoption. Set `CONTINUITY_GLOBAL_HOME` to
   the retained guidance directory, or explicitly move that ordinary configuration
   directory before starting the new client. Set `[sync] clone` to the retained
   existing clone path before installation; explicit locations remain supported.
   Do not move or edit the managed memory clone by hand. Physical clone relocation
   is not implemented in this slice; retaining its configured path preserves it.
3. Upgrade the active writer, verify its identity and writer floor, then run on one
   machine with the remote available:

```sh
llewm sync convert --writers-stopped --output json
```

The converter fetches before examining the old partition and validates each
tracked observation's bytes, lineage, hash and path. It packs every v3 envelope,
including remote-only records and rejected heads, without changing its bytes.
It stages the blocks and retires tracked v3 paths in a normal memory Git commit;
the original bytes remain in Git history and the local archive. Format-2 records
first pass through the existing explicit format-3 conversion.

Retired contract-family observations are carried forward as archival transport
records and continue to be skipped by operational import. Conversion refuses
invalid transport records and exceeds neither the
100,000-record nor 64 MiB input budget. Refusal leaves the old partition intact
and does not push. It requires remote access. Repeating a successful conversion
writes no additional observations and does not reopen settled forks. Verify
receipts and fresh-machine recovery before enabling other upgraded writers.

If `v1/` still exists, complete the earlier format-1-to-2 conversion with the
original format-2 build first. This converter does not remove unreadable
format-1 records or manufacture replacements from incomplete local state.

## What can and cannot be changed

Internal behavior should not change by accident. Rules fall into three kinds:

- **Invariants** cannot be configured: no prompts during automatic syncs, hooks and signing
  off, the environment isolation and remote check above, background session syncs,
  and no secrets in the repository.
- **Deliberate settings** are named `[sync]` keys and `sync init` flags, validated
  and clamped (`manual`, `ssh_key`, `askpass`, the clone's
  credential helper).
  Each one that differs from the default is named on the `Sync:` line in
  `llewm sync status` (`settings: ...`, with a
  `PLAINTEXT credentials` warning for the last-resort helper), and in the `settings`
  field of `sync status --output json`.
- The former `session_timeout_seconds` setting no longer affects startup sync;
  status reports it as ignored when it is present.
- **The user's own environment** is respected on purpose because it is how the
  machine reaches the network: credential helpers and programs, the SSH agent and
  `GIT_SSH_COMMAND`, proxies, `GIT_CONFIG_GLOBAL`, and tracing such as `GIT_TRACE`.

## Setup and status

Run setup explicitly on each machine. The first machine starts a lineage; another
machine joins that exact lineage before syncing:

```sh
# First machine
llewm sync init --remote git@host:org/llewm-memory.git
# Other machines: use the first machine's exact lineage
llewm sync init --lineage LINEAGE --remote git@host:org/llewm-memory.git
llewm sync
llewm sync status
```

Use the authentication options below when the existing Git setup cannot access the
remote without prompting. Automatic sync begins once configured; set
`manual = true` under `[sync]` for manual operation.

To investigate CPU use during an explicit sync, capture a local profile:

```sh
llewm sync --cpu-profile ./sync.cpu.pprof --profile-duration 30s
go tool pprof -top ./sync.cpu.pprof
```

Profiling is off by default. The profile covers the Llewm process, not child
Git processes, and stops when sync finishes or the duration expires, whichever
comes first. The default limit is 30 seconds; the maximum is five minutes.
The output must not already exist and is created with private permissions.
Profiles can reveal function names and file paths; keep them local or review
them before sharing, and delete them when finished. Automatic background syncs
are not profiled by this flag.

For a disposable 5,669-record Git fixture, run
`BenchmarkHistorySyncUnchangedGate` from the Continuity module. Its `hit`,
`deleted`, and `build_miss` cases distinguish a reusable completion proof from
a deleted proof and a different executable commit identity. The latter keeps
existing inventory and semantic replay proofs; it does not simulate a semantic
code change. Each case checks the expected sync path and zero imported/exported
records. For example, with an existing home-disk scratch directory:

```sh
go test ./internal/memsync -run '^$' -bench '^BenchmarkHistorySyncUnchangedGate$' -benchtime=10x -benchmem -cpuprofile=/path/to/scratch/sync.cpu -o /path/to/scratch/memsync.test
go tool pprof -top -cum -tagfocus=sync_case=build_miss /path/to/scratch/memsync.test /path/to/scratch/sync.cpu
```

Use `sync_case=hit` or `sync_case=deleted` for the other cases. These labels cover
only measured cycles, excluding setup, warm-up and proof invalidation; the CPU
profile also includes Go benchmark calibration cycles. CPU samples exclude
child Git processes and do not measure wall time. Synthetic fixtures do not
establish live sync speedups. Delete the scratch profile and binary afterward.

`BenchmarkHistorySyncPlanHistory` adds twenty synthetic, text-heavy plan
revisions to each 500/5,669-record Git fixture, then measures complete cycles
with one new decision. It covers repeated historical text processing that the
decision-only fixtures miss. Redaction uses a combined necessary-prefix check
to skip replacement scans only when no existing rule can match; possible
matches still run every original replacement in order. Differential fuzzing
compares this path with the replacement rules, including Unicode case folding.

The plan, mixed-history and remote mixed-history complete-cycle benchmarks also
report opt-in work counters per measured cycle. `published_block_bytes/op` counts
new block file writes to the local Git clone, including headers and repairs of
missing files; it is not network traffic or globally new knowledge.
`block_read_bytes/op` and `block_reads/op` count normal indexed tracked block
reads. Inventory, archive path, dependency verification, dependency snapshot
membership and Git tree entry counters count visits, including repeated visits
to the same history. `dependency_selected_records/op` counts closure output.
Git tracked-block observation verifies each block's inventory once while reading
it, then records archive provenance and publication membership from that exact
snapshot. It repeats the read after fetching; this reduces duplicate inventory
verification within an observation but still visits the tracked history.
`fallback_*` counters name observed fallback categories (such as changed local
generation, unavailable completion proof, inventory repair and full semantic
import); they do not yet cover every fallback branch or every local-state scan.
`archive_metadata_entries_visited/op` includes files and directories visited by
archive change guards, excluding the bookkeeping status directory.
When the saved capture generation differs, capture retains its initial metadata
snapshot for an eventual publication receipt instead of walking the archive
again before the first write. An unchanged generation avoids that map allocation;
the generation comparison is only a hint and cannot authorize reuse. Final
publication validation and the final archive/database guard still run, including
rejection of foreign arrivals between the initial observation and publication.
The retained map is speculative and may be unused when later eligibility checks
decline a receipt. This trades that allocation for one fewer metadata walk on
qualified publications; it does not make archive validation proportional to the
new records.
`local_state_records_visited/op`, `local_state_records_hashed/op` and
`local_state_payload_bytes_hashed/op` measure replay-guard hash construction,
including recovery guards. They exclude operational loading, encoding and other
canonical importer scans. Mixed-addition guards sort local identities and hash
each payload once to compute both the prior state excluding additions and the
complete renewed state. Both hashes preserve their existing proof encoding;
the complete local-state comparison and canonical import checks still apply.
Missing counters mean zero observed events. Metrics exclude fixture setup and
warm-up, contain no record contents or identities, and do not authorize reuse.
Use the same fixture shape and iteration count when comparing histories; these
counters expose remaining full scans rather than asserting bounded delta work.

## Authentication

Syncs run from session hooks and after checkpoints, where nobody can answer a
prompt. Git therefore runs with `GIT_TERMINAL_PROMPT=0` and SSH in batch mode. Only
an explicit `llewm sync init` run from a terminal lets Git prompt, and
`sync init` checks that the remote answers without a prompt before it saves the
configuration.

Supported options, in order of preference:

1. **What Git already does.** An SSH agent or an existing credential helper.
   Continuity configures nothing. Prompt programs such as `GIT_ASKPASS` and
   `core.askPass` can help explicit setup, but are disabled for automatic sync.
2. **An SSH key for memory syncs only.** `sync init --ssh-key PATH` stores the path
   as `[sync] ssh_key`. Syncs run SSH with only that key (`IdentitiesOnly`). A key
   with a passphrase must already be loaded in an agent.
3. **A credential helper in the memory clone only.** `sync init --credential-helper
   HELPER` writes the helper into the clone's own `.git/config`, after an empty entry
   that clears inherited helpers. Without the flag, `sync init` keeps an existing
   helper and otherwise selects libsecret when installed, even when askpass is
   configured. Git prompts once during interactive `sync init`, or `--token-stdin`
   supplies an access token, and the helper stores the result (in the keyring for
   libsecret). Setup then verifies access with all prompt programs disabled.
4. **A fallback prompt program.** `sync init --askpass PATH` (`[sync] askpass`),
   passed to Git as `SSH_ASKPASS`, which Git consults last. Automatic session and
   checkpoint syncs suppress askpass (including `GIT_ASKPASS` and `core.askPass`).
   They retain local commits and report authentication unavailable when credentials
   cannot be obtained without prompting. `llewm sync --prompt` can recover
   once from a desktop terminal; without a credential helper it does not repair
   future automatic sync. For an existing askpass-only setup, rerun
   `llewm sync init --remote URL --credential-helper libsecret` with the same
   remote to configure an access path available with askpass disabled, then verify plain
   `llewm sync` succeeds. Do not edit the memory clone by hand.
   The original per-project askpass fallback supported KWallet without a global
   `GIT_ASKPASS` export. Disabling askpass to prevent headless GUI failures also
   disables that wallet lookup; this does not mean the wallet has no saved
   credentials. A clone-scoped helper preserves Continuity-only configuration
   while supplying access with askpass disabled.
   Plain `llewm sync` uses the automatic no-prompt policy, including when
   launched by an older MCP process. Neither form changes stored configuration.
5. **Last resort: plaintext storage.** `sync init --plaintext-credentials` configures
   Git's store helper, which keeps the password unencrypted in `<clone>.credentials`
   (mode 0600, beside the clone and outside its working tree). `sync init` explains
   this and asks for a typed `yes` on the terminal. With no terminal (a scripted
   setup), the flag itself is the consent, and the explanation is still printed.

Invariants for every option:

- Continuity never changes global or system Git configuration, or a code
  repository's configuration.
- A secret never enters project configuration, the database, the memory
  repository, the remote URL, logs, or error text. `--token-stdin` holds the token
  in memory only long enough to hand it to the credential helper
  (`git credential approve`). Continuity keeps no copy, and Git's helper protocol
  erases a rejected token.
- Prefer credentials scoped to the memory repository that can be revoked (a
  repository-limited access token or deploy key) over account-wide ones.

## Timing and failure

- Session start queues a detached sync and builds its bootstrap from the last
  imported history. A checkpoint from another device may therefore need a later
  bootstrap after the sync finishes. Pushes after checkpoints also run detached.
- Only waiting on the remote is ever cut short. Local Git and database steps
  (export, commit, rebase, import, recording the attempt) always finish, so an
  interrupted sync never leaves a lock file, a rebase in progress, or a
  rolled-back import. A canceled Git gets SIGTERM with its transport helpers,
  never SIGKILL.
- An HTTPS remote is probed with a plain TCP connection first (1.5 s), so an
  offline machine is reported in about a second. Remotes behind a proxy are not
  probed; SSH uses `ConnectTimeout=5` and keepalives instead. Once connected,
  each fetch or push is limited to 2 minutes, and HTTP transfers below 1 KB/s for
  20 s are ended as stalled.
- Offline, records are committed locally and pushed later. Two versions of one
  record revision (two machines wrote it before syncing) become a revision fork:
  each machine keeps its own, every envelope remains an attributable observation,
  and semantic heads are keyed by record identity plus payload `content_sha256`.
  Re-encoding identical content for a later portable format therefore does not
  create or reopen a fork. Revision-resolution schema 2 names accepted and
  rejected content hashes; schema-1 envelope references remain readable through
  the observation ledger and migration appends a deterministic stable
  normalization before another format conversion. `llewm fork resolve
  TYPE:ID@REVISION` appends the accepted content as the next revision. A decision
  or narrative that two machines each superseded keeps both successors as a
  supersession fork until `llewm fork resolve FAMILY:ID` accepts one. Two
  versions of an unrevisioned record are reported as a collision. None of these
  is ever merged silently.
- A record whose type the running build cannot read (written by a newer
  Continuity) is skipped and counted, not refused. Everything else imports, the
  import watermark holds so an upgraded build picks the record up, and the `Sync:`
  line says how many records need a newer Continuity.
- `[sync] manual = true` turns off automatic syncs.

On Linux, if a client omits `DBUS_SESSION_BUS_ADDRESS`, memory Git subprocesses
can discover an existing `bus` socket in `XDG_RUNTIME_DIR` (or `/run/user/UID`
when that variable is absent). The directory must be private and owned by the
current user, and the socket must have the same owner. Explicit bus addresses,
including an explicitly empty value, are preserved. This starts no bus, enables
no askpass, and changes no global environment or Git configuration. A configured
keyring helper still controls its own wallet access; a locked or unavailable
keyring can require explicit setup before automatic sync succeeds.

## Other transports

Git is optional. [Shared-folder History sync](shared-folder-history.md) can use
an existing Syncthing/cloud-drive folder, with the operational database and
private record archive kept separately. Explicit per-destination relaying permits
hybrid Git/folder setups. Never place a managed Git memory clone itself in a
file-sync folder. The Git ownership/authentication rules above still apply to
the Git endpoint; folder publication does not establish provider delivery.
