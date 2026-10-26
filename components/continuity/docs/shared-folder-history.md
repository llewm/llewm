# Shared-folder History sync and hybrid transports

A shared-folder endpoint exchanges curated immutable records through an existing
local directory. Syncthing or a cloud-drive application can replicate that
directory. Git is optional: a folder-only machine needs no memory Git repository,
Git credentials, daemon, cloud API integration, or local model from Llewm.

This is an integrity-checked ingestion and retention workflow, not an unconditional
delivery, confidentiality, or backup guarantee. Llewm never synchronizes the live
SQLite/WAL/SHM files. Keep the live store and its private archive outside the
selected shared directory, and exclude live state from any enclosing provider
folder separately. Do not synchronize a managed memory Git clone with a file-sync
application.

## Start a folder endpoint

Choose a private folder that already exists and whose sharing membership you
understand. It must be separate from the live state, database, backups, `.git`
and managed memory clone. The directory must be real rather than a symlink.
Llewm does not configure the external provider, add sharing recipients, change
firewalls, or edit provider ignore rules.

From the project with existing history:

```sh
llewm sync status
llewm sync folder add personal \
  --directory /private/synced/llewm-history \
  --create --confirm-lineage LINEAGE
llewm sync --output json
```

The project must already have a portable identity. For genuinely new independent
history only, `llewm sync init` creates it. A receiving install instead explicitly
joins the known source lineage with `llewm sync init --lineage LINEAGE`.

`--create` writes a new immutable `.llewm-history.json` marker into the existing
directory. The marker identifies the container, independently of any project.
Local endpoint configuration pins that store ID and the selected project lineage.
One container can hold independent A/B histories: add the existing container to
each project with its own `--confirm-lineage` and the same `--store-id`.
Each operation scans and publishes only its selected lineage. All readers of the
container can nevertheless read every lineage in it, so use separate containers
and provider access controls for separate audiences. Include the marker and the complete
`v3/` subtree in external replication; do not filter out its hidden marker.
Setup does not itself import or publish record objects; `sync` and subsequent
automatic triggers perform those operations.

On another machine, first let the external application deliver the marker, then:

```sh
llewm sync folder inspect /private/synced/llewm-history
llewm init
llewm sync init --lineage LINEAGE
llewm sync folder add personal \
  --directory /private/synced/llewm-history \
  --store-id STORE_ID_FROM_INSPECT --confirm-lineage LINEAGE
llewm sync --output json
llewm bootstrap --checkpoint-id EXPECTED_CHECKPOINT
```

Follow the Git exclusion prerequisite before project initialization. Inspect the
folder's sharing scope and source history before configuring automatic import.
Adding an endpoint explicitly authorizes curated import and publication for that
selected lineage and destination. Never start a trial or generate a replacement
lineage/plans first on an install intended to receive existing history. Ordinary
`init` leaves lineage unset; explicit joining creates a new local instance identity.
A different existing lineage or different pinned store marker is refused.

Configure the client and verify fresh-client recovery of the expected checkpoint
before treating onboarding as complete. Imported verification describes its source
observations; it does not verify the receiving filesystem, activate permissions,
or authorize executing stored plans/procedures/next actions. Trial/exit states
continue to prohibit History-sync setup and execution.

## Hybrid configurations and destination authorization

For a private aggregate holding independent projects and separate group
destinations, use [project-level portfolio federation](portfolio-history.md).
That manual routing catalog can give one project several Git or folder destinations without
changing the per-project configuration below. It does not selectively publish
members of a unified history.

A project can configure one Git memory remote and up to eight named folder
endpoints on a machine. Other machines may configure a different subset. For
example: a folder-only desktop, a laptop with that folder and Git, and a Git-only
workstation. At least one bridge must eventually reach both sides for history to
converge. Llewm does not infer or discover bridges.

Relaying is **off by default for each destination**. A device's existing local
curated baseline and later locally originated records can be published to its
configured endpoints. Records observed through an endpoint can be republished to
that same endpoint after deletion. Sending those retained observations to another
destination requires that destination's relay authorization:

```sh
llewm sync relay git --enable=true --confirm-lineage LINEAGE
llewm sync relay personal --enable=true --confirm-lineage LINEAGE
```

The first command allows retained folder history into the configured Git remote;
the second allows retained Git/other-folder history into `personal`. Creating or
joining a folder can also use `--relay`. Enabling relay covers the retained
curated stream for this lineage, including unresolved versions and structurally
valid current-format records this build cannot interpret. Confirm every
recipient/destination's access policy. Relayed records preserve original canonical
bytes, hashes, IDs and attribution; a bridge does not become their author or
apply destination-specific redaction a second time.

Disabling relay uses `--enable=false`. It prevents future forwarding of records
not otherwise eligible for that destination; it does not retract copies already
published. Configuring a different Git remote clears its previous relay grant.
Removing a folder endpoint with `llewm sync folder remove NAME` removes local
configuration only, preserving all shared files and local history. A later
configuration of a destination requires explicit setup again.

