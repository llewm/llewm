# Llewm

**Keep the thread of your work.** Pronounced “loom”.

Llewm is a local-first project for agent continuity, activity auditing, and future
automation, orchestration, and local LLM interoperability.

**Less repeated discovery. Less project-doc clutter and rot.** A major goal is
to reduce the time and tokens fresh agent sessions spend hunting through prose
to reconstruct current work. Llewm should also reduce the growing piles of
handoff files, duplicate plans, and stale agent notes that make repositories
harder to navigate and maintain.

Continuity already stores attributable task state, plans, decisions, and history
and retrieves bounded context for the next action. The direction is to maintain
operational project knowledge there, keeping ordinary docs focused on useful
guidance and explanations. Guided onboarding imports selected prose into
source-linked tracks and plans using the existing agent, then presents it for
review. Doc cleanup and reconciliation of conflicting intent remain deliberate
choices.

**Persistent agent state, with small working context.** Llewm uses graph
techniques—typed links between tasks, plans, decisions, evidence, and sources—to
resolve relevant context and traverse exact references as needed. Compact startup
state and bounded retrieval keep each session's loaded context small while the
larger project history remains available.

**Keep research useful beyond the session.** Agents can save research findings,
supporting evidence, and limitations as attributable claim chunks, linked to the
work they inform. Focused search and bounded expansion retrieve selected findings
without replaying the research session. Agents supply the summaries and citations;
dated source observations and claim-level evidence links preserve their context.
Automatic web capture and truth verification are not provided.
See [research memory and qualification](components/continuity/docs/research-memory.md).

**Coordinate work across agents.** Explicit task bindings, presence observations,
overlap visibility, and scoped leases support coordination today. Required lease
fencing can protect selected record writes; presence alone does not lock files or
assign work. The separate [orchestration component](components/orchestration/README.md)
now provides a manual shared work pool, reviewed assignments and context-preserving
reassignment. Automatic agent launching and approved execution remain future
directions. See [parallel tracks](docs/parallel-tracks.md).

**Continuity is Llewm’s current MVP, not its full scope.** It preserves attributable
project guidance, tasks, plans, decisions, research, and verification across
coding-agent sessions, retrieving bounded context when work resumes. It also records
activity and usage for auditing, with optional History sync across machines.
Continuity builds as the `llewm` executable; manual orchestration and Action Control
are separate components.

## Components

| Component | Status | Purpose |
| --- | --- | --- |
| [Continuity](components/continuity/README.md) | Implemented preview; native macOS/Windows qualification pending | Session bootstrap, checkpoints, activity ledger, audits, and MCP |
| [Action Control](components/action-control/README.md) | Standalone validator implemented | Versioned Git commit-message policy validation; no execution or hook installation |
| [Orchestration](components/orchestration/README.md) | Manual coordination pilot; local fixture-qualified | Collaborative plans, authenticated claims, reassignment and stale-result review |

See the
[vision](docs/vision.md), [architecture](docs/architecture.md), and [roadmap](docs/roadmap.md).

## Get started

Start with [guided onboarding](components/continuity/docs/onboarding.md) for an
agent-assisted installation/update alone, then optional audit baseline, full local
trial, or adoption for a repository or multi-repository workspace. Trial and adoption
include [project-prose import](components/continuity/docs/prose-import.md), a populated
TUI and verified first checkpoint. Follow [Continuity installation](components/continuity/docs/installation.md) for
Linux, macOS, Windows, and WSL, then [client integrations](components/continuity/docs/client-integrations.md).
The installed binary bundles its matching plugins: run `llewm init` in the project,
then `llewm install --client codex --permissions` (or `--client claude-code`).

[Update checks and agent-led updates](components/continuity/docs/updates.md) cover
current source builds and future published binary bundles. The `update` skill
keeps acquisition, verified backups, explicit migrations, matching plugins and
fresh-client recovery together.

[Preview distribution](components/continuity/docs/preview-distribution.md) builds
versioned binaries and matching client packages.
Report ordinary bugs through GitHub or Forgejo issues; include the affected component,
version and reproduction steps. Review any draft before posting it publicly.

From the repository root, with Go installed, each component remains independently
checkable:

```sh
cd components/continuity
go mod download
go fmt ./...
go test ./...
go vet ./...
go build -o ../../dist/llewm ./cmd/llewm

cd ../action-control
go fmt ./...
go test ./...
go vet ./...
go build -o ../../dist/action-control ./cmd/action-control

cd ../orchestration
go fmt ./...
go test ./...
go vet ./...
go build -o ../../dist/llewm-orchestrate ./cmd/llewm-orchestrate
```

Alternatively, with [Task](https://taskfile.dev/) installed, run `task check` and
`task build` from the repository root. Some distributions package the executable
as `go-task`. Root tasks dispatch to all three component modules; there is no root Go
module. Build output goes in root `dist/`.

## Repository layout

- `components/continuity/`: Continuity's Go module, tests, schemas, plugins, and documentation.
- `components/action-control/`: Action Control's independent Go module and validator.
- `components/orchestration/`: the independent manual team coordinator and client.
- `docs/`: Llewm-wide vision, architecture, and roadmap.
- `AGENTS.md` and `CLAUDE.md`: project-wide development guidance.
- `go.work`, `Taskfile.yml`, and `.forgejo/workflows/`: coordinated local and CI entry points.

Local `.llewm/continuity/` state belongs to the detected Git root, including when commands
run from a component directory. It is ignored by Git.

## License

Original project source is licensed under the [MIT license](LICENSE). Third-party
dependencies retain their own licenses; binary distributions carry
[third-party notices](components/continuity/THIRD_PARTY_NOTICES.txt).
See [source publication and contribution routing](docs/source-publication.md).
