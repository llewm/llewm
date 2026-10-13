# Requirement coverage

Status: implemented in 7a of `continuity-multi-repo-sync` (migration 059, MCP API
88); cells sync since 7b (migration 060, MCP API 89). Coverage replaces client-capability evidence and hook qualification runs, two
record families for one claim: "client X on platform Y does Z, per source S".

## Model

- **Requirements** are authored in the repository, in `docs/requirements.md` (path
  relative to the project root). Each is a section starting at
  `<!-- continuity:requirement ID -->`, with the next heading as its title, an
  optional `scope: client, client` line right after it, and a description up to the
  next marker. The file is canonical and is parsed on every read; nothing is copied
  into the database. A duplicate ID or a section without a heading is reported as a
  problem and skipped.
- **Cells** are append-only observations: one per requirement, client, and
  platform at a time, each with a status, a verification level, a client version,
  an implementation note, links, and the instance (one project store on one
  machine) that observed it. The latest cell for a requirement, client, and
  platform is current, whichever machine recorded it; earlier cells stay as
  history. Cells travel through History sync as `coverage_cell` records, so every
  machine and developer sharing the memory repository sees the same matrix. Each
  cell is immutable and has its own ID, so synced cells never conflict and need no
  fork handling. A build without the record type defers it. A synced cell whose
  requirement is not in this checkout's requirements file yet reads as orphaned
  until the code catches up.
- **The matrix** is the read, naming for each cell whether it was observed on this
  machine or on another instance: every requirement as a row, its current cells, and a
  row status that is the worst current status (`failing`, `unsupported`, `partial`,
  `supported`) or `unknown` when it has no cell. Cells whose requirement is no
  longer in the file are listed as orphaned.

| Field | Values |
| --- | --- |
| status | `unsupported`, `partial`, `supported`, `failing` (never `unknown`: absence is unknown) |
| verification | `inferred` (reasoned from docs or code), `reported` (a client or person said so), `observed` (seen working in a real session), `tested` (an automated check exercised it) |
| client, platform | free text; use `claude-code` and `codex`, and `linux`, `macos`, `windows`, or `any` |
| refs | `commit:HASH`, `file:PATH`, `narrative:ID`, or a URL |

Recording refuses a requirement that is not in the file, so a typo cannot create an
orphan. Text is redacted before storage. A cell is a caller-reported observation,
never permission or proof of delivery.

## Commands

```text
llewm coverage [--client C] [--platform P] [--status S] [--requirement ID]
llewm coverage --requirement ID --history
llewm coverage record --requirement ID --client C --platform P \
  --status supported --verification observed [--client-version V] \
  [--implementation "..."] [--ref commit:HASH]
```

Coverage is CLI only (`llewm coverage`, `coverage record`); MCP API 98 removed
its tools, which had been called once or twice in all recorded sessions.

## Doctor and stale cells

`llewm doctor` adds one `coverage` line per detected client (Claude Code,
Codex): how many requirements are supported, partial, unsupported, failing, and
unknown, and how many cells are stale. Gaps are warnings, not failures. A cell is
stale when its client version differs from the newest `client_version` recorded for
that client in actor invocations. Hooks do not currently report a client version, so
no cell is flagged until a version source exists; a cell without a version is never
stale.
