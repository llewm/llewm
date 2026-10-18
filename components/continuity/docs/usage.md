# Usage and cost

Status: 8a-8c of `continuity-auditing`, with the usage-accuracy rebuild in
migration 069 (MCP API 105; decision `usage-tracking-design`).

## Where the numbers come from

Claude Code and Codex record every model request in their own session files.
Continuity reads only the usage fields from them, never prompts, responses, or
tool content:

| Client | File | Read |
| --- | --- | --- |
| Claude Code | `~/.claude/projects/<root with non-alphanumerics as ->*/<session>.jsonl` (`$CLAUDE_CONFIG_DIR`) | per response: model, input, cache write (and how much of it used the 1-hour lifetime), cache read, output, thinking tokens; tool calls by name; tool-result size in bytes |
| Codex | `~/.codex/sessions/YYYY/MM/DD/rollout-*-<session>.jsonl` (`$CODEX_HOME`) | `token_usage_record` per response (input normalized to exclude cached tokens; model from `turn_context`); subagent identity from `session_meta`; function calls, output sizes, and structured error indicators |

A file belongs to the project when the working directory it records is inside the
project root. Each file is read from a saved byte offset to its last complete line,
so repeated reads cost almost nothing and a line still being written is read next
time. Claude Code repeats a response's usage on every content block, so requests
are counted once per message ID. Codex subagent requests retain their parent
session ID and are excluded from main-session startup-prompt statistics.

Reads happen at Stop, SessionEnd and PreCompact for that session (bounded to 3 s), in a
detached catch-up started at SessionStart, and with `llewm usage ingest`
(backfill, or `--client C --session ID`). The rows (`usage_requests`,
`usage_tool_calls`, `usage_tool_results`, `usage_sources`) stay on this machine;
they can be cleared and rebuilt from the session files at any time.

These file formats are the clients' internals, not documented interfaces. Unknown
record types are skipped. A recognized Claude or Codex usage record that lacks its
required identity or token fields stops ingestion before its file cursor advances;
the explicit ingest command reports the incompatible-record count instead of
silently losing usage.

## Exact and estimated

- **Tokens are exact**: the numbers each provider reported to its client.
- **Cost is an estimate** at API list prices. Subscription plans (Claude Max, ChatGPT
  plans behind Codex) bill differently, so read cost as an API-rate equivalent.
  Built-in rates cover Anthropic models (standard global Claude API prices
  verified 2026-10-05; cache writes at 1.25x input for 5 minutes and 2x for 1
  hour, with model-specific cache reads). Every other model, including all Codex
  models, is **unpriced** until configured with an attributable, dated rate card:

  The following values are illustrative; use verified rates for the model and
  processing tier you actually want to estimate.

  ```toml
  [usage.prices."gpt-6-luna"]
    input = 1.25         # US dollars per million tokens
    cache_read = 0.125
    output = 10.0
    source = "https://platform.openai.com/pricing"
    as_of = "2026-10-05"
    basis = "standard API, short-context tier"
  ```

  Reports keep unpriced tokens separate instead of counting them as free and list
  the exact rate model, source, date, basis, and rates applied to every priced
  model. Overrides without `source`, `as_of`, and `basis` are rejected. Continuity
  does not infer a Codex subscription's bill or choose among unrecorded API
  processing/context tiers.
- **Tool-result tokens are estimated** as bytes / 4.

## Reports

```text
llewm usage report [--by model|session|day|client|task|tool|family]
                        [--days N | --since DATE] [--until DATE] [--client C]
                        [--session ID]
```

Each token row shows requests, input, cache write, cache read, output, the cache
hit ratio (cache read as a share of all prompt tokens), and estimated cost. The
report then lists each applied price and its provenance.

- `task` attributes a session to the task of a checkpoint carrying its session ID,
  else of the last checkpoint written during its requests (attributed by time).
- `tool` and `family` show calls, total and largest result sizes, and errors.
  Families: `mcp` (tools named `mcp__…`), `skill`, `cli` (Bash, Codex `exec`), and
  `builtin` (Read, Edit, and other client tools).
