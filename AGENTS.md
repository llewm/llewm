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
- Keep `/tmp` small: it is a 3 GiB per-user tmpfs quota, and filling it breaks
  builds and the shell. Put caches and build outputs on the home disk (Go uses
  its default `~/.cache/go-build`, which it trims itself). Delete scratch
  artifacts you create, such as database copies, exported bundles, and test
  binaries, as soon as they have served their purpose, and before ending the
  task. Never keep a copy of a live database in `/tmp` longer than one check.

<!-- continuity:contract pre-release-breaking-changes -->
## Pre-release changes

kind: directive
scope: every component, until Llewm's first public release
expiry: the first public release

Until public release, prefer correctness and redesign over backward compatibility:
make breaking changes that improve the product, and merge or replace overlapping
record types, commands, and tools instead of adding alongside them. Do not add
compatibility aliases or shims. Still back up and verify live databases before
migrating them.

## Continuation

Continue from the Continuity bootstrap and its checkpoint, then check the relevant
Git state. Current state lives in Continuity records, not in repository files: load
a plan item with `llewm brief LABEL`, install steps from the pinned procedures,
and hosts from resources. Use `docs/vision.md` and `docs/architecture.md` for
project-wide scope decisions. Continuity-specific product
context is in `components/continuity/docs/product-context.md`.
With other agents active, use your own worktree and stage explicit paths.
