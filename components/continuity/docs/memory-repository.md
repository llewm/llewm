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
  fetch, merge, import, and push, under a file lock. People and agents do not edit,
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

### Durable archive protocol

Schema 091 and writer API 137 define a project-scoped SQLite archive of exact envelope bytes and
append-only origin observations, separate from disposable block indexes. Envelope
hashes preserve competing versions of one logical record. A first-origin foreign
key requires body and provenance to commit together. Insertion also appends the
change journal and updates its sequence/counts in that transaction. Consumers use
checksummed, project/lineage/policy-bound receipts; uncertainty requires complete
validation rather than an inferred delta. Raw endpoint strings are not persisted
as provenance: origin and consumer identities are hashes.

SQL migration creates no active archive. New identity insertion creates an
inactive empty-archive marker in the same transaction; existing identities are
not backfilled. Only that fresh-identity proof and an absent legacy route allow
normal opening to initialize empty storage. A legacy identity or an interrupted
migration cannot silently become an empty replacement. Disposable endpoint status
is also stored in SQLite, separately from durable observations and import proofs.

Explicit storage migration must stage
verified observations and compare their complete content/provenance inventory
before activation. Full audit validates bodies, hashes, origins, journal coverage
and counts without consulting disposable indexes. Ordinary access verifies the
selected bytes; changed journal epochs invalidate old consumer receipts. Archive
rows cannot be silently replaced as cache repair. Existing loose-file archives
require the supported, backup-gated storage migration. Normal opening then checks
bounded exact fence bytes, and capture binds its canonical cursor to the archive
epoch. Capture writes and acknowledgement share a reserved writer transaction.
Counter changes unrelated to the exact appended journal event invalidate that
epoch, so damaged tracking cannot reuse canonical capture proofs. Old CLI writers
own a different storage path; drain them before the writer137 cutover.

The legacy migration reader opens an existing archive without creating it. It
requires the exact lineage marker, canonical file framing, and at least one
valid origin marker per envelope. It rejects orphan provenance, abandoned staging
files and unaccounted authoritative entries. Two complete content/provenance
inventories must agree; this is portable and does not depend on inode timestamps.
Endpoint status files are disposable. Repeated staging preserves the same bytes
and origins, and never activates or deletes source files. The coordinator still
must stop writers, verify backups, and fence the old storage route before
activation. Reading a stable snapshot alone is not writer coordination.

The cutover coordinator records immutable, checksummed preparation in
the archive state. It creates a non-overwriting SQLite backup, checks its
integrity, foreign keys and project identity, and hashes the verified backup.
Source staging and its recovery receipt commit together. Completion verifies
the backup and staged archive before retiring the source to a distinct sibling
directory. An exclusively published, durable marker blocks the old archive path
from being recreated by legacy writers. Only a reverified retained source and a
matching SQLite audit permit activation. Recovery can resume after preparation,
rename, marker publication or activation; competing source directories, changed
backups and mismatched receipts fail closed. Retained sources and backups are
never overwritten or deleted by cutover. A backup created before preparation
commits may remain unregistered after interruption; a new preparation must use
a fresh backup destination rather than infer ownership of that file.

Normal Git sync uses bare storage and reads bounded, hash-verified commits, blobs
and trees without an index or working tree. Tree entries retain their modes;
selected transport blobs must be regular non-executable files. A tree delta
skips equal subtree identities. Three-way merging reuses unchanged trees and
rejects conflicting immutable leaves instead of text-merging record bytes.
Namespace policy and new-object protocol validation must precede publication.
Setup creates native bare clones. Existing working clones require explicit
storage migration; normal opening never converts them or initializes replacement
history. A migrated clone requires its exact owner, preparation and completion
receipts before use. Reading an incomplete cutover fails closed.
The explicit full storage audit validates supported old wire formats as well as
format 5, reports the selected lineage's formats and whether conversion is still
required. This lets storage cutover finish before wire conversion; ordinary sync
still refuses old formats.

