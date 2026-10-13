# Continuity requirements

What each supported client and platform must do for Continuity to work. This file
is the authority for requirement IDs and wording. Observations of whether a client
meets a requirement are coverage cells, recorded with `llewm coverage record`
and read with `llewm coverage`.
Cells name the machine that observed them and travel through History sync; they
are not stored here. A requirement without a cell is unknown. See
`components/continuity/docs/coverage.md`.

A section starts at its `continuity:requirement` marker, takes the next heading as
its title, an optional `scope:` line (clients it applies to) right after the
heading, and the text up to the next marker as its description. Never reuse or
renumber an ID; retire a requirement by deleting the section and leaving its cells
to show as orphaned.

<!-- continuity:requirement session-start-bootstrap -->
## Session start delivers the bootstrap

The client runs the SessionStart hook and places its output in the first model
request, so a fresh session sees the current task, next action, guidance, and
pinned records without asking.

<!-- continuity:requirement mcp-tools -->
## Continuity MCP tools are callable

The client lists the Continuity MCP tools and can call each of them, with approvals
that do not prompt for read-only tools.

<!-- continuity:requirement checkpoint-reminder -->
## Checkpoint reminder reaches the model

When reminder policy is enabled and a threshold is crossed, the reminder returned by
the hook is placed where the next eligible model request can use it, once per rearm.
For asynchronous Codex hooks, eligibility starts at the next safe point after the
hook finishes: after in-flight model requests and tool calls in an active turn,
or on the next user turn when idle. The first response after the triggering tool
need not contain the advisory. Synchronous Claude hooks must deliver it before
the first post-tool model response. Qualify active-turn and idle delivery separately.

<!-- continuity:requirement posttooluse-ordering -->
## Tool-use hook ordering is known
scope: claude-code, codex

The scheduling of PostToolUse relative to the next model request is documented and
verified: synchronous for Claude Code, asynchronous with a context limit for Codex.

<!-- continuity:requirement hook-suppression -->
## Reminders honor suppression and disablement

A disabled policy and a suppressed edge produce no reminder and no failure. An
invalid threshold produces no reminder and reports a validation failure to the
hook runner without undoing the completed tool or stopping the client session.

<!-- continuity:requirement plugin-package -->
## Plugin package is structurally valid

The plugin's hook files, canonical executable, matchers, and lifecycle fixtures match
what the client loads. A hook change is requalified on the exact installed version.

<!-- continuity:requirement instruction-precedence -->
## Instruction precedence is documented

The order in which the client reads global, project, and nested instruction files is
documented and observed, so the bootstrap does not duplicate or contradict it.

<!-- continuity:requirement permission-rules -->
## Permission rules are installable

`llewm client permissions` generates rules for the client's own settings that
approve the read-only Continuity tools and CLI, and `llewm doctor` warns when
they are missing.

<!-- continuity:requirement skills-commands -->
## Plugin skills load and trigger

The client loads the Continuity plugin skills (checkpoint, continue, decision,
history, items, narrative, status, track) and triggers them from their descriptions.

<!-- continuity:requirement platform-binary -->
## The binary runs on the platform

`continuity` installs and runs on the platform, reaches its database, and passes the
Go checks, including with the shell and path conventions the client uses.

<!-- continuity:requirement continuity-first-discovery -->
## Agents consult Continuity before other discovery

In a new session the agent reads the bootstrap, the slice brief, and a history
search before grepping or exploring the repository for the same facts.