- Every report ends with each client's **session start prompt**: the prompt size of
  each session's first request (system prompt, tool definitions, instructions, and
  bootstrap), which every later request in the session also carries, mostly as
  cache reads.

## Other machines

Raw per-request rows never leave the machine that ran the session. Once a session
is finished (its end observed, or idle for 6 hours), its summary travels through
History sync as a `usage_session_summary` record: client, session, instance, first
and last request time, attributed task, session start prompt size, exact totals per
model, and calls and result sizes per tool. A summary is named by a hash of its
content, so a resumed session adds a newer summary and readers keep the newest one
per session. Reports add other machines' sessions from their summaries
(`remote_sessions` counts them; they are dated by their first request) and never
count this machine's own summaries twice.

MCP exposes the same report as `continuity_usage`. `llewm activity` shows the
request and token totals.

## Audit-only baselines and comparisons

For a new machine, start with [audit-first onboarding](onboarding.md#start-with-usage-auditing)
to install only the executable and initialize the target project.

Audit-only observation is optional and needs no model. The cleanest baseline uses
**offline collection**: disable the Llewm plugin, MCP server and skills in the
client, start a fresh session, complete the task, then run these commands yourself
at a terminal. No Llewm hooks or tool definitions need to run during the baseline:

```sh
llewm init
llewm usage ingest
llewm usage compare --baseline codex:BASELINE_SESSION --candidate codex:FULL_SESSION
# Machine-readable, with exact totals and per-model/tool detail:
llewm usage compare --baseline codex:BASELINE_SESSION --candidate codex:FULL_SESSION --output json
```

Use `claude-code:SESSION` for Claude Code. Session files
must still exist and belong to this project; explicit comparisons use local rows,
not remote summaries. Ingest after a session finishes and inspect parse issues.
Backfill with `usage ingest` after both runs so recognized Codex subagent rollouts
are included under their parent session. The narrower `usage ingest --session ID
--client C` reads that session file only and can miss separate child rollouts.
The `usage report --session ID --client C` filter also isolates an individual run.

For ongoing passive collection, `llewm client audit-hooks --client codex`
(or `claude-code`) prints hook-only JSON to merge into the client's hook settings.
It registers SessionStart, Stop, SessionEnd and PreCompact with `--audit-only`;
it installs nothing and includes no MCP server, skill descriptions, status message,
tool hooks or model context. Remove the full plugin's hooks when using this profile.
Hook registration still depends on the client's support and approval mechanism;
these generated snippets have automated contract tests, not native live-client
qualification. Offline collection remains available independently of hooks.

`llewm mode audit-only` sets the project's hooks to passive observation;
`llewm mode full` restores normal behavior and `llewm mode` reads the
setting. The persisted top-level `mode` in `.llewm/continuity/config.toml` defaults to
`"full"`; unknown values are rejected. The `hook --audit-only` flag forces passive
observation for that hook without changing project configuration.

Successful audit-only hooks return **no stdout or model-visible context**. They
record only a session/lifecycle marker and the observed hook mode, and ingest usage
at end boundaries. They skip startup background catch-up, bootstrap/guidance,
reminders, automatic memory sync, roadmap scans, presence and commit attribution.
Tool inputs, prompt text and raw hook payloads are not persisted even when raw
payload capture is enabled for full mode. Parser incompatibilities remain errors.
Explicit CLI reads and writes remain available; mode is not a security boundary.
MCP startup is refused in audit-only mode, but an already-running MCP process and
already-loaded plugin/skill descriptions cannot be removed from a live session.
Disable them and start a fresh session for an uncontaminated baseline. Existing
repository-native instructions still load according to the host; record whether
they contain Llewm-specific workflow requirements in either condition.

The comparison reports candidate minus baseline: a negative delta is a reduction.
Uncached input, cache write, cache read, output, reasoning (a subset of output),
startup prompt, tool calls, result bytes, reported errors and request-window time
stay separate. Zero-baseline percentages are null. Cost deltas are omitted when
either run has unpriced tokens; applied rate-card provenance accompanies priced
runs. Observed hook modes are evidence about those hooks, not proof that the full
plugin was absent. Offline mode is unknown rather than guessed from today's config.

For credible comparisons, use the same task, starting code revision, client/model
and settings, equivalent permissions and cache conditions, fresh sessions, and the
same correctness checks. Repeat tasks in both orders to reduce warm-cache and
learning effects. Include repairs and subagent usage. Verify equal outcomes
separately: fewer tokens on an incomplete task is not efficiency. The request
window includes idle time and is not time to first productive action or task
duration. Source offsets expose known unread bytes but do not prove complete
provider billing; absent/partial source coverage and differing model/client sets
produce warnings. The report describes observed differences, not causal savings.

Existing tool/family reports measure retrieval transport volume without saving
tool bodies. They do not yet identify repeated reads of unchanged references or
prove that a retrieved result helped the task. Those require further attributed
instrumentation; this mode does not invent that evidence.

### Switch an existing setup to audit-only

This works after ordinary onboarding, during an active trial, or after adoption.
It preserves saved knowledge and usage; it does not adopt, exit, reset or delete
the project's trial receipt. An exited trial refuses writable opens: audit mode
is not a reactivation path. Use the [reviewed recovery guidance](trials.md#disconnect-and-retain-recovery)
for that case.

1. Finish the current work and preserve enough ordinary project prose to work
   without querying Continuity. Review root instructions and global/client
   guidance for requirements to call Llewm; record their presence in the
   comparison and explicitly review changes needed for a prose-only baseline.
2. Stop the project's active MCP process and hooks, then run
   `llewm mode audit-only` from the target Git root at a terminal. Read back with
   `llewm mode`. This changes project hook behavior, not client configuration.
3. Disable the full Llewm plugin, its MCP registration, skills and full hooks in
   the client. Preserve unrelated settings and keep a record of what changed.
   Shared user-scope integrations may serve other projects; use a separate
   client profile/configuration when necessary.
4. Choose offline ingestion, or review and merge
   `llewm client audit-hooks --client codex` (or `claude-code`) into hook settings.
   This command only prints JSON. Do not keep duplicate full-plugin hooks.
5. Restart the client and parent IDE as needed and start a fresh session. Confirm
   no Llewm bootstrap, MCP tools or plugin/skill instructions are loaded. Then
   collect usage as described above. Existing session context cannot be undone
   by changing the mode.

To return, remove the manually registered `--audit-only` hooks (their flag would
remain passive even in full mode), run `llewm mode full`, restore the reviewed
full client integration, and restart into a fresh session. Follow
[client verification](client-integrations.md#verify-the-fresh-client) for adopted
projects. During an active trial, full local hooks and session recovery are available
again; `trial adopt --reviewed` separately records ongoing use. Changing mode
does not perform that adoption. Keep optional History sync and
reporting as separate choices; audit mode does not erase their configuration
or stop independently launched processes.

## Code statistics

`llewm stats code` counts lines added and removed from Git history with no
model (plan item 8i). It reads `git log --numstat --no-renames` in every
repository of the project, skipping merges unless `--merges`, and groups by
`language` (default; from the file name or extension), `extension`, `day`,
`author`, `commit`, `directory` (the first two path segments), or by records:
`task` and `node` from the task and roadmap-node anchors of narratives recorded
for each commit (`node` also from plan items citing it), and `plan-item` from
plan items whose evidence cites `commit:HASH` (a prefix of 7 or more characters
matches). A commit tied to several keys counts toward each; `(no record)` holds
the commits tied to none. `--since`, `--until`, `--days`, and `--range` (a Git
revision range) select commits; `--output json` returns the report.

Generated, vendored, and lock files (`vendor/`, `node_modules/`, `go.sum`,
`*.lock`, `package-lock.json`, `pnpm-lock.yaml`, `*.min.js`, `*.pb.go`) are
counted apart as `excluded`; `[stats] exclude` in `config.toml` adds patterns (a
pattern ending in `/` is a directory anywhere in a path, others match the path
or its base name). Counts are computed on demand rather than cached. Runtime depends on the
repository and selected range. There is no MCP tool: an agent runs the command
and receives its bounded report.