The bare-clone cutover coordinator takes nonblocking migration and
legacy sync locks and inventories the clean source, all resolved refs, immediate
symbolic-ref links, object format and private configuration. It refuses dangling
or unsupported loose refs rather than losing refs omitted by Git's listing.
An immutable checksummed preparation binds sibling stage and retained-source
paths, the configured remote and source hashes; receipts contain no credential
values. An owned stage can resume initialization and fetch without deleting
partial storage. All refs are fetched locally, symbolic links restored and
private configuration retained with only the bare/worktree settings changed.
Both configured fetch/push URLs and the local source route must remain exact.
Strict object checks and a second source inventory precede activation. The
original working clone is retained, namespace switches and completion are
durable, and interrupted retirement/switch/completion can resume. No index or
object alternates connect the bare clone to the original. Native clones and
conversion stages ignore Git templates; normal opening refuses alternate object
stores, shallow boundaries, grafts and shared common directories. Runtime
qualification is on Linux; Windows lock code is compile checked only.

The supported local interface is `llewm sync storage`:

- `plan --save FILE` inspects source storage without changing it and exclusively
  saves a device-private, checksummed recovery plan. Without `--save`, it only
  prints the plan. A bound portable project identity is required. The plan binds
  the physical project root, database schema transition, configuration, lineage,
  backup destinations and primary Git clone; keep it outside managed storage.
- After draining **every** writer using that database, archive or shared clone,
  `prepare --plan FILE --writers-stopped` stages Git locally, creates and verifies
  the pre-schema backup and its durable `.verified` receipt, explicitly applies
  the exact planned schema migration, then backs up and stages the archive.
  Schema migration affects the entire configured database; the plan reports its
  project count. Archive migration covers only the selected project.
- `complete --plan FILE --writers-stopped` rechecks the backup and Git source,
  retains and fences the old archive, switches the owned bare clone, and performs
  full local audits. Repeat the same command with the same plan after an
  interruption. Retained sources, verified backups and receipts are preserved.
- `audit` verifies active archive bodies, origins, journal coverage and counts,
  all reachable Git objects and refs, and the selected lineage's current blocks.
  It does not contact remotes or certify peer delivery or semantic import.

These commands do not stop processes or authorize a live cutover. Ordinary
`database.Open` and read-only opening never migrate a pending schema. Planning
uses a narrowly scoped read-only migration inspection path; preparation is the
explicit backup-gated transition. A prepared backup is bound by SHA-256 as well
as SQLite integrity and schema checks. If a crash leaves a backup without its
published verification receipt, retain it and create a fresh plan instead of
inferring ownership. Do not remove recovery evidence to force normal opening.
The audit may acquire a local clone coordination lock; it does not alter history.

Schema 092 adds project-scoped mutation tokens for block inventory, envelope
metadata, references, memberships and publication intent. Canonical history
changes leave these tokens alone; raw inventory changes, including moves between
projects, invalidate them transactionally. Rollback restores the prior token.

Schema 093 separately tracks dependency selector mutations. Selector maintenance
does not invalidate canonical capture or transport completion proofs; semantic
journal replay binds its own baseline to this token. Missing, changed or unknown
triggers on the selector tables require complete semantic validation.
Its covering envelope membership index also lets publication stream at most
257 memberships before checking endpoint markers. A larger fanout requires
reference materialization, including when none of those blocks has a marker.
Schema 094 adds exact recovery-identity lookup. Complete recovery readers check
that lookup metadata matches the correction's validated immutable proof.

Git observation receipts bind the build, physical clone, endpoint, lineage,
index interpretation, SQLite schema, inventory generation and durable archive
epoch. Before reuse, installed archive/inventory tracking triggers must match
their embedded migration definitions. Missing or changed tracking requires
complete transport validation and a durable archive audit; unavailable tracking
cannot issue a reusable receipt. Verified blocks, inventory repair and provenance
commit in one writer transaction. Publication can advance a receipt through its
own verified inventory writes only if the prior stamp still matches under that
writer lock. It never advances the observed Git tree before a Git commit exists.
Interrupted staged intent remains publishable on the next cycle.

