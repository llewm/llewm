# Machine-local agent coordination

The orchestration executable includes a separately started local broker for
already-running clients. It holds durable messages and advisory session state
for **one project on one machine**, independently of the shared work coordinator.
It has a distinct database identity and only listens on literal loopback IPs.
Continuity remains independently usable without either service.

The local broker reuses private storage and engineer authentication, not shared
plan state. An enrolled engineer's `work` permission permits local publishing;
read-only members can inspect broker identity. Session inbox reads require the
session's authenticated engineer. All project members are in the same trust
boundary: addressed messages are routing, not private-message ACLs. Machine,
agent, task, scope and session labels remain attributable caller assertions.
They cannot establish ownership, process liveness or execution authority.

## Start locally

Build from `components/orchestration`:

```sh
go build -o ../../dist/llewm-orchestrate ./cmd/llewm-orchestrate
```

From the repository root, initialize a **new** database and enroll the local
engineer. Use a stable project ID and a machine label; do not use a team database
or the Continuity database. New files/directories receive the existing native
private permissions (including Windows ACLs).

```sh
dist/llewm-orchestrate broker-init \
  --db .llewm/orchestration/local/broker.db --project llewm --machine workstation
dist/llewm-orchestrate broker-member \
  --db .llewm/orchestration/local/broker.db --id engineer --permissions work \
  --token-file .llewm/orchestration/local/engineer.token
dist/llewm-orchestrate broker-serve --db .llewm/orchestration/local/broker.db
```

The default local listener is `127.0.0.1:9468`; another local project needs its own
port/database. The shared coordinator still defaults to port 9467. Stop the local
broker with Ctrl-C. Starting it installs no OS service. To rotate a credential,
repeat `broker-member` with a new token-file path; `broker-revoke --db PATH --id ID`
revokes one. Each engineer uses their own credential. Never put a credential in
messages, source, a checkpoint or shared memory.

## Running-agent MCP adapter

Start an independent stdio adapter for each actual client session:

```sh
dist/llewm-orchestrate broker-mcp \
  --endpoint http://127.0.0.1:9468 \
  --token-file .llewm/orchestration/local/engineer.token \
  --session ACTUAL_CLIENT_SESSION_ID --agent codex --task implementation
```

Configure the other agent with its own actual session ID, agent label and task.
Instead of `--session`, `--session-env VAR` reads the ID from an environment
variable the host sets for each session, so one static configuration serves every
session. Claude Code sets `CLAUDE_CODE_SESSION_ID` for the MCP servers it starts:

```sh
claude mcp add --scope local llewm-broker -- /ABS/llewm-orchestrate broker-mcp \
  --endpoint http://127.0.0.1:9468 --token-file /ABS/engineer.token \
  --session-env CLAUDE_CODE_SESSION_ID --agent claude-code
```

Codex runs one app-server for all threads and gives MCP servers no per-thread
session ID, so a per-session adapter cannot be configured. Codex agents use the
CLI (`broker-request`/`broker-pull`) with the session ID that the hook hint names.
A static configuration has no task; see the task binding note below.
This is a separate adapter from `mcp` (the reviewed job queue). No allowed plan is
needed for project-level messages. Configuration is explicit: this release does
not discover host sessions, edit client configuration or automatically install
hooks. A restarted session needs its new session ID. A task ID is optional; use
it when replies must remain discoverable after that session ends. When changing
task bindings, update the adapter and hook together and use a separate cursor.

**Task binding.** Each hook heartbeat publishes the hook's configured task, and a
task change starts a new scope. A session whose explicit `broker_state` sets a
task loses it at the next heartbeat from a task-less hook. Its pulls with that
task are then refused, and replies routed to that task are not seen. With static
host configuration, leave the task unset everywhere and use session or project
routing. Task routing to offline recipients needs a per-task hook and adapter.

The five tools are:

| Tool | Meaning |
| --- | --- |
| `broker_info` | Read broker ID, project, machine and `scope: machine` |
| `broker_state` | Publish `active`/`ended`, TTL, concise scope and refs for this configured session |
| `broker_send` | Store a broadcast, session/task-addressed message or correlated reply |
| `broker_ack` | Explicitly acknowledge a message by `reply_to` |
| `broker_pull` | Read from a cursor, or wait up to 20 seconds for relevant changes |

