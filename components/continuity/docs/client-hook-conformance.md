# Client hook conformance

Codex and Claude Code own hook scheduling, trust, output parsing, and context
delivery. Their documented and observed behavior can change independently of
Continuity. A passing test therefore has a bounded scope: one exact client version,
Continuity commit, plugin configuration hash, execution mode, and trust state. It is
not a permanent provider guarantee.

<!-- continuity:contract client-hooks -->
## Conformance requirements

kind: constraint
scope: Continuity hook delivery in Codex and Claude Code
review: after any trigger listed under "When live qualification is required"

- On each plugin hook change, structural tests verify the configured hook files,
  the canonical executable, lifecycle fixture coverage, the Codex asynchronous
  PostToolUse limit, and Claude Code synchronous PostToolUse.
- On each relevant change, deterministic integration tests cover disabled and
  enabled behavior, bounded reason text, suppression, checkpoint rearm, invalid
  thresholds, and concurrent atomic edge claims.
- When a listed client, plugin, hook, policy, trust, packaging, or delivery
  trigger changes, the affected client is requalified on an exact installed
  version using the live matrix, and the result is recorded as coverage cells.
- Live qualification records the exact client and Continuity identity, the
  effective hook configuration, execution mode and trust state, and payload-free
  event references, never transcripts or raw hook payloads.
- Qualification is current only for its exact recorded version; stale or unknown
  qualification keeps reminder policy opt-in and bars claims of provider
  compatibility or a recommended default.

## Three verification layers

1. **Package contract, on every change.** Normal Go tests parse both plugin hook
   files structurally. They require Codex PostToolUse to remain asynchronous with its
   context limit, Claude PostToolUse to remain synchronous, and both commands to use
   the canonical executable. Fixture coverage must match configured lifecycle events.
2. **Deterministic integration, on every change.** Normal Go tests exercise disabled
   policy, enabled hook output, bounded reason text, suppression, checkpoint rearm,
   invalid thresholds, and concurrent atomic edge claims without contacting a model.
3. **Live client conformance, when provider behavior may have changed.** Run the
   external client matrix below on an exact installed version. Keep prompts and answer
   keys outside the repository. Record aggregate outcomes and attributable evidence,
   not transcripts or raw hook payloads.

Package and integration tests protect Continuity's side of the interface. Only the
live layer verifies that a provider actually schedules the hook and places returned
context where the next eligible model request can use it under the client's
synchronous or asynchronous delivery contract.

## When live qualification is required

Treat prior live evidence as stale and rerun the affected client matrix when any of
these changes:

- client version, distribution channel, or execution mode;
- provider hook documentation relevant to scheduling or output;
- plugin hook configuration, command, matcher, trust state, or packaging;
- Continuity hook input decoding, output JSON, reminder policy, or edge persistence;
- sync/async selection, timeout, or additional-context limits; or
- an observed missing, late, duplicated, blocking, or malformed reminder.

Also rerun both supported clients before enabling or changing any recommended
threshold profile. Routine offline CI must not silently claim live conformance when
credentials, network access, cost approval, or an interactive client are unavailable.

## Live matrix

Run each case in a disposable Git project with raw payload retention disabled. Use an
explicit semantic checkpoint and explicit thresholds. Keep provider/model statements
separate from the machine-observed event ledger.

| Case | Required observation |
| --- | --- |
| Enabled event edge | One successful tool event produces one reminder request. Claude's first post-tool response identifies the advisory and reason. Codex's first eligible response after background-hook completion identifies them at the next safe point; an already in-flight response may precede it. |
| Suppression | A second qualifying event before a new checkpoint produces no second request or context item. |
| Rearm | A newer semantic checkpoint permits exactly one new reminder request. |
| Disabled | Zero thresholds produce no freshness evaluation, request event, or model-visible advisory. |
| Invalid policy | Validation fails visibly to the hook runner while the already-completed tool and client session continue normally. |
| Concurrency | Concurrent qualifying events produce one atomic request for the selected checkpoint. |
| Age edge | An explicit age threshold produces `age_threshold` without requiring an event threshold. |
| Trust/package refresh | A fresh installed client loads the intended plugin hash and runs PostToolUse under its normal trust workflow. |

For Codex, test active-turn background delivery, idle completion followed by the
next user turn, and the normal trust flow as separate cases. During an active
turn, the safe point follows in-flight model requests and tool calls; an initial
answer before the advisory is not by itself a failure. When idle, hook completion
must not start a new turn. Record advisory insertion and turn boundaries without
persisting transcript text; model acknowledgment and request-ledger observations
remain separate evidence. Do not infer idle behavior from an active-turn pass.
This criterion retains asynchronous Codex PostToolUse under
`decision:codex-async-safe-point-delivery` (2026-10-04), matching
[Codex's documented scheduling](https://learn.chatgpt.com/docs/hooks#how-background-hooks-run).
For Claude Code, verify synchronous PostToolUse ordering in the event stream. Test
interactive behavior separately from one-turn print mode; success in one mode does
not qualify the other.

IDE nested-tool scheduling qualification is currently deferred for both Codex and
Claude. Do not infer it from print-mode passes, and do not block unrelated recovery
work on it. Reopen this row only for a deliberate client-mode qualification pass or
before relying on IDE hook delivery for a recommended default policy.

## Evidence and qualification state

Record each live result as a coverage cell (`llewm coverage record`; see
[coverage](coverage.md)) against the matching requirement in `docs/requirements.md`,
with verification `observed` or `tested`. The cell's client version, implementation
note, and links carry the identity that matters:

- client name, exact version, and platform (the cell's client, client version, and
  platform);
- Continuity commit, plugin package, hook configuration hash, execution mode, and
  trust state (the implementation note, and links such as `commit:HASH`);
- the checkpoint and payload-free reminder-request event IDs (links); and
- failures, skipped cases, and cost or credential constraints (the implementation
  note, with status `partial` or `failing`).

A cell is evidence for the version it names. `llewm coverage` and `llewm doctor` flag a cell whose verified client version differs from the last version
observed for that client, and a requirement with no cell is unknown. Unknown or
stale coverage keeps reminder policy opt-in and blocks claims of provider
compatibility or a recommended default; it does not disable observation-only hooks
or deterministic CLI/MCP freshness reads.