A known observation reads only changed selected blocks through the Git tree
delta. Previously observed bytes remain covered by explicit full audits;
out-of-band corruption of an old object is not promised detection on every
cycle. Publication consumes the archive journal under a reserved writer, with at most
4,096 changes and 512 pending block hashes per receipt. It stages only newly
authorized uncovered observations, appends stable local semantic-catalog blocks,
and acknowledges the exact archive snapshot in that transaction. Pending intent
is removed only after a verified local Git tree contains its immutable block;
remote delivery remains governed by fetched refs and push outcomes. Observation
hands off exact before/after inventory stamps so it cannot bless intervening
unknown mutations. Missing, damaged, oversized or stale tracking takes complete
materialization, without acknowledging an oversized pending set.

Completion proof version 5 requires a current publication cursor with no pending
local intent. Normal changed cycles can reuse guarded archive and canonical
journal baselines. Missing or invalid tracking and large or sensitive dependency
closures use the complete reference path. Git format 5 bounds selected block
tree nodes; namespace ancestor listings still depend on the number of unrelated
versions and lineages in the same repository.

Private commit primitives independently verify bounded commit bytes before
using tree/parent identities. A merge requires namespace policy validation,
including before fast-forward adoption, and retains both divergent parents.
Unrelated initial histories can join; multiple merge bases refuse rather than
selecting an arbitrary base. Branch publication checks the resolved HEAD and
expected tip, requires ancestry preservation and uses a durable compare-and-swap
without replacing symbolic links. These primitives create no index or working
tree. Normal publication and fetched-history adoption use these primitives.
Merge commits can make the unpushed commit count larger than the number of new
record blocks. Format-2/3/4 namespaces refuse ordinary adoption; explicit conversion
validates and archives every selected observation before one retirement commit.
A stale peer retaining an old namespace must also run explicit conversion.

Block-object publication validates exact format-4 block frames, appends
only canonical immutable identities and preserves unrelated root entries and lineage
trees. Arrival validation opens only changed selected subtrees, checks every new
blob's Git identity and block/envelope hashes, and rejects block replacement or
removal. Unsupported selected-lineage formats refuse; other lineages remain
opaque. Object writes request fsync before subsequent commit/ref publication.
Git format 5 replaces flat block paths with adaptive radix trees.
Leaf buckets hold at most 64 full-hash block names; larger buckets branch on
successive hash bytes, with at most 256 children per node and 32 levels. Appends
split only overflowing buckets. Delta validation permits structural relocation
only when every existing hash retains its exact Git blob identity, and rejects
removal, replacement, mixed nodes and noncanonical bucket sizes. New subtrees
receive complete structural validation; previously verified unchanged subtrees
are reused by identity. A concurrent merge can temporarily produce an oversized
or mixed bucket. Before committing, the runtime normalizes only trees created by
that merge and validates that the local parent's block identities survive.
Unchanged input subtrees are not walked during normalization. Publication
membership uses a hash-prefix point lookup instead of listing the full inventory.
The block frame codec and shared-folder format remain 4; only Git paths change.

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

The global component also binds SQLite's schema version. Before generation or
cursor reuse, installed canonical change and journal counter triggers must match
their compiled migration definitions. Missing, modified or unknown guards disable
cursor acknowledgement and completion proofs, requiring complete capture.
Restoring the original definitions still invalidates prior receipts because the
missing interval may have hidden writes. Schema repair remains explicit.