State TTL is 30–1800 seconds. Publish state before sending or pulling. For example,
call `broker_state` with `request_id: start-1`, `state: active`, `ttl_seconds: 600`,
`summary: Editing orchestration broker tests`, and repository-relative refs.
Use a fresh request ID for each new observation. Repeat identical input with the
same request ID after an uncertain upload; changed input conflicts.

An empty state summary/omitted refs preserve the previous scope for the same task,
so hook heartbeats do not erase an agent's announcement. A task change starts a
new scope. `ended` is terminal for that session ID; it is appropriate for actual
session termination, not a turn-ending Stop event. An expired session may renew
contact; expired contact alone neither completes work nor releases a shared claim.

## Request and reply example

1. Both agents publish their active state. Codex sends:

   ```json
   {"request_id":"verify-1","to_task":"review","text":"Read checkpoint checkpoint:EXACT and confirm all sections are present; keep your own task unchanged.","refs":["checkpoint:EXACT"]}
   ```

2. Claude calls `broker_pull` with `after: 0`. The feed includes the message's
   stable `id`, authenticated actor, session/task and timestamp. Claude inspects
   it and acknowledges using `broker_ack` with a new request ID and `reply_to`
   equal to that message ID.
3. Claude sends a response with a new request ID, `reply_to` equal to the same
   message ID, and the verification result. Omit recipient fields for replies:
   they route to the original sender's task when present, otherwise its session.
4. Codex pulls with `reply_to` equal to the request ID **from the receipt's `id`
   field**, not the caller's `request_id`. An acknowledgment and a substantive
   reply are separate events. Neither is independent proof of the reported check.

Without recipient fields, a message is project-wide. `to_session` and `to_task`
are mutually exclusive. A session recipient must already be registered; a task
recipient may be offline and register later. A new session using the same task
can receive its messages and correlated replies. Old session messages remain
readable by their engineer but do not silently redirect to a replacement session.

Messages supply context and requests, not permission. An agent must apply its
current instructions and authorization before acting. Publishing does not launch
an agent, interrupt a client, modify task selection or change reviewed assignments.

## Cursors and bounded waits

A feed includes `broker.id`, `next_after`, up to 100 matching events, a snapshot
of the 256 most recently updated sessions and the broker's observation time.
`sessions_truncated` says when older sessions were omitted. Use `watch_session`
for an exact session snapshot. Session state is `active`, explicitly `ended`, or
`expired` as derived from the broker's clock. None proves OS process liveness.

Save the broker ID and cursor **together**, scoped to session, task and filters.
A nonzero cursor requires matching `broker_id`; a future cursor or a different
broker is refused. Start at zero for a new consumer/filter or a replacement broker.
Advance only after processing the returned page. Delivery can repeat after a
crash; deduplicate by stable event ID. No automatic acknowledgment occurs on read.
A full page stops at its last event; a shorter page advances across unrelated
traffic to the consistent snapshot's high watermark.

`wait_seconds: 1..20` enables a bounded server-side wait from the same cursor.
`reply_to` filters to acknowledgments/replies to one request; `watch_session`
filters to one session's state events and also returns when contact has expired
or the session has explicitly ended. Without those filters it waits for any
routed message or session observation. The reply contains a `reason`: `changes`,
`snapshot`, `timeout`, `session_ended`, or `contact_expired`. A state-change event
may return `changes` with an ended/expired snapshot. A missing watched session is
not interpreted as an exit; the request can time out.

Waits repeatedly recheck the durable store without holding a transaction while
sleeping. A write between a read and a wait is found using the same cursor, and
writes from another process are visible. Cancellation releases the waiter.
Credential revocation is rechecked on each snapshot. Timeouts and expired contact
are conditions for the agent to assess, not authorization to reclaim work.

## Hook participation

