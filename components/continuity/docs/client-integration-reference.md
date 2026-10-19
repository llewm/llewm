# Client integration reference

The `llewm` binary must be on `PATH` before either plugin is enabled.
All bootstrap interfaces include [engineer-wide guidance](global-guidance.md)
alongside project instructions when configured. Each client must inherit the same
`CONTINUITY_GLOBAL_HOME` if you override its default location.
When a checkpoint exists, the bootstrap also presents a bounded single-line
conversation-title suggestion derived from the project and current task. This is
startup context only: clients remain responsible for title generation and may
ignore it.
For a first installation or project adoption, start with [guided onboarding](onboarding.md).
Follow [Installation](installation.md) for platform-specific setup and the
`llewm` executable. Use `llewm` in plugin commands. Restart the client and its
parent IDE after changing `PATH`, or launch them from a terminal with the updated
environment; a shell alias alone is insufficient for hooks and MCP subprocesses.

Hooks and MCP startup require the configured database schema to match the running
binary. If an installed update contains pending migrations, they fail with
`migration_required` before recording an event or starting the server; they never
apply the migration themselves. Use the explicit, backup-gated workflow in
[Installation](installation.md). This failure is intentional and keeps an automatic
PostToolUse hook from racing ahead of the operator's backup.

MCP `continuity_info` reports the running server's application version, Git commit,
dirty-build flag, Go version, API version, executable path, process start time, and
the database's latest applied migration. It fingerprints the executable at process
startup and compares that fingerprint and embedded commit with the file currently
installed at the same path. `health.status` is `current`, `stale`, or `unknown`;
`restart_required` is true only for a confirmed mismatch. Unreadable files and
missing metadata stay unknown rather than being mislabeled as current.

Compare this response with `llewm version --output json` after an update. A stale MCP
bootstrap response also carries a bounded `runtime_warning`; it does not displace
the budgeted bootstrap Markdown. Plugin package versions are separate client-side
identity and remain explicitly unknown until launchers supply trustworthy package
metadata.

Claude Code commands below resolve the repository root with `git rev-parse
--show-toplevel`, so they work unmodified whether you are at the repository root
or still inside `components/continuity` after a build. Other paths in this
document are relative to `components/continuity/`.

## Codex

The package under `plugins/llewm` is a Codex plugin with stdio MCP, lifecycle
hooks and skills. From the Llewm repository root, use the installed client's
marketplace workflow shown below; check its `--help` if the interface differs.

```sh
codex plugin marketplace add ./
codex plugin add llewm@llewm
```

These commands register the repository-root `.agents/plugins/marketplace.json`
and install its local Llewm package. They were checked against the installed
CLI's help; fresh-client activation still needs verification after installation.

The `SessionStart` hook returns the compiled bootstrap as additional developer
context. `PostToolUse` can return the opt-in checkpoint reminder described below;
the other configured hooks record lifecycle observations without semantic
checkpoint creation. Hook-driven usage ingestion is described in [usage](usage.md).