Migration 090 resolves checkpoint action transition ownership through its parent
action, matching the portable export reader. Appending or carrying an action
therefore invalidates only its project and can use selected capture. Reassigning
a transition journals both owners; updates and deletions still require complete
export. Missing parent ownership retains global invalidation. Existing history,
cursor receipts and the wire format are unchanged.

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
changes or affected identities, unknown families and more than 128 selected
identities in a table retain complete validation. Journal reads and the total
selected export are additionally bounded to 4096 changes and identities.
Complete export starts with a read-only snapshot. When new identities need
retention, it releases that transaction and reserves the writer on the same
pinned connection. An unchanged SQLite `data_version` permits reuse of the
loaded specifications and validated encodings; another connection's commit or
an unknown version requires a fresh complete snapshot under the writer lock.
Unchanged exports continue without acquiring the writer.
Capture may advance its archive stamp across its own new
record/provenance files only after accounting for the exact additions, validating
their bytes, and checking that all pre-existing files retain their metadata
identities. Unexpected changes, including concurrent foreign provenance, decline
the optimization. Parent directories may change timestamps and size but must
retain their device, inode and mode. Tracking starts only before an actual file
creation; platforms without change-time identities retain complete validation.
Observing an endpoint block can also extend the capture stamp for its new
destination markers when every record and its local provenance were already
captured. The receipt validates all existing metadata and the exact new marker
bytes, then rechecks the archive stamp. It checks the bounded journal twice in
the observation's writer transaction and commits the cursor with membership;
canonical changes, foreign arrivals, damaged tracking, unexpected filesystem
changes or rollback leave the old receipt untouched. Unchanged observations
perform no additional metadata walk. This avoids a full capture on the next
consecutive local arrival without treating observation as semantic import proof.
Semantic import has an independent journal cursor. After a complete successful
reference import, a private snapshot receipt can establish a baseline binding the
archive position, canonical position, schema and dependency selector generation.
The baseline is saved only if the verified raw projection and complete archive
snapshot still match under the writer lock. A concurrent arrival remains pending.

Full reference replay validates incoming bytes before reserving the writer, then
commits recovery records, fork observations, main canonical records and import
receipts as one semantic unit. A semantic error, identity collision, cancellation
or failed commit rolls back every new semantic write from that attempt; reported
import/fork counts are cleared. Previously committed facts and received transport
history remain intact. Deferred unknown schemas still remain transport-only.
The writer stays reserved through semantic validation, so other writers can wait
longer; readers retain their own WAL snapshots.

Optional journal-baseline seeding uses a savepoint inside this transaction.
An ineligible baseline discards its provisional derived writes without undoing a
successful semantic import. A real SQL/commit failure rolls back the semantic
unit. Seeding verifies canonical, archive and projection positions again after
its writes, including receipt-trigger side effects, before committing a cursor.

Subsequent eligible imports traverse indexed forward and reverse typed dependency
ranges and read only reached archive bodies. Canonical journal selections must
match exact archived bytes before and after application. Canonical rows, retained
wire envelopes, import receipt, selector updates and both consumer cursors commit
in one transaction; statement or commit failure leaves the previous baseline.
Unexpected canonical, selector or raw projection side effects decline replay.

Replay is bounded to 128 bodies and 4096 journal changes or dependency rows.
Missing, damaged or crossed receipts, competing envelope versions, supersession
changes, recovery records, unsupported input and larger closures use the complete
reference importer. A complete baseline uses the reference importer's recovery
normalization and narrow roadmap schema-equivalence rule. Competing heads must
already exist in both fork stores. Retired records remain archived and ignored;
unknown deferred input prevents a baseline. Every supplied semantic identity must
match canonical state, and omitted canonical records prevent seeding except for
local invocation activity.

Historical corrections and resolutions carry conservative dependency guards.
Correction guards include the subject and dependencies in both original and
additionally redacted payloads. Reaching a guard requires reference replay.
Dependency rule versions are independent of block index versions. Complete
validation repairs disposable selectors and can resume replay afterward.

Only a journal consumer holding this guarded baseline can scope recovery
comparisons to selected identities and reuse an unchanged validated correction
graph. Its post-apply canonical journal checks reject unexpected recovery writes
before commit. Ordinary imports keep complete recovery comparisons and graph
validation. Recovery lookup probes and checked recovery rows are counted
separately, so a hidden full graph read cannot appear as bounded point work.
Canonical capture can also scope recovery lookup after a complete export has
established its consumer cursor. Corrected selected identities and recovery
changes require complete export; independent identities use exact lookup probes.
Before returning a new cursor, export checks the global journal range across its
retention transaction: only its selected envelope insertions may occur, exactly
once each. It verifies their actual stored bytes too. Extra or foreign writes,
suppressed insertion and fabricated journal events cause rollback and an explicit
error. Failed SQLite commits clear their native transaction on the pinned
connection before it returns to the pool. The archive writer shares this cleanup.
Journal pruning is not enabled.
Existing proofs are discarded by
migration 086; capture-v2 does not reuse earlier capture proofs.
Migration 082 narrows attribution tracking: raw invocation activity invalidates
only when the invocation is already retained or referenced by indexed history.
Adding a referring record invalidates independently. The generation also records
the last invalidating trigger name for device-local diagnosis.

