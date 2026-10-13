<!-- continuity:roadmap-node continuity-governed-contracts -->
# Contracts

Status: implemented in 6f of `continuity-multi-repo-sync` (migration 062, MCP API
91; decision `contracts-markdown-canonical`). This replaces the governed-contract
records, their lifecycle transitions, registry projection, authority migrations,
and evaluation (13 MCP tools and the six technical kinds). The four stored
revisions were re-expressed as the marked sections listed below, and the
pre-migration backup keeps the old rows.

## Model

A contract states one **project shape** so it can be found and quoted exactly:

| Kind | Meaning | Example |
| --- | --- | --- |
| `axiom` | A truth the project builds on | Transcripts are not canonical state |
| `directive` | How to work | Prefer redesign over compatibility until release |
| `promise` | What the product guarantees | The memory repository never holds a secret |
| `constraint` | A boundary that must hold | Hook qualification is current only for its exact version |

Each contract is a section of a Markdown file in the repository. The file is
canonical: people and agents edit it like any other document, review it in Git,
and any client that reads the file sees the statement without Continuity.
Decision records keep the *why*; the marked section is the *what*.

```markdown
<!-- continuity:contract pre-release-breaking-changes -->
## Pre-release changes

kind: directive
scope: every component, until Llewm's first public release
expiry: the first public release

Until public release, prefer correctness and redesign over backward compatibility...
```

- The marker names a stable ID (letters, digits, `.`, `_`, `-`).
- The next heading is the title. `kind:` is required; `scope:`, `expiry:`, and
  `review:` are optional and must come right after the heading.
- The statement runs to the next continuity marker or the next heading of the same
  or a higher level, so subsections belong to it.
- The SHA-256 covers the whole section from its heading, so changing the title,
  header, or statement is drift.

Continuity finds sections with `git grep` over tracked and untracked (not ignored)
`*.md` files under the project root. A duplicate ID, a missing heading or kind, or
an empty statement is reported as a problem, and that section is skipped.
Workspace members other than the root are not scanned yet.

## Drift

The device database keeps an append-only index of accepted section hashes
(`contract_index_entries`; local, not synced, since the files travel through Git).
Each read compares the sections found now with that index:

- `current`: the hash is the accepted one;
- `changed`: the section changed since it was accepted;
- `new`: the section was never accepted;
- `missing`: an accepted section is no longer found (file moved or deleted, or the
  marker removed).

The session bootstrap adds a `Contracts` line whenever anything is not current.
After reviewing the change, `llewm contract check --accept` records the current
hashes (and drops missing sections from the index). Nothing is accepted
automatically, so an edit made outside a reviewed change keeps showing until
someone looks at it.

## Commands

```text
llewm contract list                 # every contract: kind, state, file:line, title
llewm contract show ID              # one statement exactly as written, with its hash
llewm contract check [--accept]     # only drifted contracts; --accept records them
```

Contracts are read with the CLI (`contract list`, `show`, `check`); MCP API 98
removed the read-only MCP tool. Accepting drift stays a deliberate CLI step.

A contract states a shape; it does not authorize, execute, or prove anything.
Whether a client meets a requirement is recorded in the [coverage
matrix](coverage.md).

## Contracts in this repository

| ID | Kind | File |
| --- | --- | --- |
| `pre-release-breaking-changes` | directive | `AGENTS.md` |
| `memory-repository` | promise | `components/continuity/docs/memory-repository.md` |
| `client-hooks` | constraint | `components/continuity/docs/client-hook-conformance.md` |
