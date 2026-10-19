# Attribution records

Continuity stores agent attribution as versioned, append-only records. This is an
observation interface: it does not enforce policy, install hooks, execute actions,
or authenticate a client claim.

Actor invocation records retain session and parent links, client and model
details, and the source of the claimed identity.

Missing identity data is stored as `unknown`; a model name is never treated as a
unique actor. Cross-record references include the project ID in their foreign key.
Text is bounded and redacted before insertion. Duplicate IDs fail instead of
replacing existing observations.

## Invocations

Lifecycle adapters append an invocation observation for each accepted hook event.
The client, session, and model values are labeled `client_hook_claim`; this records
what the hook reported and does not authenticate the actor or prove client
configuration. Raw hook retention is disabled by default. CLI
`llewm activity --limit N` returns recent invocations (CLI only); the default
limit is 20 and the maximum 100.

The invocation preserves model effort when a client or provider exposes it.
Requested effort is stored separately from effective or reported effort, with
provenance, the raw client value, and an open normalized vocabulary so new provider
values do not fail ingestion. Missing values remain unknown. A commit narrative
can point to the exact same-project invocation that created it. Do not
infer effort or an invocation link from model identity, latency, answer quality,
reasoning-token usage, or temporal proximity.

## Client coverage

What each client and platform does is recorded as coverage cells, not as
attribution records; see [coverage](coverage.md).
