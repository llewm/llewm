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
bookkeeping do not invalidate the token. Tracking is conservative across the
whole database: a relevant change in another project may cause a full cycle.

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
archive size. Generic folder endpoints retain their complete validated scan;
they do not yet expose a trustworthy atomic multiwriter change token.

The immediate sync JSON result includes `unchanged: true` when this proof skips
export, archive content reads, block processing, and semantic replay. Proofs are
not portable import receipts and are never evidence of another peer's receipt.

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
