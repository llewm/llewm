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
guidance and explanations. Onboarding and doc cleanup still require explicit
review; Llewm does not automatically import or reconcile existing prose today.

**Continuity is the entire current MVP, not the full scope of Llewm.** It preserves
attributable project rules, task state, attempts, and verification across coding-agent
sessions. It builds as the `llewm` executable.

## Components

| Component | Status | Purpose |
| --- | --- | --- |
| [Continuity](components/continuity/README.md) | Implemented preview; native macOS/Windows qualification pending | Session bootstrap, checkpoints, activity ledger, audits, and MCP |
| [Action Control](components/action-control/README.md) | Standalone validator implemented | Versioned Git commit-message policy validation; no execution or hook installation |
| [Reporting](components/reporting/README.md) | Private Forgejo adapter; fixture-qualified | Selected product bug reports, local outbox and delivery receipts |

See the
[vision](docs/vision.md), [architecture](docs/architecture.md), and [roadmap](docs/roadmap.md).

## Get started

Start with [guided onboarding](components/continuity/docs/onboarding.md) for an
agent-assisted setup and verified first checkpoint. Follow [Continuity installation](components/continuity/docs/installation.md) for
Linux, macOS, Windows, and WSL, then [client integrations](components/continuity/docs/client-integrations.md).
The installed binary bundles its matching plugins: run `llewm init` in the project,
then `llewm install --client codex --permissions` (or `--client claude-code`).

[Preview distribution](components/continuity/docs/preview-distribution.md) builds
versioned binaries and matching client packages. Automatic private product bug
submission is a separate opt-in through the [Forgejo adapter](components/reporting/README.md).

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

cd ../reporting
go fmt ./...
go test ./...
go vet ./...
go build -o ../../dist/llewm-report ./cmd/llewm-report
```

Alternatively, with [Task](https://taskfile.dev/) installed, run `task check` and
`task build` from the repository root. Some distributions package the executable
as `go-task`. Root tasks dispatch to all three component modules; there is no root Go
module. Build output goes in root `dist/`.

## Repository layout

- `components/continuity/`: Continuity's Go module, tests, schemas, plugins, and documentation.
- `components/action-control/`: Action Control's independent Go module and validator.
- `components/reporting/`: the independent private Forgejo report-delivery adapter.
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