After a successful, stable cycle with no pending semantic or delivery work,
later cycles can reuse its proof. They still verify the configured remote,
fetch, compare the verified lineage subtree identities, and check local database
and archive state again. There is no index or working tree to inspect. An unrelated
lineage's commit does not require record replay. A missing or invalidated proof,
changed policy or build, local edits, pending push, or uncertain state runs the
full workflow. Explicit conversion always runs the full workflow.

The SQLite archive guard reads its epoch, journal sequence and counters without
walking history. Selected archive bytes are verified on access; full audits
detect old-object damage. An unchanged proof does not promise every-cycle
detection of out-of-band changes to previously verified immutable bytes.
Folder endpoints retain conservative provider validation and can reuse a local
completion proof too: the configured marker/lineage, endpoint contents, archive stamp, database token,
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

All transports share a complete local import snapshot with stable block
membership for published records and records received through other destinations.
Its device-local `local-snapshot:` membership covers each envelope once, so a
foreign arrival adds a group for new records without repacking old history.
This membership never grants transport provenance or relay permission. Normal
cycles validate catalog block frames; archive audits independently verify durable
bytes. Damaged inventories follow the existing repair path.

Canonical import planning compares small bundles against selected operational
identities, using the same primary readers as journal export and validating
their retained wire bytes. This comparison does not certify omitted local state.
Callers requesting a complete snapshot, recovery graphs, unsupported selections
and large bundles retain complete reads. Transaction reuse and apply-time
dependency checks still protect the selected plan against concurrent writes.

Migration 089 stores one immutable envelope body and reference set per project
and envelope hash. Blocks contain only membership rows; overlapping blocks share
content. Removing the last membership collects its body and references, while
repairing one block preserves other blocks and endpoint publication intent.
The migration removes only obsolete unpublished blocks whose every exact envelope
is covered by current published inventory. Uncovered data and all staged intent
survive. Conflicting content for one hash aborts the migration. Semantic proofs
are invalidated; canonical observations and transport format remain unchanged.
This schema requires writer API 136: drain old writers on every active host before
migration, retain verified backups, and verify fresh clients after the cutover.
Physical database compaction is a separate SQLite maintenance operation after
logical cleanup; free pages alone do not shrink the file.

Object discovery reuses immutable Git tree nodes across locked block cycles.
Only a committed branch tip supplies membership; loose files and abandoned
indexes cannot enter publication. Durable archive publication intent survives
an interrupted object/tree/commit/ref boundary and is replayed on retry.
Raw tree objects are cached locally under the bare clone's `status/git-trees`.
Each read verifies the complete bytes against the selected Git SHA-1 or SHA-256
object ID before parsing membership. Only missing, changed or damaged nodes
are fetched from Git again; cache writes are optional. Discovery limits fail
closed instead of accepting an incomplete delta. Every accessed block undergoes
Git object identity and content validation, so a tree cache cannot conceal
missing or corrupt selected blob bytes. Cache contents are bounded per object and
root-relative file access confines local I/O. Cached node reads, path enumeration
and complete content validation still visit old history. This reduces repeated
Git discovery; it does not establish fixed-delta scaling or replace full recovery.

Lease refreshes retain a disposable raw-commit receipt for each fetched scope
tip in device-local archive status. Every reuse verifies the exact Git hashes,
commit structure, empty tree, event schema, parent chain and scope/ref binding;
decoded events alone are never trusted. Receipts contain at most 20 commits and
64 KiB of serialized status. Missing, damaged, changed or oversized receipts
fall back to Git. Cache write failures leave synchronization functional. One
lazy `cat-file --batch` process serves all cache misses in a locked refresh,
with bounded headers and object bodies, and is closed and reaped on exit.
Scopes share the verified empty-tree read. The fetch still establishes ref
freshness, and writes still compare-and-swap the exact remote tip. History and
lease fetches remain separate: lease failures are advisory sync notes, whereas
history-fetch failures stop history processing; independent lease operations
also require their own fresh read.

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

