# Attribution records

Continuity stores agent attribution as versioned, append-only records. This is an
observation interface: it does not enforce policy, install hooks, execute actions,
or authenticate a client claim.

Schema version 1 retains actor invocation records:

- actor invocations, including session and parent links, client and model
  details, and the source of the claimed identity.

Policy references, incidents, scoped restrictions, action-policy documents, and the
effective-policy compiler were removed in 6e1 (2026-10-01). None had been
populated; the engineer's commit policy lives in the global `AGENTS.md`.

Missing identity data is stored as `unknown`; a model name is never treated as a
unique actor. Cross-record references include the project ID in their foreign key.
Text is bounded and redacted before insertion. Duplicate IDs fail instead of
replacing existing observations.

## Invocations

Lifecycle adapters append an invocation observation for each accepted hook event.
The client, session, and model values are labeled `client_hook_claim`; this records
what the hook reported and does not authenticate the actor or prove client
configuration. Raw hook retention is disabled by default. CLI
`llewm activity --limit N` returns recent invocations (CLI only since MCP
API 98); the default limit is 20 and the maximum 100.

Migration 014 preserves model effort when a client or provider exposes it.
Requested effort is stored separately from effective or reported effort, with
provenance, the raw client value, and an open normalized vocabulary so new provider
values do not fail ingestion. Missing values remain unknown. Migration 015 lets a
commit narrative point to the exact same-project invocation that created it. Do not
infer effort or an invocation link from model identity, latency, answer quality,
reasoning-token usage, or temporal proximity.

## Client coverage

What each client and platform does is recorded as coverage cells, not as
attribution records; see [coverage](coverage.md).