The device-local configuration is `.llewm/continuity/config.toml`:

```toml
[sync]
manual = false
# Optional Git endpoint; omitted entirely for folder-only setups:
# remote = "git@host:owner/history.git"
relay_to_git = false

[[sync.folders]]
name = "personal"
directory = "/private/synced/llewm-history"
store_id = "32_lowercase_hex_characters_from_the_marker"
lineage_id = "lineage:32_lowercase_hex_characters_of_the_selected_project"
relay = false
```

Set `[sync] manual = true` to disable automatic triggers for all endpoints on
that device. Every process using a hybrid store must run a shared-folder-aware
build; older binaries ignore the new endpoint configuration and cannot preserve
its relay provenance. Stop old sync processes and restart clients when enabling
this feature on an existing store. Restart long-running clients after configuration changes so their
checkpoint callbacks observe the chosen configuration. Local folder endpoints
provide eventual history exchange, not cross-machine lease coordination. Remote
lease fencing still requires the separately configured Git coordination feature.

## Publication, ingestion, and status

The same content-addressed current-format objects used by Git are stored under
`v3/<lineage>/records/<type>/<identity-hash>/<envelope-hash>.json`. No provider
mtime, mutable shared manifest, global cursor, distributed lock, or arrival order
is authoritative. Local publication stages/syncs bytes and uses exclusive
no-replace hard-link publication; filesystems that cannot provide the operation
report failure rather than falling back to overwrite. A provider's replication
of those files is a separate operation with its own semantics.

Readers confine filesystem access to the selected root and reject symlinked,
nonregular, malformed, oversized, wrong-path or wrong-lineage objects. Unix reads
also use nonblocking/no-follow opens against replacement races. Only the selected lineage namespace is scanned; other project partitions are
ignored. Each scan is bounded by entry, record and byte limits. Validated incoming objects are retained
in the private local archive **before** semantic application. Supported records
use the same dependency, transaction, collision, fork and resolution rules as Git.
Incomplete dependencies remain pending and are retried on later scans. Unsupported
schemas in the current wire format can be retained/relayed without application;
unknown wire formats are refused. Provider conflict copies never substitute for
canonical files; their presence is reported for review.

`llewm sync` attempts each configured endpoint independently, then makes a second
folder pass when needed to forward newly received authorized history. A failed
endpoint does not erase the successful work or receipts of another endpoint.
With multiple endpoints JSON reports `git` and `folders` separately. A folder
receipt reports `published_locally` or `pending`/`unavailable`/`error`, counts,
time and `peer_receipt: "unknown"`. Git transport may publish retained objects
while local semantic application remains pending; its error/detail makes that
condition visible. Neither a directory write nor a Git push certifies that a
particular peer imported a record.

After checkpoints, automatic projects request a detached sync through the existing
bounded lifecycle mechanism. Session start queues shared-folder/hybrid sync in the
background and reports the last imported state, because cloud placeholders or
filesystem drivers can block I/O. It does not wait for hydration. Run explicit
`llewm sync` before relying on newly delivered history, and inspect its result.
There is no continuously running Llewm watcher or new required daemon.

## Recovery and limits

The durable device-local archive is `.llewm/continuity/history/`, with immutable
objects, local origin markers and per-endpoint status. Keep it private and outside
file replication. No automatic archive garbage collection or shared-object pruning
is performed. A SQLite-only backup does not include opaque unapplied archive
records; include the archive in an appropriate stopped-writer/full-state backup
when those observations must survive disk loss.

- Missing directory or marker: report unavailable; never recreate it during sync.
  A replacement marker/store at the same path is refused. Restore the intended
  mount/provider state or explicitly configure a new endpoint.
- Partial/unhydrated file or missing dependency: retain validated observations,
  report pending, retry after the provider delivers complete data. A local archive
  does not establish that a peer received it.
- Deleted shared object: an intact authorized local archive can republish it.
  Filesystem deletion is never semantic deletion. If every durable copy is lost,
  the transport cannot recover the data. Deliberate history erasure/retention
  changes require a separate reviewed workflow.
- Corrupt existing object: preserve the valid local copy and report the problem;
  never silently overwrite the provider's file or choose a conflict-copy winner.
- Directory visibility, offline hydration, provider versioning, sharing settings
  and delivery are external. Hashes detect corruption, not malicious authorship.
  This format is plaintext curated data; application-level encryption/signatures
  are not implemented. Do not assume a cloud folder provides end-to-end secrecy.

Disposable Linux tests qualify the local filesystem protocol, delayed/partial
arrival, missing mounts, identity replacement, corruption/deletion handling,
explicit relay isolation, three-device folder/Git convergence, unresolved/resolved
fork preservation, opaque-schema relay and idempotent replay. They simulate the
provider boundary; they do not qualify a live Syncthing/cloud account, every
filesystem, other native platforms, or arbitrary provider configurations.
