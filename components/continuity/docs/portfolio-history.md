# Project-level memory federation

A portfolio routes independent project histories to explicitly named Git memory
repositories. One private repository can collect projects A, B and C, while each
project also exchanges its history with a different developer group. Each project
keeps its own lineage and local operational store. The portfolio is a routing
catalog, not a new shared SQLite database or a merged project.

```text
Project A <--> private portfolio repository (A, B, C)
          <--> Alpha repository (A)
Project B <--> private portfolio repository (A, B, C)
          <--> Beta repository (B)
Project C <--> private portfolio repository (A, B, C)
          <--> Gamma repository (C)
```

## Sharing boundaries

Use independently created project lineages for independent sharing audiences.
A unified multi-repository workspace has one history: portfolio routing cannot
publish only one member, task or topic. Splitting a workspace can retain its old
lineage/history, so a split alone does not establish a new confidentiality boundary.
Two portfolio project bindings cannot claim the same lineage.

All readers of a destination Git repository can read every lineage and historical
record stored there. Names, lineage directories and relay settings are not access
controls. Keep the aggregate private; use separate access-controlled repositories
for groups that must not see each other's projects. Curated prose can still contain
sensitive information; review what belongs in a project's shared history.

Git moves immutable curated records, not live databases, raw client telemetry,
credentials or full transcripts. See the [memory repository contract](memory-repository.md).
Never edit or commit to a managed clone by hand. Removing a route or disabling
relay does not retract records or Git history already published.

## Configure without transferring history

Commands work from any folder, including a workspace folder outside Git.
The machine-local catalog is `portfolio.toml` in the Continuity global directory
(`CONTINUITY_GLOBAL_HOME` when set). It contains destination URLs, optional clone
and SSH-key paths, and pinned source project routes; it is not History-synced.
Keep it private and do not copy it to teammates as project configuration.

Start with existing initialized projects that already have independent portable
identities. Inspect each source without creating or changing it:

```sh
llewm portfolio project inspect /path/to/project-a
llewm portfolio project inspect /path/to/project-b
```

If a source has no portable identity, first choose whether to create new local
history or join existing history through the normal [onboarding workflow](onboarding.md).
The portfolio never guesses or creates that identity. Trial/exited projects cannot
sync. `CONTINUITY_HOME` overrides are refused to avoid routing several roots into
an unintended store; configure each owning project's storage explicitly instead.

Name the exact destinations, using existing Git credential helpers or an SSH agent:

```sh
llewm portfolio destination add private --remote git@example.org:me/portfolio-memory.git
llewm portfolio destination add alpha --remote git@example.org:alpha/project-a-memory.git
llewm portfolio destination add beta --remote git@example.org:beta/project-b-memory.git
```

These commands record local configuration only; they do not contact the remotes,
create clones, initialize projects, change credentials, or enable automatic sync.
SSH, HTTP(S), and absolute local Git repository paths are supported. Passwords and
query credentials in URLs, remote-helper commands, relative paths and unknown
catalog fields are refused. Secrets belong in existing authentication facilities.

Use `--clone /absolute/owned-memory-clone` to retain an existing managed clone,
including a pre-release namespace, or to choose a separate new clone location.
Otherwise the clone is shared per remote under the normal Llewm data directory.
Use `--ssh-key /absolute/key` when a destination requires an explicitly selected
key; only its path is saved. Synchronization never prompts for credentials or
installs a credential helper. A credential failure is reported per destination.

Bind projects with the exact local project ID and lineage returned by inspection:

```sh
llewm portfolio project add a --root /path/to/project-a \
  --confirm-project PROJECT_A_ID --confirm-lineage LINEAGE_A \
  --destination private,alpha
llewm portfolio project add b --root /path/to/project-b \
  --confirm-project PROJECT_B_ID --confirm-lineage LINEAGE_B \
  --destination private,beta
llewm portfolio show
llewm portfolio sync --all --dry-run --output json
```

Each binding has one to eight destinations. A portfolio supports at most 64 named
destinations and 256 projects. Owning roots, IDs and lineages are rechecked before
sync; relocation, topology or identity changes require explicit rebinding. Worktrees
and submodules resolve to their owning store; federated members remain independent.
Clone paths must be separate from participating checkouts, selected operational
storage, backups, shared-folder endpoints and other destination clones.

## Authorize forwarding explicitly

Relay starts disabled for every project/destination route. Locally originated
curated records are eligible for all of that project's destinations. Records
received from a destination can return to that destination. Forwarding received
history to another destination requires that route's relay authorization.

For bidirectional exchange between A's private aggregate and Alpha, authorize both
directions for **A's lineage**:

```sh
llewm portfolio relay a private --enable=true --confirm-lineage LINEAGE_A
llewm portfolio relay a alpha --enable=true --confirm-lineage LINEAGE_A
```

Repeat deliberately for B's routes if desired. This authorizes the whole retained
curated stream for that lineage, including prior observations and unresolved or
currently uninterpretable record versions. It never includes B merely because the
private Git repository also contains B. It also does not create a private subgroup
inside A: newly authored A records can go to all A routes, even with relay disabled.
Use `--enable=false` to stop future forwarding to one destination.

## Sync and inspect results

```sh
llewm portfolio sync a --output json
llewm portfolio sync --all --output json
llewm portfolio status --all
```

Selection is explicit: give one project name or `--all`. Dry-run/status read local
pins without opening a writable database, creating an archive/clone, or contacting
a remote. A real sync uses the existing verified record archive, managed Git
transport, lineage-scoped import and fork/collision handling. Multiple destinations
get two passes so later imports can reach earlier destinations when relay permits.
Concurrent peer changes can require another cycle; a successful push is not proof
that another developer imported the records.

Receipts identify each project, destination and pass independently. An unavailable
destination does not undo another destination's completed work. Partial/pending
imports and offline endpoints return a failure with their receipts preserved.
Status and import watermarks are scoped to the project and exact remote.

Portfolio sync is manual. Existing per-project `llewm sync`, its automatic triggers,
folder endpoints and primary Git coordination/lease remote keep their own explicit
configuration. Portfolio commands do not run those other destinations or create
another cross-machine lease authority. A preview reports whether the source also
has per-project sync configured. Account for those routes when reviewing sharing.
Origin tracking is shared, so forwarding through either interface still follows
the destination's existing relay authorization. Independent project lineages are
the isolation boundary; the catalog is not an operating-system access-control layer.

## Remove or revise configuration

```sh
llewm portfolio project remove a
llewm portfolio destination remove alpha
```

An in-use destination cannot be removed. To change a source identity or its
destination set, remove its local binding, inspect the current source, and add the
reviewed binding again. New routes start with relay disabled. Existing project
databases, archives, clones, published history and authentication are retained.
A running sync uses its loaded catalog snapshot; finish or cancel it before
changing routes. Removing configuration does not cancel an already-running sync.
Concurrent catalog edits fail with a lock diagnostic. After a crash, verify that
no portfolio configuration writer is running before reviewing/removing a leftover
`portfolio.lock` in the global directory; never delete a managed memory lock or
edit a clone to repair catalog configuration.

## Qualification scope

Local Git fixtures exercise a private two-project aggregate, separate group
repositories, fresh receiving stores, exact checkpoint recovery, peer round trips,
relay isolation, cross-project history exclusion, idempotence and partial failures.
They do not qualify a particular hosting provider, enterprise access policy, or
native client/OS. Real destinations still need explicit setup and observed checks.