Hook trust is separate from MCP tool approvals and command rules. In the observed
Codex IDE 0.160.0 setup, removing Continuity's plugin hook trust entries stopped
hook execution without a visible IDE review notice or model-visible warning.
The model could still read native project instructions; that did not establish
bootstrap delivery. The Codex CLI displayed hook review immediately. After the
user approved the installed hooks there, a fresh IDE chat received the bootstrap
and its SessionStart event was recorded. For this version, use the CLI's `/hooks`
review when the IDE does not expose one, then reload the IDE and start a fresh
chat. Do not manufacture trust hashes or equate plugin installation with hook
approval. This is a bounded observed setup path, not a guarantee for later clients.
See [official hook trust guidance](https://learn.chatgpt.com/docs/hooks#review-and-trust-hooks)
and [client-hook conformance](client-hook-conformance.md).

Source packages, installed caches, hook trust, permissions, and live delivery
need separate verification. Historical qualification applies only to the exact
versions and modes observed; detailed trial logs remain in the private archive.

## Checkpoint reminder surfacing design

API 15 provides the deterministic read needed by a client integration, but the
hook integration surfaces reminders only when explicitly enabled in the local
project `.llewm/continuity/config.toml`. Both thresholds remain disabled by default:

```toml
[checkpoint_reminder]
remind_after_seconds = 0
remind_after_events = 0
```

Set either value to a positive integer to opt in. The client integration passes
those explicit age and event thresholds to `work freshness` only after the
triggering lifecycle event has been recorded. Negative values and event thresholds
above the documented API maximum are rejected rather than reinterpreted.

Surfacing is advisory and edge-triggered. The first recommendation after a
selected semantic checkpoint may add one short, attributable context item asking the
agent to consider the checkpoint skill. Later events must not repeat that item until
a newer semantic checkpoint rearms the reminder. A missing checkpoint is one such
edge, not a reason to inject a reminder after every tool. Delivery observations must
be payload-free and separate from the checkpoint itself; a surfaced recommendation
does not prove the agent saw it and must never create semantic state automatically.

The implementation uses a client event that can deliver bounded model-visible context without
blocking or rewriting the triggering action. Codex and Claude Code currently
document `PostToolUse` additional context, including delayed delivery for a Codex
background hook. Codex keeps this hook asynchronous and caps the reminder
independently of bootstrap output. Claude runs it synchronously because asynchronous
context is deferred to the next conversation turn and can miss a one-turn print-mode
session. Fall back to observation-only behavior when a client does not support the
output contract. Compaction hooks remain evidence signals rather than transcript
capture or automatic checkpoint creation. Source contracts checked 2026-09-15:
[Codex hooks](https://developers.openai.com/codex/hooks) and
[Claude Code hooks](https://code.claude.com/docs/en/hooks).

Codex's asynchronous advisory is eligible at the next safe point after the hook
finishes, after in-flight model requests and tool calls. It may follow an initial
post-tool answer within the same turn. If the turn is idle, delivery waits for the
next user turn and does not start one. Historical active-turn trials observed
this ordering; they do not establish idle-session or current IDE compatibility.

Provider behavior is not treated as a permanent contract. The required offline,
integration, and version-pinned live gates are defined in
[client hook conformance](client-hook-conformance.md). A client upgrade, hook contract
or configuration change, trust change, or delivery anomaly makes the affected live
qualification stale until its matrix is rerun.

Automated tests cover one edge, suppression before a new checkpoint, rearming, and
an eight-way concurrent claim. Before enabling any profile by default, verify
separately in each supported client:

- one reminder is delivered after crossing each configured policy edge;
- further tool events do not repeat it before a newer checkpoint;
- saving and reading back a newer checkpoint rearms the evaluation;
- disabled thresholds produce no model-visible output;
- concurrent hook completions do not duplicate the edge; and
- hook failure or unsupported output leaves normal tool processing unchanged.

Measure configured thresholds, evaluation and recommendation counts, delivery
observations, time to the next semantic checkpoint, and reminders ignored. These are
operational observations, not evidence of inferred task progress. Select any later
recommended policy from real-session results rather than embedding one in the
Continuity service.

## Roadmap scan surfacing design

`SessionStart` can also run the deterministic roadmap status comparison from
[roadmap control](roadmap-control.md) before returning the bootstrap, gated by the
same opt-in shape as the checkpoint reminder above:

```toml
[roadmap_scan]
on_session_start = false
```

When enabled, the handler compiles the bootstrap first, then calls
`Roadmap.Status` and appends its bounded `LifecycleContext` line only for
`changed` freshness or an explicit due reason; `current` or not-due state adds
nothing beyond the bootstrap. Any scan error is swallowed so the optional check
never displaces bootstrap delivery — this is stricter isolation than the
checkpoint reminder, which surfaces hook errors instead of silently continuing.
Automated CLI integration coverage exercises disabled, current/not-due,
changed/due, and failure-isolated scan behavior against the compiled hook output.

Historical Linux trials exercised disabled, current/not-due, changed/review-due,
and isolated roadmap-failure paths in Claude Code. Reminder trials exercised
print-mode delivery, suppression, rearming, and a bounded concurrent claim.
A Codex IDE trial recorded SessionStart without PostToolUse, while a separate
print-mode trial delivered the reminder. These observations are execution-mode
specific, not broad reliability or evidence that later versions behave identically.
Native Windows/macOS and affected fresh-client cases still require qualification.

Keep the distinctions that those trials exposed:

- executable/plugin installation does not prove hook trust or delivery;
- a fresh stdio MCP probe does not update the server attached to an existing chat;
- disabled/current/failure cases need their own checks, not inference from a due case;
- a reminder may be produced too late to influence a short session; and
- retain failed or repaired attempts separately from clean qualification passes.

## Claude Code

The repository-root `.claude-plugin/marketplace.json` publishes the Claude
package from `components/continuity/plugins/claude-code`. Install it at local
scope so both the Claude Code CLI and IDE integrations discover it for this
checkout without a launch flag:

```bash
claude plugin validate .
claude plugin marketplace add ./ --scope local
claude plugin install llewm@llewm --scope local
claude plugin list --json
```

The explicit `./` identifies a local marketplace; `.` alone is not accepted by
the marketplace-source parser. Local-scope registration and enablement are
machine-specific Claude state and must not be committed. The marketplace catalog
and plugin manifest are project packaging metadata and are tracked.

Claude's local IDE surfaces and CLI share plugin configuration. Restart the IDE
and its parent process after installation or an environment change, then confirm
`llewm` is enabled through `/plugins`. The IDE process must inherit a
`PATH` containing `llewm`, because the package's hooks and MCP server invoke
that command directly.

For one-session source testing, bypass the persistent install with:

```bash
claude --plugin-dir "$(git rev-parse --show-toplevel)/components/continuity/plugins/claude-code"
```

`--plugin-dir` takes precedence over an installed package with the same name for
that process. A relative-path plugin from a marketplace added as a local directory
loads in place; use `/reload-plugins` or restart Claude Code after source changes.
The plugin uses the same binary, SQLite store, and hook response format in either
mode.

## Checkpoint skill

Both packages include `skills/checkpoint/SKILL.md`, backed by
`continuity_checkpoint` with a `continuity_bootstrap` readback. It captures the
objective, completed work, changed artifacts, verification claims, failed
attempts, source references, and the next recommended action.

- Codex CLI/IDE: use `/skills` or type `$` and select the plugin's checkpoint
  skill. The skill name is `checkpoint`; select the Llewm Continuity entry if
  another plugin uses the same name. See [Codex skill invocation](https://learn.chatgpt.com/docs/build-skills).
- Claude Code: use `/llewm:checkpoint`. Plugin skills use the manifest
  name as their namespace; see [Claude plugin skills](https://code.claude.com/docs/en/plugins).
- In either client, ask "Save a Continuity checkpoint for this task before I
  switch sessions." The skill remains available for normal model selection.

For Claude development, use the local marketplace installation above for normal
CLI and IDE work, or the explicit `--plugin-dir` override for a one-session package
test. For an installed Codex copy, refresh it through its existing local marketplace
workflow and start a new thread. Editing repository source alone does not establish
that a running client has reloaded the changed skill.

The skill requires the structured MCP schema described in the
[checkpoint contract](checkpoints.md). Restart stale MCP processes after updating
the binary. At API 19 it first calls the read-only prospective preflight and saves
only a payload reported ready; older MCP processes lack that tool and must report
that readiness was not checked before persistence. Success includes a returned
checkpoint ID and a bounded bootstrap containing that ID and the essential handoff.
A tool failure must be reported; a prose summary alone is not a persisted checkpoint.
Current save paths also recompile the exact received payload against the startup
budget before writing, so the safety property does not depend on skill selection,
tool ordering, or reuse of a preflight result for different content.

At MCP API 107, a confirmed writer-floor refusal returns `deferred: true` and a
device-local recovery ID instead of dropping the structured checkpoint. This is
not a saved checkpoint. Restart the client and run
`llewm checkpoint recovery apply ID`; require the receipt to confirm both
readback and envelope removal. Failed apply retains the envelope, and bootstrap
continues to report it. This narrow continuation of the already-authorized
checkpoint operation does not require a second CLI-fallback approval.

Clients may expose plugin tools through a lazy catalog rather than the initial
prompt's tool list. The skill searches that catalog before calling MCP unavailable.
If connection recovery or restart cannot restore the tools, an explicitly
user-authorized CLI fallback may save a checkpoint only when the installed CLI
exposes the same structured fields, atomic predecessor-action reconciliation,
dry-run validation, structured save receipt, and readback checks as MCP. No field
may be collapsed into the summary and no reduced-fidelity checkpoint is accepted.
If any required capability is absent, the handoff remains unsaved until MCP or a
current CLI is available. This is a recovery path, not permission to bypass an
available MCP checkpoint tool.

At API 30 the same preflight/save pair also projects and atomically applies explicit
predecessor `action_updates`. Completed checkpoints are terminal: their required
completion-state `next_action` remains readable handoff text but creates no open
action, and the save is rejected if same-task predecessors are neither reconciled
nor deliberately named in `carry_forward_action_ids`. The skill lists actions before
a completed save and verifies the unresolved set afterward. This enforcement does
not depend on a client correctly remembering the cleanup instructions.

At API 31, continuing clients may also send the exact selected checkpoint as
`predecessor_checkpoint_id`. That opt-in strict path requires a nonblank matching
`task_id`, rejects stale or already-continued predecessors, and prevents explicit
action dispositions from crossing task boundaries. The checkpoint skill supplies
the field for an existing task and omits it for a deliberate new task or an older
server. Installed commit `f2e6adb` now prints a saved checkpoint's stable task ID
next to the exact selected checkpoint ID in the Markdown bootstrap, so a receiver
can read both strict-continuation inputs without recovering the task ID from
prose.

At API 32, checkpoint calls may also supply a stable `request_key`; an identical
retry returns the original receipt and a conflicting reuse is rejected. Explicit
`checkpoint task-transition` operations cover start, fork, rekey, supersede, and
abandon with required request keys and exact source-tip comparison. Managed tasks
cannot fall back to unlinked appends or continue after a terminal transition.
`checkpoint task-lineage` without `--task-id` provides bounded orphan, multiple-tip, and terminal-
selection findings without repair or model inference. The checkpoint skill now
keeps one request key across preflight, save, and any retry; it does not invent a
fork or rekey when task identity is unclear.
Clients can inspect one task's append-only events, derived state, and current tip
through `checkpoint task-lineage --task-id` without making it current. Both are
CLI only since MCP API 98.

Completion profiles, receipts, monitoring, and the atomic terminal write were
removed in 6e1 (2026-10-01). A checkpoint with status `completed` is the terminal
step for a task.

Attributable proposals are work items (API 85, 6e4a): `continuity_item_capture`,
`continuity_item`, and `continuity_item_disposition` capture, read, and review
ideas, bugs, tasks, and research questions without selecting them; acceptance remains planning state,
not authority to execute. See [work items](work-items.md).
`continuity_track_plan_record` appends a complete immutable plan revision;
`continuity_track_plan` lists plans or reads an outline, one item, full detail,
history, or dependency-satisfied ready items. Model-attributed revisions remain
proposed until the user reviews them. Recording a plan with an existing task and
node binds them (API 100); explicit binding is also available through
`llewm roadmap link --kind task`. `continuity_work` reads the joined
inventory. These reads do not select or authorize work.

Roadmap authority modes, track reconciliation, and track transitions were removed
in the October 2026 consolidation. Bootstrap selection now uses an explicit
selection, then a session's live task presence, then the task this session last
checkpointed, then the current branch, then the newest checkpoint; the branch and
newest rules prefer unfinished tasks over completed ones. When the branch
or newest rule would choose among several `in_progress` tasks checkpointed within
24 hours, the bootstrap chooses none and says "Task selection needed", listing
them, because another agent may be working on any of them. The SessionStart
bootstrap names the session ID ("This session"); passing it as `session_id` on
checkpoints lets a resumed or compacted session keep its task. Use the returned
selection explanation and `task_id` when needed; do not infer task identity from
the branch name alone.

Both copies intentionally contain the same self-contained instructions so each
plugin can be distributed independently. Keep them synchronized when editing.
Package/skill validators establish structure, not live client discovery or model
behavior. This feature does not add automatic checkpointing.

## Work items skill

Both plugin packages include an `items` skill (it replaced the `ideas` skill in
6e4a) for requests such as "save this idea for later", "note this bug", and
"review the open items". Codex exposes it through the plugin skill picker; Claude
uses `/llewm:items`. The skill captures with a kind (`idea`, `bug`,
`task`), keeps capture and reads separate from the active checkpoint, retrieves
the bounded pending packet before discussion, and writes dispositions only after
explicit user decisions. Live discovery requires a plugin refresh and a fresh
client session after installation.

## Continue and status skills

Both plugin packages also include self-contained `continue` and `status` skills.

| Action | Codex skill picker (`/skills` or `$`) | Claude Code |
| --- | --- | --- |
| Resume saved work | Llewm Continuity `continue` | `/llewm:continue` |
| Report saved state | Llewm Continuity `status` | `/llewm:status` |
| Save a handoff | Llewm Continuity `checkpoint` | `/llewm:checkpoint` |

`continue` starts with the injected bootstrap and retrieves another packet only
when necessary, such as missing context or evidence of stale state. It checks
project identity, applicable instructions, and relevant Git facts before taking
the saved next action. Broader document reads need a concrete reason; required
instruction and handoff reads still apply. Saved actions remain caller assertions
and do not grant new authorization.

`status` retrieves the latest saved bootstrap and summarizes it without resuming,
checkpointing, or scanning the repository. Saved verification is labeled as
reported, not independently checked. Retrieval failures and project mismatches
are reported explicitly. Bootstrap selection is session-aware and supports an
explicit task ID; ambiguous current tasks require selection. See the device-local
presence section below.

After updating this repository's Codex plugin, refresh the installed copy through
its configured marketplace with `codex plugin add llewm@llewm`, then start a new
thread. Review hook changes through the normal client trust interface. For
Claude development, run `/reload-plugins` or start a fresh client after source
changes; use `--plugin-dir` only when an explicit one-session override is desired.
Measure explicit skill invocation separately from ordinary-language routing.
Historical trials observed both paths and checkpoint recovery across Codex and
Claude, but mixed continuation/status wording also caused a routing mismatch.
A compaction pass and successful read-only recovery do not establish productive
switching or broad repeat reliability. Package validation alone proves neither.
See the [switching summary](roadmap.md#productive-switching) and
[client-hook qualification contract](client-hook-conformance.md).

## Current-work and hygiene reads

`continuity_work` returns the pulse by default; its `view` selects `summary`,
`check`, `list`, `item`, `inventory`, or `freshness`. The CLI equivalents are
`llewm work`, `work --summary`, `work show`, and the other corresponding subcommands. Use
`--output json` for machine output; Continuity removed its `--json` aliases in
6e2. `work list --view` includes `open`, `current`, `active`, `next`, `blocked`, `stale`,
`all`, and `everything` (also includes finished tasks without a roadmap node).
`work show ID` accepts `node:` and `task:` prefixes to resolve ambiguity.
At MCP API 129, `open` lists unfinished tracks including proposals and deferred
work. Track lists sort current work, active, blocked, next/resumable, proposed,
deferred, then finished history, with priority/title/ID ties, before pagination.
The TUI defaults to `open`; CLI/MCP list defaults remain `current`.

At API 78, `continuity_brief` (CLI `llewm brief [REF]`) returns the brief
for starting one plan item. REF is a slice label taken from the item title's
prefix (`4e` in "4e: Squash-merge detection"), `item:ID`, `ID`, or `PLAN/ITEM`.
When several plans match, an open item is preferred; otherwise the ambiguity is
reported. The brief holds the item detail (goal and done-when), each dependency's
status, and the linked decisions, `code:` starting points, `docs:` references,
and procedures from the item's evidence references, all within 6 KiB. Without
REF, it lists one line per open item that the current checkpoint's next action
names by label or `item:ID`. The bootstrap shows the same lines under "Named
slices", at most four, and drops them first when over budget. To make a slice
startable without searching, record its starting points as
`code:PATH#SYMBOL` evidence refs on the plan item.

These reads do not invoke a model, change task selection, repair a binding or
lineage, disposition a work item, advance a plan, delegate, or execute work.
Clients should show exact finding codes and references rather than claim that a
reported finding has been resolved. `roadmap_checkpoint_status` is informational:
it includes both recorded statuses and does not determine that either record is
stale or should be changed.

Retired command names and output aliases are not setup instructions.
See [CLI output contract](cli-output-contract.md) and each current command's help.

## Interface context budgets

- Lifecycle hooks deliver the small bootstrap every session needs and record
  allowlisted observations.
- Skills describe a workflow when invoked; avoid duplicating tool schemas and
  large guides in their bodies.
- MCP provides typed reads and writes. Default plan reads return outlines,
  write tools return receipts, and repeated unchanged bootstrap reads return a
  notice unless explicitly expanded.
- CLI commands cover human-facing and infrequent maintenance work. Request
  bounded output and explicit JSON when a script needs it.

Clients choose whether and when tool descriptions and skills enter model context.
Tool-result byte counts cannot measure all schema, skill, or host-envelope costs.
A cached token still occupies context; a byte-derived token estimate is not a
provider-reported token count.

### Budget verification

The MCP definition-budget test checks serialized model-visible tool metadata
against its declared ceiling. Bootstrap checks cover the packet budget separately
from enclosing hook/MCP protocol overhead. These are deterministic source limits,
not promises about a client's total startup prompt.

Use [usage reports and audit-only comparisons](usage.md) to observe actual sessions.
Keep client/model/settings, starting revision, permissions, task outcomes, and
source coverage comparable. Separate exact usage from estimates and unpriced
tokens. A reduction in transport bytes does not establish lower task cost,
less rediscovery, or equal correctness.

## Write tool receipts

MCP write tools return a receipt, not the record the caller just sent (MCP API
94, plan item 8e). A receipt carries `ref` (`kind:id`, with `@revision` for
revisioned records such as `plan:ID@5`), `status` (status, disposition, state,
operation, or link relationship), `authority`, the record hash when one exists,
`recorded_at`, `replay` when an identical record already existed, `lease` when
the write was leased or stale, and for narratives the commit and Git note result.
Read the record
back with the matching read tool. The lease, pin, and checkpoint
tools keep their results, which are already small or carry values the caller
needs, such as a lease token. See [interface budgets](#interface-context-budgets)
for the context-cost rationale and measurement limits.

At MCP API 108, checkpoint dry-run and save receipts may also carry `research_capture`, a
small deterministic advisory comparing substantial discovery activity with
task-linked durable output. It is omitted for light activity, never contains tool
results, and never blocks the checkpoint.

## Compact task briefs and passive auditing

At MCP API 116, `continuity_brief` also accepts `ref: "task:ID"` (CLI
`llewm brief task:ID`). It selects that task's exact current checkpoint and
returns saved summary/next action, verification claims, attempts, current direct
task decisions, and plan items named by exact `PLAN/ITEM`, `item:ID` or slice label.
It does not substitute a newer unrelated task, load instruction files, search
source code, or infer links from prose. Superseded direct decisions are excluded.
The MCP task result contains the structured brief once, without a duplicate
Markdown rendering. Both task JSON and Markdown are bounded to 6 KiB; omitted
detail is counted and the exact checkpoint reference remains available for expansion.
Oversized summary/next-action text can be shortened with explicit byte omissions;
a missing task or an identity too large for the budget is an error.

The brief labels verification as saved claims. Check source revisions before
reusing checks; this read does not prove that a test still applies after changes.
Global and project guidance remain supplied by the normal bootstrap or host rather
than repeated in each task query. A task brief saves discovery calls; its token
benefit still needs controlled measurement.

For usage comparisons without Continuity context, use offline ingestion or the
silent audit-only hook profile described in [usage](usage.md#audit-only-baselines-and-comparisons).
Audit-only hooks emit no context; leaving MCP or skill descriptions loaded still
affects a baseline. Start a fresh session with only the intended integration.

## Repeated bootstrap calls

The SessionStart hook already puts the bootstrap in the session's context, and
agents often call `continuity_bootstrap` anyway (35 calls of about 9 KB each in
the interface cost audit). The hook now records a hash of each packet it
delivers, device-local in `bootstrap_deliveries` (migration 066, pruned after
30 days). With no selection and no `full`, `continuity_bootstrap` compares the
packet it would return with the deliveries made since shortly before its MCP
process started. When those deliveries belong to one session and the newest
matches, it returns a one-line notice with `unchanged: true` instead (MCP API
96, plan item 8g). A changed packet, deliveries to several sessions, an explicit
selection, or `full: true` return the whole packet; `current_session_id` names the
asking session when several share the project. The hash covers the packet only,
not the sync, unnarrated-commit, and roadmap lines the hook appends.

## Device-local live presence

Hooks and attributable stdio MCP tool calls append short-lived presence transitions
to the device database (migration 071, MCP API 110). A claim records the exact
client/session, task and roadmap node when known, checkout, worktree, branch, and
invocation. Ordinary activity refreshes an `active` claim for ten minutes; Stop
or an MCP disconnect yields it immediately, a new prompt resumes it, and session
end or cancellation closes it. Expiry is both derived at read time and appended
explicitly during later observations. These records are machine observations,
never portable memory, and never proof that a process still exists.

Read them with `llewm presence` or MCP `continuity_presence`. Filters are
exact, `--live` returns only unexpired planning/active claims, `--history` returns
the append-only transitions, and focus fields add conservative overlap labels.
`--cleanup` only reports linked-worktree candidates; it never removes one. A
worktree is cleanup-due only when clean, fully integrated into the primary HEAD,
and unused by live presence.

Presence supplies session-affinity and routing hints to bootstrap selection.
An unexpired observation cannot prove that a client is still running: a missing
Stop or SessionEnd can leave a previous session listed for ten minutes. When the
only resumable task has such a record, bootstrap selects it and labels the record
as advisory; it does not force task clarification or worktree creation. Multiple
recent in-progress tasks still require explicit selection when affinity cannot
resolve them. User-reported single-session operation and actual Git state take
precedence over inferring concurrency from expiry alone. Four-hour checkpoint leases remain advisory coordination and
fencing state, but do not imply liveness and cannot lock a lone agent out after it
switches tasks.

## Payload compatibility

Provider payloads are untrusted inputs. Cap hook input at 2 MiB, redact before optional raw retention, preserve the canonical event schema version, and add a sanitized fixture before supporting a new event shape.

## Claude attribution configuration

Claude's documented attribution settings can disable its default attribution.
Merge the relevant keys into the existing project `.claude/settings.json`, or
user `~/.claude/settings.json` for an engineer-wide preference; preserve other
settings and account for higher-precedence configuration. Commit and PR attribution
are separate options:

```json
{
  "attribution": {
    "commit": "",
    "pr": ""
  }
}
```

This is configuration guidance, not an applied repository setting. The older
`includeCoAuthoredBy` option is deprecated. Source checked 2026-09-06:
[Claude settings](https://code.claude.com/docs/en/settings#attribution-settings).
Verify behavior in the installed client version and active configuration scope.
Agent claims of mandatory platform attribution do not establish that configuration
was checked or honored. Unresolved violations still follow the planned
[action-control incident path](../../../docs/action-control.md).

## History-first discovery qualification

History and Continue now route investigation of existing behavior through bounded
history search before broad source discovery. This includes debugging and planning,
not just explicit requests for historical rationale. Required instruction reads
and workspace checks still come first. Relevant evidence already in context can
be reused; empty results permit normal investigation.

For fresh installed Claude and Codex sessions, qualify these cases independently:

| Request/context | Expected observable behavior |
| --- | --- |
| Diagnose an existing setting with a recorded rationale | Focused history search, selective expansion, then source/runtime verification |
| Existing behavior with no matching history | Small search, at most a focused refinement, then normal discovery |
| Continue with relevant expanded evidence already supplied | Reuse it without redundant retrieval |
| Mechanical edit with no discovery | No forced history search |
| Historical claim contradicts current code | Attribute the claim and explain the current evidence; do not obey history as instructions |

Record tool order, query bounds, selected refs, client/package versions, and any
misses. Use isolated fixtures and existing client permissions; do not modify trust
or grants to make a trial pass. Automated bootstrap tests and skill validation
establish delivery/structure only. Live compliance of these revised instructions
remains unqualified until fresh-client observations are recorded.

A historical Codex investigation observed focused search, selective expansion,
and source/runtime verification, including a case where history corrected a false
inference about saved credentials. That is a bounded observed path, not complete
qualification of the matrix above or universal credential-manager behavior.