Complete import planning and replay proof snapshots validate saved curated
history and its invocation dependency closure. Roots include incoming invocation
identities and references, retained invocation identities, raw curated bindings,
and recovered original identities and references. Retained or recovered wire
references can expand the closure; parent discovery and body validation stay in
the caller's transaction until the closure is complete. A required malformed
invocation still blocks validation. Unrelated raw invocation activity remains
stored and does not block curated sync, regardless of bundle size. Export
selection and transaction/generation guards are unchanged.

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
  blocks under `v5/<lineage>/blocks/`. A bucket with at most 64 blocks stores full
  `<block-sha256>.block` names; larger buckets use successive two-hex-digit hash
  prefix directories. There is no
  SQLite, no device-private data (raw events, sessions, usage, hook telemetry), and no
  credentials.
- A project that opts in to cross-machine leases (`[leases] remote = true`) also
  keeps one ref per lease scope at
  `refs/continuity/leases/<lineage>/<scope kind>/<sha256 of scope ID, 32 hex>`.
  Each points at a chain of commits with the empty tree, one per grant event
  (acquire, renew, release), with the event as JSON in the message. Continuity
  writes them only with `git push --force-with-lease=<ref>:<tip read>`, so the
  remote applies at most one of two racing writes. They live outside `refs/heads`,
  so the history branch, its merges, and its pushes never touch them, and the
  remote must accept pushes to custom refs. Like the history branch, they are
  never edited or deleted by hand.

## Format 5 conversion

Format 5 gives Git blocks a bounded adaptive tree layout. Format-4 block frames
retain their exact bytes, hashes and Git blob identities. Format-2/3 observations
use the existing explicit envelope conversion and packing rules.
Ordinary Git sync refuses older tracked records for the selected lineage.

Migration 095 advances the local writer floor to API 138. Existing older
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

The converter fetches before retiring the old partition and validates each
tracked observation's bytes, lineage, hash and path. It packs every v3 envelope,
including remote-only records and rejected heads, without changing its bytes.
It relocates every format-4 block without repacking it, and retires only this
lineage's old namespaces in a parent-preserving commit. Other lineages and opaque
root entries retain their identities. Original commits remain ancestors, and
observations remain in the local archive. Format-2 records first pass through
the existing explicit format-3 conversion. A partial conversion may retain both
old and new namespaces; repeating the explicit command validates their union
and finishes the retirement. A peer with an old local namespace must also use
explicit conversion before ordinary sync.

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
decision-only fixtures miss. Redaction searches assignment and URL delimiters
for the first necessary rule prefix, rather than running a regexp search across
every prose position. It preserves ASCII regexp whitespace and Unicode simple
case folding, including long s and Kelvin sign. Possible matches still run every
original replacement in order. Differential tests and fuzzing compare both the
prefix byte offset with the previous regexp and the final text with all original
replacement rules. `BenchmarkCandidateSearch` compares these prefix searches;
its synthetic results do not establish complete live sync latency.

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
`fallback_*` counters name observed fallback categories, including capture cursor
and publication guards, delta selection/snapshot/recovery/missing actors, export
writer retries, unavailable completion proofs, inventory repair and full semantic
import. They count the observed branch, not a unique cycle or every possible
fallback. `capture_full_exports/op` counts full-export requests from capture,
including the ordinary first capture and repairs after a failed delta receipt.
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
`import_invocation_closure_rounds/op` counts curated import snapshot passes,
including passes repeated for references discovered in retained wire forms.
`import_invocation_metadata_rows/op` counts parent-discovery rows, and
`invocation_rows_loaded/op` counts invocation bodies scanned by the shared loader
(including exports and repeated passes). These count attempted work, not unique
identities or proof completeness.