`broker-hook` accepts a bounded Codex/Claude-style hook JSON envelope on stdin.
It reads only `session_id` and `hook_event_name`, publishes a heartbeat, checks
local changes, and emits a bounded `hookSpecificOutput.additionalContext` hint
when another session has posted messages or acknowledgments routed to it. Other
sessions' state heartbeats are not counted, because every hook publishes one. It does not inject message
bodies or retain raw hook/tool input. Supported events are SessionStart,
UserPromptSubmit, PreToolUse, PostToolUse, PreCompact, PostCompact, Stop and
SessionEnd. SessionEnd publishes `ended`; Stop keeps the session active.

Example invocation for a reviewed hook configuration:

```sh
dist/llewm-orchestrate broker-hook \
  --endpoint http://127.0.0.1:9468 \
  --token-file .llewm/orchestration/local/engineer.token \
  --agent codex --task implementation \
  --cursor-file .llewm/orchestration/local/ACTUAL_SESSION_ID.implementation.cursor
```

The host supplies stdin; configure an absolute executable/token/cursor path if its
working directory can vary. The adapter's session and task must match the hook.
Use an independent cursor file for each actual session/task. Alternatively,
`--cursor-dir DIR` derives a private `SESSION.TASK.cursor` file from each envelope's
`session_id`, so one static host hook serves every session. IDs outside
`[A-Za-z0-9_-]` are hashed into the name. The hint names the session ID, so an
agent without a session-aware adapter can pull through the CLI. Hook delivery tracks
notification hints only. The agent's inspected inbox cursor is separate and must
still be pulled routinely, even if a host drops a hint. Do not run a context hook
asynchronously if the host cannot surface its output.

The hook has a three-second request budget, uses a private cursor file and advances
it only after successful output. A concurrent invocation that finds the private
`.lock` file exits successfully without output or cursor change, because hosts run
hooks for parallel tool calls at the same time. A crashed hook may leave that
lock, and then every later hook for that session is a silent no-op. Verify that
no hook is running before removing it. An unavailable broker returns an error
without advancing the cursor. Hook errors must be configured as advisory by the
host; this command is not an approval gate. Real host rendering/delivery remains
a deployment qualification check; automated tests exercise the envelope contract.

CLI users and custom wrappers can instead use `broker-info`, `broker-request
--file COMMAND.json`, and `broker-pull --file QUERY.json`, each with endpoint and
token-file flags. The command JSON is the MCP mutation's fields plus
`schema_version: 1`, `action: state|message|ack`, `session_id` and optional `task_id`.
State uses `agent`, `state`, `ttl_seconds`, `text` and `refs`. Pull JSON uses
`session_id`, optional `task_id`, `after`, optional `broker_id`, `reply_to`,
`watch_session` and `wait_seconds`.

## Cross-machine boundary

This release implements local coordination; **no bridge or remote delivery exists**.
A future bridge must exchange typed envelopes through an explicitly configured
shared authority, never replicate a writable broker/team/Continuity SQLite store.

- Preserve origin broker ID, stable event ID, project ID, authenticated origin and
  original timestamp; retain forwarding and remote-acceptance provenance separately.
- Forward only explicitly selected message/observation types. Local absolute paths,
  process IDs, cursor files, credentials and raw hook input stay on the machine.
  A shared work-scope announcement can use repository-relative refs and commit IDs.
- Persist an outbox and deduplicate at the receiving authority. A remote receipt
  advances the outbox only after durable acceptance. Retrying cannot create another
  message or turn the relay into the original author.
- Distinguish `locally_stored`, remote acceptance, delivery and explicit recipient
  acknowledgment. Current receipts assert only local storage or an explicit ack.
- Local observations remain advisory. Submit shared assignment/claim operations
  to the single team authority; an offline queue records pending intent, never a
  globally exclusive claim. Reconcile stale results through existing review gates.
- Remote connectivity, permissions and revocation are independent of local delivery.
  A disconnected bridge must not block local agents from exchanging local messages.

Messages and state survive broker restart. History is append-only, with bounded
individual records/reads; automatic retention/compaction is not implemented. Keep
curated scope and references, not transcripts. Known credential patterns are redacted
before persistence, but arbitrary secrets cannot be reliably recognized. Back up
only while the broker and other local writers are stopped, preserving SQLite
sidecars as for the shared coordinator. No existing database is migrated.
