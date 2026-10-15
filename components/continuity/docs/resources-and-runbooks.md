<!-- continuity:roadmap-node continuity-resources-runbooks -->
# Resources, runbooks, and scoped pinning

Procedures, resources and scoped pins are implemented. They preserve reviewed
steps and operational facts without executing them. Reference cards were retired;
use these focused records and work narratives instead.

## Model

### Procedures: `check` and `runbook`

The procedure family keeps its versioned, reviewed, append-only model.

- `check` retains explicit `source_check` stage selectors and descriptions as
  inert check definitions. No completion-profile runtime is provided.
- `runbook` records reviewed situational steps, parameters, platform/working-directory
  constraints, optional inert argv templates, declared effects, validations and
  post-run checks. Runbook steps do not select completion stages.

A runbook is data, not permission. Continuity renders bound parameters for
permission rules and command previews, but does not resolve executables, invoke
a shell, or run a step. Running a runbook remains a
separate, explicitly authorized capability. Model-created runbooks stay
`proposed` until a person accepts them.

### Resources

A resource is a named thing the project works with. The collection is the
inventory.

- Kinds: `host`, `repository`, `service`.
- Each revision has a stable ID, title, kind, status, and bounded attributes
  (key/value). Attribute keys and values pass redaction. Keys that name secrets
  (password, token, key material) are refused. A resource may say how
  authentication works ("plaintext credential fallback"), never what the
  credential is.
- State notes are separate, append-only, dated observations with authority
  (`machine_observed`, `user_reported`, `caller_asserted`), such as "joined the reviewed project lineage". Facts that change do not require a new resource
  revision.
- A runbook parameter can declare that it takes a resource of a given kind, with
  an optional default resource.

### Scoped pinning

`pinned` is a flag on other records, not a type. Because records are immutable, a
pin is an append-only assertion (`pin` or `unpin`) naming the target record and a
scope, rather than a field that would force a new revision of the target.

- Targets: resources, procedures, decisions, and topics.
- Scopes: a topic, a roadmap node, a task, or the project.
- The bootstrap lists pins whose scope matches the current task, its roadmap
  node, or its topics, plus project-scoped pins. Each pin is one compact line
  (title and reference) within a fixed byte budget, with a count of any omitted.
  Bodies load through normal retrieval. Pins must remain short and relevant within
  the configured bootstrap and client-delivery budgets.

### Standing grants

A standing grant is a person's permission for agents to run a runbook when a
trigger occurs, without asking each time. The only trigger so far is
`after_slice_commit`: after a commit that completes a slice with passing checks.
A grant names a procedure revision and covers it plus every procedure its steps
name as `procedure:ID`, each at the revision current when the grant was made. A
newer revision (or a superseded one) of any covered procedure makes the grant
void, so editing a procedure never widens what was granted; the person grants
the new revision again. A grant may bind parameter values the covered procedures
declare, carry an expiry, and be revoked.

