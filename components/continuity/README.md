# Continuity

Continuity is the first component and entire current MVP of [Llewm](../../README.md).
It is a local-first continuity and activity controller for coding agents. It keeps cloud sessions disposable while preserving project rules, task state, failed attempts, environment discoveries, verification results, and cost attribution.

A major goal is to reduce repeated prose discovery in fresh sessions and the
accumulation of duplicate plans, handoff files, and stale agent notes in the
repository. Structured state and bounded retrieval support that direction today;
guided onboarding populates it from selected docs through
[source-linked prose import](docs/prose-import.md). Reducing redundant prose
and reconciling conflicting intent remain deliberate review choices.

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
go run ./cmd/llewm bootstrap
go run ./cmd/llewm work
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
[full local trials and leaving Llewm](docs/trials.md) before adopting a real project.

## Workspaces and History sync

For project identity, relocation, unified/federated workspaces, and reviewed
split/merge, see [multi-repository projects](../../docs/multi-repo-and-sync.md).
Curated history can sync between machines through a managed Git memory repository;
live SQLite databases and device-private events stay local. Follow the
[memory repository guide](docs/memory-repository.md) for setup, authentication,
automatic sync, and recovery.

## Project layout

- `cmd/llewm`: command-line entry point
- `internal/database`: SQLite opening and migrations
- `internal/activity`: canonical event model and persistence
- `internal/bootstrap`: compact session bootstrap compiler
- `internal/checkpoint`: checkpoint service
- `internal/audit`: usage and activity reports
- `internal/attribution`: append-only actor invocation observations
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