`operational_full_loads/op` counts complete operational snapshot load attempts;
`operational_delta_loads/op` counts selected loads that reach specification
assembly. `operational_specs_loaded/op` counts assembled envelope specifications
before retained/recovery overlays, including repeated work after writer retries.
These are logical records, not SQL rows: child rows and partial loads that fail
before assembly are not included. `snapshot_envelope_encodes/op` counts fresh
encoding attempts of assembled specifications, and `snapshot_encoding_reuses/op`
counts reuse of an encoding already validated in the same snapshot. Encoding
performed while validating a retained or recovered envelope is covered by the
retained/recovery check counters instead.
`export_writer_snapshot_reuses/op` counts complete export snapshots reused after
the same-connection writer guard. `fallback_export_writer_retry/op` counts full
reconstructions after that guard detects a commit or cannot prove equality.
`retained_envelopes_checked/op` and `retained_envelope_bytes_checked/op` count
retained overlay validation requests and input bytes, including failed validation;
the corresponding `recovery_*` counters cover recovery substitutions. These do
not count every encoder/decoder elsewhere in transport or canonical importing.
Transport decoding still fully canonicalizes untrusted payload and index JSON,
validates routing metadata and reference order, and checks both hashes. It then
reconstructs the fixed canonical envelope layout once, omits the outer hash member
from the hash input, and compares the result with the received bytes. Duplicate
fields, alternate escapes and unsupported integer representations remain invalid;
unknown record schemas retain their existing transport-only handling. The generic
canonical encoder remains the independent test reference.
Track-plan loading and bundle decoding also reuse the pure safe-text predicate
for repeated exact strings within that one load or decode. This cache holds at
most 1,024 cloned keys totaling 1 MiB; oversized values and capacity misses use
the ordinary predicate. Required-field checks, structural validation, reference
and index comparisons, dependency checks, and current database reads still run
on every record. The cache is neither persisted nor used as a semantic receipt.

During complete semantic journal replay, a call-scoped cache can reuse the pure
canonical/hash decode of an exact retained envelope. It is limited to 8,192
entries and a conservative 64 MiB decoded-storage charge; a full cache uses the
ordinary decoder. `snapshot_decode_cache_misses/op` counts successful fresh
validations and `snapshot_decode_cache_hits/op` counts exact byte reuse. Every
fresh snapshot still rereads its operational and retained rows, checks identity and
lineage, resolves recovery, and runs its normal semantic/transaction guards.
Cached results own private buffers; callers receive independent copies. The
cache is discarded after this replay call and provides no local-state proof.
Atomic reference replay can additionally reuse one complete validated curated
snapshot within that exact transaction and project. This separate cache charges
at most 64 MiB and owns its buffers; misses or an unavailable guard require a
fresh ordinary load. Missing canonical tracking, unreviewed or modified main views,
or TEMP table/view shadows disable reuse. Reuse requires unchanged SQLite `total_changes()`, main and
TEMP schema versions, and the evaluation time fixed for this operation. Any
write, including an unrelated or trigger-induced write, invalidates it. Incoming
invocation roots must be covered by the validated dependency closure; new roots
require another full load. Incoming envelopes, dependencies and comparison plans
still pass their normal validators. The frozen evaluation time applies only to
this transaction's derived usage summaries. The cache cannot cross transactions,
change the curated actor scope, establish a persistent proof or relax exact-commit
binding. The two caches have independent 64 MiB limits.

Revision-fork comparison can use the revisioned records from that same guarded
curated snapshot. It validates the same revisioned records and a superset of the
ordinary export's invocation dependencies; extra incoming actors are excluded
from this view. A missing or invalidated snapshot uses the ordinary export reader,
including its normal dependency checks. This view does not change export scope.

`atomic_import_snapshot_loads/op` counts snapshots retained after full validation;
`atomic_import_snapshot_reuses/op` counts same-transaction snapshot copies;
`atomic_import_snapshot_invalidations/op` counts changed/unavailable guards;
`atomic_import_snapshot_bounds/op` counts capacity refusals;
`atomic_import_snapshot_guard_refusals/op` counts unavailable/uncertified guards.
`atomic_import_revision_snapshot_reuses/op` counts revision-fork views obtained
from the guarded snapshot without another ordinary export load.
These describe work, not new records or proof of a successful commit.
`atomic_semantic_seed_sideeffects`
counts unexpected canonical changes detected after optional baseline writes.

