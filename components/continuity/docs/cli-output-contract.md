# CLI output contract

Continuity's data output is independent of terminal detection. Human-facing reads
normally return text or Markdown; structured scripts should request
`--output json` where the command supports it. Some maintenance operations and
mutation receipts emit JSON directly. Inspect that command's `--help` for its
actual selector and default.

There is no `--json` compatibility alias in the Continuity CLI.
The independent Action Control validator has its own `--json` option.

## Data and diagnostics

Data goes to stdout; diagnostics go to stderr. JSON interfaces identify their
schema version where the command's contract specifies it. Text retains stable
identifiers, authority, ambiguity, failures, counts, and truncation rather than
silently discarding them. Collection reads remain bounded, with cursors or domain
filters where supported.

MCP stdio and lifecycle hooks use their own protocol-defined formats; CLI output
selection must not alter those protocols. The interactive console has terminal
requirements and a CLI fallback, but that does not change ordinary command output.

## Reads and receipts

Use explicit current commands such as:

```sh
llewm work --output json
llewm work check --scope current --output json
llewm roadmap view status --output json
llewm version --output json
```

Checkpoint and narrative bodies are bounded expansions. Plan reads distinguish
outline, one-item, and full-detail requests. Write receipts identify the saved
record and status; use the matching read to inspect its content.
A successful write receipt is not independent verification of caller-supplied
claims.

See [checkpoint semantics](checkpoints.md),
[client write receipts](client-integration-reference.md#write-tool-receipts), and
[console JSON reads](console.md#versioned-reads-for-editor-clients).

## Changes before public release

`llewm install --output json` returns a selected setup receipt, not raw client
settings. `state` is `preview`, `blocked`, `installed`, `partial`, or `failed`.
It records bundled package identity, executable/client versions, fixed client
argv, completed steps and any retained package backup. `plugin_installed` means
the client reported the expected enabled package version. Permission outcomes
distinguish `needs_review`, `declined`, `current`, `applied`, and
`applied_or_current`; none proves effective runtime policy. Hook trust remains
`needs_client_review`, and `live_verified` remains false. Errors return nonzero
even when a partial receipt is printed. With `--permissions`, human review text
uses stderr so stdout remains JSON. Invalid arguments and terminal refusals can
fail before a receipt is available.

Prefer one correct command and record model over overlapping interfaces.
Retired aliases and migration-era command spellings are not supported.
Review changes to schemas, stdout/stderr, defaults, cursors, and failure behavior
with their consumers; test meaningful interface contracts rather than preserving
an obsolete command surface.
