# Continuity

Continuity is the first component and entire current MVP of [Llewm](../../README.md).
It is a local-first continuity and activity controller for coding agents. It keeps cloud sessions disposable while preserving project rules, task state, failed attempts, environment discoveries, verification results, and cost attribution.

A major goal is to reduce repeated prose discovery in fresh sessions and the
accumulation of duplicate plans, handoff files, and stale agent notes in the
repository. Structured state and bounded retrieval support that direction today;
populating it from existing docs and reducing redundant prose still require
deliberate review during onboarding and ongoing work.

The MVP deliberately avoids general-purpose AI memory and autonomous execution. It focuses on:

- compact session bootstraps;
- structured checkpoints and attempt history;
- Codex and Claude Code lifecycle ingestion;
- MCP resources and tools;
- instruction and activity hot-path reporting;
- token/cost allocation auditing; and
- human-reviewed procedure discovery and export.

## Status

Continuity is an implemented preview with a read-only console, lifecycle hooks,
checkpoints, bounded history retrieval, usage auditing, and MCP. Linux is locally
verified; native macOS and Windows runtime and client qualification remain pending.
Client hook payload mapping and procedure mining are deliberately bounded and
should be extended using captured, sanitized fixtures.

## Prerequisites

- Current stable Go toolchain compatible with the official MCP Go SDK
- Git

## Build and test

From the Llewm repository root, enter the component first:

```sh
cd components/continuity
```

Run all commands below from this component directory.

```bash
go mod download
go fmt ./...
go test ./...
go vet ./...
go build -buildvcs=true -o ../../dist/llewm ./cmd/llewm
```

See the [checkpoint contract](docs/checkpoints.md) for validation, redaction,
failed attempts, and verification-claim semantics.

## Quick start

Start with [guided onboarding](docs/onboarding.md) to verify installation, client
activation, and recovery of a first checkpoint.

Define personal rules, context, and reusable prompt guidance once for all projects
with `llewm global init`. See [Engineer-wide guidance](docs/global-guidance.md)
for locations, examples, precedence, and budget behavior.

For the installed `llewm` command, follow
[Installation](docs/installation.md). It covers Linux, macOS, Windows, and WSL,
including persistent `PATH` setup and client restarts. The commands below run
directly from the source checkout without installation.

```bash
go run ./cmd/llewm init
go run ./cmd/llewm version
go run ./cmd/llewm doctor
go run ./cmd/llewm checkpoint create \
  --task "Implement bootstrap compiler" --task-id bootstrap-compiler \
  --summary "Prepare bounded startup context" \
  --next "Add golden packet fixtures"
go run ./cmd/llewm checkpoint show --query "bootstrap compiler" --output json
go run ./cmd/llewm checkpoint show CHECKPOINT_ID --max-bytes 16384
go run ./cmd/llewm work freshness --task-id TASK_ID --limit 20 \
  --remind-after-events 50 --remind-after-seconds 3600 --output json
go run ./cmd/llewm bootstrap
go run ./cmd/llewm activity --limit 20
go run ./cmd/llewm coverage record \
  --requirement instruction-precedence --client codex --platform linux \
  --status supported --verification observed
go run ./cmd/llewm coverage
go run ./cmd/llewm roadmap view status
go run ./cmd/llewm roadmap view overview
go run ./cmd/llewm roadmap view render
go run ./cmd/llewm roadmap view check --path docs/generated-roadmap.md --output json
go run ./cmd/llewm roadmap review-packet --manual
go run ./cmd/llewm item list --friction --min-count 2 --max-bytes 8192
go run ./cmd/llewm work
go run ./cmd/llewm work --summary
go run ./cmd/llewm work check --scope current
go run ./cmd/llewm work list --view next
go run ./cmd/llewm work show node:continuity-project-state-hygiene
go run ./cmd/llewm work --output json
go run ./cmd/llewm roadmap view overview --output json
```

Browse saved work with `llewm tui`. See the [console guide](docs/console.md)
for navigation, report previews, delivery snapshots, and versioned editor reads.

Start the stdio MCP server:

```bash
go run ./cmd/llewm mcp
```

Commands normally discover the project from the current directory. Use the
root-level `--project-root PATH` flag to select a Git project explicitly from
another working directory; `PATH` may be the root or a path inside it. The same
flag binds an MCP process to one project for its lifetime:

```bash
llewm --project-root /path/to/project status
llewm --project-root /path/to/project mcp
```

The default state directory is `.llewm/continuity` at the detected Git root (the Llewm
repository root when developing here), not in this component directory. Set `CONTINUITY_HOME` to use a different local component state directory.
See [layout migration](docs/project-layout.md) for legacy `.continuity` stores and
[prose-first trials and leaving Llewm](docs/trials.md) before adopting a real project.

Project IDs are opaque and permanent; the root path is only the project's current
location. A linked Git worktree uses its main checkout's project and store, and a
submodule uses its superproject's, unless that worktree or submodule has its own
`.llewm/` project directory. When a checkout moves with its store inside it,
Continuity recognizes the repository and records the relocation. Any other
ambiguous match is refused until you choose explicitly:

```bash
llewm project show                   # project, checkout kind, and recorded locations
llewm project rebind PROJECT_ID      # this checkout is that project, moved
llewm project rebind --new           # this checkout is a separate project
```

Several repositories developed together can either share one project (`unified`)
or keep independent projects grouped for routing (`federated`). In the folder
that holds them, propose and then save a workspace manifest:

```bash
llewm workspace init            # print the proposed .llewm/workspace.toml
llewm workspace init --write    # save it
llewm init                      # create the workspace store
```

That default unified mode makes the workspace folder and every listed member
(with its worktrees and submodules) resolve to one project and store. An unlisted
repository under the folder stays its own project.

