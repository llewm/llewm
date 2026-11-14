# Llewm

## Project scope

Llewm is a local-first project spanning agent continuity and activity auditing,
with future automation, orchestration, and local LLM interoperability. Continuity
is the entire current MVP, not a permanent limit on the larger project.

## Component guidance

Before editing Continuity, read `components/continuity/AGENTS.md`. Its Go/SQLite,
no-execution, and no-required-model boundaries apply to that component's MVP. Do
not broaden its capabilities merely because the overall Llewm roadmap includes them.

## Engineering rules

- Keep one Git repository, with implemented components under `components/`.
- Keep component code, dependencies, tests, and documentation together.
- Add shared packages only when actual consumers establish the interface.
- Preserve attributable observations and redact sensitive data before persistence.
- Distinguish observed facts from interpretations and estimated usage from exact usage.
- Scope future execution capabilities explicitly; a roadmap entry is not authorization to execute actions.
- Run `go fmt ./...`, `go test ./...`, and `go vet ./...` from each changed Go module.
  Root `task check` dispatches these checks for the current component.
- Update installation paths, CI, and documentation links when moving components.
- Before editing, grep for every place the change must reach (callers, tests,
  fixtures, config, exports, docs); fix a bug's root cause once, in shared code.
- Code you move or merge keeps its validation and error handling.
- Prefer existing repo code, then the standard library, then an existing
  dependency, before new code. Never add a dependency for a few lines.
- End each reply with what you skipped or did not verify, and any risk the user
  must know.
- Keep `/tmp` small: it is a 3 GiB per-user tmpfs quota, and filling it breaks
  builds and the shell. Put caches and build outputs on the home disk (Go uses
  its default `~/.cache/go-build`, which it trims itself). Delete scratch
  artifacts you create, such as database copies, exported bundles, and test
  binaries, as soon as they have served their purpose, and before ending the
  task. Never keep a copy of a live database in `/tmp` longer than one check.

<!-- continuity:contract release-compatibility -->
## Release compatibility

kind: directive
scope: development of Llewm and its components; not projects using Llewm
review: before a release or a change to a supported interface or persisted format

Preserve supported CLI commands, MCP tools, configuration, plugin integrations,
and persisted data across updates. Correctness and simplification remain goals,
but release preparation is not blanket permission to remove or break behavior.

- Identify affected callers, users, documentation, and stored data before changing
  a supported interface. Prefer a compatible transition or documented deprecation.
- Make a breaking change only as an explicit, reviewed release decision with its
  rationale, affected versions, upgrade instructions, and recovery path. Do not
  silently remove commands, tools, record types, or configuration keys.
- Back up and verify live databases before migration. Test supported upgrades and
  recovery; never imply that an older binary can read a newer format unless verified.
- Keep Llewm development policies in this repository. Shipped guidance must concern
  using Llewm and respect the adopting project's workflow and user instructions;
  it must not impose Llewm's release, commit, or branch policy on other projects.

## Continuation

Continue from the Continuity bootstrap and its checkpoint, then check the relevant
Git state. Current state lives in Continuity records, not in repository files: load
a plan item with `llewm brief LABEL`, install steps from the pinned procedures,
and hosts from resources. Use `docs/vision.md` and `docs/architecture.md` for
project-wide scope decisions. Continuity-specific product
context is in `components/continuity/docs/product-context.md`.

## Branch and worktree lifecycle

For sequential single-session work, including new tasks, use the current checkout
and branch. Create a task branch/worktree only for confirmed concurrent writers,
an explicit isolation request, or an established project workflow requiring it.
Open tasks, checkpoints, leases, existing branches/worktrees, and expiring presence
records alone do not establish another active writer. If concurrency is uncertain,
inspect `llewm presence --live` and Git state; respect the user's report that this
is the only session. With concurrent writers, use your own worktree and stage
explicit paths.

After integrating work, remove its dedicated worktree and local branch once the
worktree is clean, the branch has no unique commits, and no live writer uses it.
Removing a worktree alone is incomplete cleanup. Report a concrete reason for any
retained task branch/worktree; do not remove unmerged or dirty work.