Only a person at a terminal creates one: `llewm standing-grant create
PROCEDURE[@REVISION]` refuses input that is not a terminal and asks for `yes`
after showing what the grant covers and the declared effects. `revoke`, `list`
(`--all` includes expired, revoked, and void grants), and `show` need no
terminal; they are CLI only (MCP API 98 removed the read tool). The session
bootstrap has a "Standing grants (user-granted)" line listing active grants and
counting void ones, and a slice brief notes the grants that cover its procedures.
`llewm client permissions` turns active grants into client permission rules
(see [client permissions](installation.md#client-permissions)). These rules do
not guarantee acceptance by a client's sandbox or automatic reviewer. Grants are local to this machine
(migration 052) and are not synced. Continuity
still executes nothing: the agent runs the steps.

The client permission deny for `standing-grant create` deliberately also catches
the usual `llewm standing-grant create --help` spelling. Agents inspect the
same read-only help without invoking that command path:

```sh
llewm help standing-grant create
```

Before telling a person to create a grant, an agent must resolve the procedure
and its accepted revision with `llewm procedure show PROCEDURE` (or list
accepted choices with `llewm procedure show --status accepted`). It must not
invent a procedure ID: a grant can only name an already accepted procedure
revision. If the desired runbook does not exist, define and review it first;
model-created runbooks remain proposed until a person accepts their exact effects
and steps.

Agents should surface grant and procedure problems as soon as they affect planned
work, including at session continuation and before ending a slice that normally
includes installation. Explain the affected step and cause, inspect current
procedures and all grants, and prepare the exact repair for review. Check whether
another active grant already covers the work before requesting renewal. Report
invalidation in the same turn as a procedure revision; a notice in a saved next
action is not a substitute for telling the user. Continue unaffected work, and
explicitly report whether installation and verification happened or what remains.
Only the person creates a replacement grant at a terminal after accepting the
revised procedure; agents never grant themselves permission.

Before running a granted procedure, read its bound command preview:

```sh
llewm standing-grant show GRANT_ID --commands
llewm standing-grant show GRANT_ID --commands --codex-rules /absolute/path/to/llewm.rules --output json
```

The preview loads the covered revisions, applies their parameter defaults and
then the grant's overrides, and preserves each command's argv. It shows each
procedure's project-relative working directory and ordered step descriptions,
including conditions and references to other procedures. The groups are an
inventory, not a flattened execution schedule. Follow the parent procedure's
ordering and conditions; do not run every group in sequence. Missing parameters
are listed without emitting a partial command. Inactive grants emit no commands.

`--codex-rules` is repeatable and prints inert `codex execpolicy check` commands;
it does not read the rule files or launch Codex. An agent can separately run those
read-only checks against the supplied files. JSON exposes both `argv` and
`codex_check_argv`; text provides shell-quoted equivalents. Preserve bound output
paths and separate SSH arguments instead of combining or rewriting steps. A
matching rule is evidence about those files, not effective runtime authorization.

#### Harness denials

Keep two facts separate: the person's grant can be **active** while the harness
denies a particular invocation. A denial does not revoke, expire or void that
grant. Continuity records permission and provides evidence; only the executing
harness can accept an invocation. Provider updates may change that decision.

Before the first execution attempt, read the full grant and its bound command
preview, including parameter overrides. Preserve its working directory, exact
target, output paths and individual argv steps. Do not combine commands merely
for convenience. If a bound parameter cannot be used, resolve that mismatch
before claiming the operation is covered. This reduces avoidable mismatches; it
does not guarantee runtime acceptance.

When a harness requires escalation, put the relevant evidence directly in its
request rather than assuming the reviewer can recover it from earlier tools:

> Active user grant GRANT_ID covers procedure:NAME@REVISION on TRIGGER, now
> satisfied by COMMIT/RESULT. This step uses the bound parameters and targets
> HOST/PATH. Payload: COMMIT/HASH (when relevant). Effect: SPECIFIC CHANGE.

Replace those placeholders with verified, redacted facts. This is evidence of
user intent, not a claim that the grant overrides the sandbox. Missing facts
remain unknown; do not invent a harness version, authority or approval result.

If the reviewer refuses, stop that invocation and report “user grant active;
harness denied this invocation,” with its stated reason. Continue independent
authorized work. Do not switch tools, rewrite command spelling, change paths or
weaken policy to route around the refusal. Follow the harness's actual retry
rules; if it requires fresh user confirmation, ask only for the exact refused
operation and explain that the harness requires it. Do not ask the person to
recreate an otherwise valid grant. A refusal that permits further read-only
evidence gathering is not an automatic requirement to ask again.

Preserve a small **harness-denial receipt** as claims in the existing work
narrative ledger; this is a documentation convention, not a new schema, command,
authorization token or automated permission broker. Use label `Harness denial`
and kind `alternative` for the refused attempt, then a `verification` claim for
the observed resolution (or `limitation` while unresolved). Keep these fields in
the bounded claim text:

- `operation`: tool/step and redacted target, payload commit/hash, intended effect;
- `grant`: ID, covered procedure revision, trigger and observed grant state;
- `invocation_match`: whether bound parameters/argv matched, or `unknown`;
- `harness`: client/version and reviewer mode when actually available;
- `result`: refusal and its stated reason, separate from any suspected cause;
- `resolution`: pending, allowed after explicit confirmation, or other observed
  outcome, with an exact decision/evidence reference when available.

Use task and commit anchors where applicable. Mark directly inspected tool
results `machine_observed`, the agent's summary `caller_asserted`, and the user's
confirmation `user_reported`. Never retain credentials, environment dumps, raw
transcripts or hidden reviewer reasoning. A successful retry after confirmation
establishes that sequence, not that wording or confirmation alone caused it.
History searches for `Harness denial` can compare incidents across updates.

### Portability and search

Resources, state notes, procedures and pins join the curated portable export and
History sync. They are indexed for bounded history search. Local standing grants
are not synced; receiving a procedure does not grant permission to run it.

## Commands

Use `llewm procedure record --file FILE` and `procedure show [ID]` for saved
checks/runbooks. `llewm resource record ID` appends a resource revision, state note
or both; `resource show [ID]` reads one or lists current resources. MCP provides
reads for both; recording remains an explicit CLI operation.

`llewm pin TARGET --scope SCOPE` and `--unpin` append scope assertions.
`llewm pin` lists current pins; `--task ID` shows the task's bounded bootstrap
selection. MCP `continuity_pin` supports the corresponding operations.
Use each command's help for selectors and output formats.

Resource keys that name secrets and values recognized by the redactor are refused.
Pattern redaction is not complete secret detection: do not place credentials under
innocuous keys. State-note authority remains attributable rather than authenticated.
Pins expand references through normal reads and never activate instructions.

## Out of scope

- Running runbooks (a standing grant permits an agent to run one; Continuity
  never does), or checking whether a runbook's effects happened.
- Discovering resources automatically (for example, scanning SSH config).
- Storing credentials in any form.