For independent lineages and stores, create a federated manifest and initialize
each member. Opening a member records a metadata-only route in the machine-local
`registry.db` under the Continuity global configuration directory; project bodies
remain in the member stores. `workspace show` and `workspace route` consider only
current manifest members, flag routes whose checkout no longer exists, and accept the
workspace through a symlinked path. `workspace show` reports registered and missing routes:

```bash
llewm workspace init --mode federated --write
(cd api && llewm init)
(cd worker && llewm init)
llewm workspace show
llewm workspace route api          # member, repo/project/lineage ID, or fingerprint
```

To change an existing workspace topology, preview the exact portable record sets
and stores first, then repeat the returned digest explicitly:

```bash
llewm workspace change --mode federated
llewm workspace change --mode federated --apply --confirm SHA256

llewm workspace change --mode unified
llewm workspace change --mode unified --apply --confirm SHA256
```

A split gives every member its own project/store and imports the unified curated
memory under the same lineage. A merge requires every member (and any existing
workspace store) to have that same lineage; different lineages are refused rather
than conflated. The confirmation binds the manifest, stores, identities, and
portable record-set hashes, so a memory change requires a new preview. The manifest
switch happens only after all validated imports succeed. Old stores remain in place
for recovery, while device-private raw events, sessions, and usage samples stay in
their original stores.

Generate an optional VS Code workspace without changing project authority:

```bash
llewm workspace editor                         # preview JSON
llewm workspace editor --write                 # WORKSPACE.code-workspace
llewm workspace editor --write --path dev.code-workspace
```

A folder of several repositories without a manifest is refused rather than guessed.

In a workspace, paths are workspace-relative (`joncyd/cmd/main.go`). `--commit`
resolves in the member containing the working directory; at the workspace folder,
name one with `--repo MEMBER` (MCP tools take `repo`). Narrative commit anchors
record their member in `region_locator` as `member:<path>`, and roadmap sync
watermarks record every member's head, so freshness counts commits per member.

### History sync

Curated history (checkpoints, narratives, decisions, topics, roadmap, plans, work
items, procedures and resources) can sync between machines through a Git memory repository that
Continuity manages. Immutable observation files avoid ordinary line-level record merges; semantic
collisions and forks remain explicit. The live SQLite database never goes into Git.

```bash
llewm sync init                       # first machine: start a history
llewm sync init --lineage LINEAGE     # other machines: join it
llewm sync init --remote git@host:org/llewm-memory.git
llewm sync                              # export, commit, rebase, import, push
llewm sync status
```

Once configured, sync is automatic. Each session start exports, fetches, and
imports before the bootstrap is built, so records from other machines reach it,
and leaves its push to a detached background sync. It waits on the remote at most
`session_timeout_seconds` (8 s by default, at most 10 s so the bootstrap still fits
the plugins' 15 s SessionStart hook limit); a fetch that does not finish in time
also continues in the background. Each new checkpoint starts a background sync
too. Bootstrap and `llewm work` show a sync line such as
`Sync: last synced 3m ago · 1 unpushed commits · remote unreachable (2 attempts)`.
Set `manual = true` under `[sync]` to sync only by hand.

Git runs without terminal prompts, so authentication must come from an SSH agent
(SSH runs in batch mode), a credential helper, `GIT_ASKPASS`, or `core.askPass`.
To give only Continuity a prompt program, such as a wallet-backed one for an HTTPS
remote, pass `--askpass /usr/bin/ksshaskpass` to `sync init` (stored as `askpass`
under `[sync]`; `--askpass ""` clears it). It is a fallback: Git consults it last,
after the helper, `GIT_ASKPASS`, and `core.askPass`, and it is not used for SSH
key passphrases.

Without global Git settings, `sync init` can set up credentials for the memory
clone only: `--credential-helper HELPER` (default: your existing helper, else
libsecret) is written into the clone's own Git config, Git prompts once or
`--token-stdin` supplies a token, and the helper stores it; `--ssh-key PATH` uses one
key for memory syncs. `--plaintext-credentials` (unencrypted, confirmed) is the last
resort. The memory repository has its own [rules of engagement](docs/memory-repository.md).

Offline, `sync` still commits locally and pushes next time. Two versions of one
record are reported as a collision on every sync until resolved; everything else
keeps flowing.

See [multi-repository projects and sync](../../docs/multi-repo-and-sync.md).

## Project layout

- `cmd/llewm`: command-line entry point
- `internal/database`: SQLite opening and migrations
- `internal/activity`: canonical event model and persistence
- `internal/bootstrap`: compact session bootstrap compiler
- `internal/checkpoint`: checkpoint service
- `internal/audit`: usage and activity reports
- `internal/attribution`: append-only actor, incident, restriction, and capability evidence
- `internal/clients`: provider-specific hook adapters
- `internal/friction`: deterministic failure/recovery grouping
- `internal/workitem`: ideas, bugs, and tasks with dispositions and links
- `internal/workstate`: deterministic current-work summaries and state findings
- `internal/mcpserver`: MCP resources and tools
- `internal/procedures`: deterministic procedure-candidate model
- `plugins`: matching client packages bundled into the executable for `llewm install`
- `migrations`: embedded, append-only database migrations
- `schemas`: versioned external event schemas

See [docs/architecture.md](docs/architecture.md) and [docs/roadmap.md](docs/roadmap.md).
The [roadmap-control contract](docs/roadmap-control.md) covers stable nodes, file
bindings, model-sync watermarks, versioned review routines, accepted due thresholds,
and zero-model freshness reporting.
The [work-items contract](docs/work-items.md) covers ideas, bugs, and tasks, their
dispositions and promotion, and bounded repeated-friction candidates.
