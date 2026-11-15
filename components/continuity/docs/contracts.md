<!-- continuity:roadmap-node continuity-governed-contracts -->
# Contracts

## Model

A contract states one **project shape** so it can be found and quoted exactly:

| Kind | Meaning | Example |
| --- | --- | --- |
| `axiom` | A truth the project builds on | Transcripts are not canonical state |
| `directive` | How to work | Preserve supported interfaces across releases |
| `promise` | What the product guarantees | The memory repository never holds a secret |
| `constraint` | A boundary that must hold | Hook qualification is current only for its exact version |

Each contract is a section of a Markdown file in the repository. The file is
canonical: people and agents edit it like any other document, review it in Git,
and any client that reads the file sees the statement without Continuity.
Decision records keep the *why*; the marked section is the *what*.

```markdown
<!-- continuity:contract release-compatibility -->
## Release compatibility

kind: directive
scope: development of Llewm and its components; not projects using Llewm
review: before a release or a change to a supported interface or persisted format

Preserve supported interfaces and persisted data across updates...
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
llewm contract check [--accept]     # only drifted contracts, "Drifted: N of M"; --accept records them
```

Contracts are read with the CLI (`contract list`, `show`, `check`). Accepting
drift stays a deliberate CLI step.

A contract states a shape; it does not authorize, execute, or prove anything.
Whether a client meets a requirement is recorded in the [coverage
matrix](coverage.md).

## Contracts in this repository

| ID | Kind | File |
| --- | --- | --- |
| `release-compatibility` | directive | `AGENTS.md` |
| `memory-repository` | promise | `components/continuity/docs/memory-repository.md` |
| `client-hooks` | constraint | `components/continuity/docs/client-hook-conformance.md` |

## Scope of development and installed guidance

The release directive governs work on Llewm itself. Installing a binary or plugin
does not copy this repository's `AGENTS.md` into an adopting project. Its marked
contracts are discovered under its own project root, not as a global policy pack.

Installed bootstraps carry Continuity's built-in usage rules, the adopting
project's guidance, and the engineer's optional global guidance. Built-in rules
must not select a commit, release, or branch policy for that project. Personal
global guidance can intentionally affect every project on that engineer's account;
`llewm global init` creates heading-only files, not Llewm's development policies.
See [global guidance](global-guidance.md) for sources and override behavior.

The other marked contracts retain their product scope: `memory-repository`
protects Continuity-owned sync storage, and `client-hooks` requires exact-version
qualification of hook delivery. Neither authorizes changes to an adopting project's
code, workflow, or Git history.