The context-scoped collector lives in `internal/syncwork`, shared by portable
loading, archive validation and transport; it persists no measurements.
`publication_envelopes_reused/op` counts staged publication entries whose exact
bytes match the permitted, validated archive snapshot. Those entries reuse its
parsed envelopes while retaining complete block reconstruction and hash checks.
`publication_envelope_decodes/op` counts identity decode attempts for entries
outside that exact snapshot, including entries no longer permitted for relay.
Neither counter implies that publication membership or archive scans are bounded
by the new arrivals.
Publication planning consumes the ordered SQL catalog one block at a time,
discarding each block's temporary SQL bodies after validation. The complete
validated archive snapshot and returned publication plan still occupy memory;
this bounds only the additional Go catalog buffer, not total sync memory or work.
Missing counters mean zero observed events. Metrics exclude fixture setup and
warm-up, contain no record contents or identities, and do not authorize reuse.
Use the same fixture shape and iteration count when comparing histories; these
counters expose remaining full scans rather than asserting bounded delta work.

Complete Git observations reuse only envelopes freshly decoded from their exact
block bytes in that call. Duplicate archive insertions compare the durable bytes,
record identity and initial provenance against that validated input. The final
audit still reads every durable body, origin and journal entry and checks all
maintained counts; bodies absent from the observation are decoded independently.
`archive_verified_bytes_reused/op` counts duplicate insertion comparisons;
`archive_audit_verified_bytes_reused/op` counts audit comparisons against fresh
input, and `archive_audit_decodes/op` counts independent audit decodes. Neither
reuse survives the call or trusts exported parsed envelope fields.

Archive replay compares existing rows in batches of 32 before preparing any
per-record writes. The entire input must match exact bytes, identity, initial
provenance and the requested origin before this read-only shortcut succeeds.
Any mismatch, duplicate input, uncertainty, unreviewed view or TEMP shadow uses
the original sequential path from its beginning. In particular, a batch needing
even one write cannot use prefetched matches for its other records; unknown
triggers must retain their effects on later comparisons. This is not an archive
audit or a persistent receipt. `archive_match_queries/op` and
`archive_match_records/op` count attempted comparisons;
`archive_match_shortcuts/op` counts completely matching inputs.

Index insertion compares stored envelope bodies, identities, membership and
reference sets in batches of 32 within the caller's transaction. Completely
matching blocks need no writes. When only memberships are missing, inserts can
also use 32-row batches if the entire membership-trigger effect chain matches
the reviewed definitions from migrations 086, 089 and 092. Unknown main or TEMP
triggers, changed/missing definitions, or any body/reference mismatch use the
original sequential repair path from its beginning, preserving trigger effects
on later rows. This gate grants no reusable verification receipt; exact-commit
proof binding and full audits remain.

`inventory_match_queries/op` counts attempted body/reference batch reads;
`inventory_match_records/op` counts their scanned body rows, including repeated
checks. `inventory_match_shortcuts/op` counts fully matching write-free blocks.
`inventory_membership_batches/op` counts attempted batched insert statements,
including failed attempts. They do not count unique records or certify a commit.

Cold archive snapshots collect canonical envelopes during that same complete
audit, then select permitted provenance in one query within its read transaction.
This removes repeated envelope reads, decodes and per-envelope route checks;
local/source/explicit-relay policy and hash ordering are unchanged. A failed
audit exposes no partial snapshot. Under the same reserved writer, ingestion and
indexing compare exact bytes, metadata, provenance and membership before attempting
inserts or repairs. Already matching entries avoid no-op writes and their trigger
compilation. The SQLite driver recompiles native statements even through Go's
prepared-statement interface, so that interface alone does not remove this cost.
No body or comparison cache crosses transactions. Full archive work remains
linear in retained history.

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
  (export, object publication, merge, import, recording the attempt) finish their
  transaction or ref-update boundary. An interrupted sync releases its lock and
  retains durable objects and publication intent for retry. A canceled Git gets SIGTERM with its transport helpers,
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
