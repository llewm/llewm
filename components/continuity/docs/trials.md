# Try Continuity, adopt it, or return to prose

Try the full local Llewm workflow while retaining your original docs.
Adopt when you choose ongoing use.
You can leave either path and continue without Llewm.

A trial records a starting snapshot and enables normal local hooks, knowledge
recording and fresh-session recovery. Guided onboarding [populates tracks and
plans from current prose](prose-import.md) so the TUI shows your project immediately.
The `trial start` command creates the baseline/store; the onboarding agent performs
the semantic import. History sync stays disabled during trial. Adoption records
ongoing use; it does not unlock local recovery or automatically replace project
docs. Keep useful standalone instructions in both paths.

To stop using Llewm, disable its client integrations, stop its processes, remove
instructions that require Llewm, and start a fresh session. If useful knowledge
has accumulated in Llewm, first reconcile it into project docs using the
[handback workflow below](#prepare-a-handback). The same workflow supports both
trial and adopted trial projects; existing setups without a trial receipt use a
manual review. See [removal and retained data](installation.md#remove-or-disable-an-installation).

A trial is not an execution sandbox or an automatic rollback of agent edits.
Keep ordinary Git and database backups. Trial/exit never deletes project
knowledge, rewrites project prose, uninstalls shared plugins, or changes client
approvals.

## Disposable demo

For a first look, create an empty Git repository in a disposable directory. Add
a `.gitignore` containing `/.llewm/` (see the
[Git exclusion prerequisite](project-layout.md#keep-local-state-out-of-git)), an `AGENTS.md` describing a small fictional task and a `PLAN.md`, then run:

```sh
llewm trial start --prose PLAN.md
llewm trial status
llewm checkpoint create --task-id demo --task 'Try a handoff' \
  --summary 'This is disposable example knowledge' --next 'Review the handback'
llewm trial prepare --out review
```

Read `review/review.md`. You can try the CLI without installing any client plugin,
setting permissions, or connecting a remote. A fresh process can read saved
records with `checkpoint show`, history reads and `bootstrap`. Use `llewm --help` for exact read
commands. Only the demo directory and normal local project registry are affected.

## Real-project trial

Before creating trial state or changing client setup, complete the
[Git exclusion prerequisite](project-layout.md#keep-local-state-out-of-git), then run:

```sh
llewm trial start --prose docs/PLAN.md,docs/DECISIONS.md --review-after 336h
llewm trial status
```

Start initializes a local store and portable identity without a remote. Existing
state is preserved. A project already configured for History sync cannot enter a
trial: use a separate demo, or perform a separately reviewed existing-project
handback. Start requires a positive review period. Expiry only sets `review_due`;
it never adopts, disconnects, deletes, or schedules anything.

The receipt baselines root guidance, README, common plan paths, `.gitignore` and
common project client settings plus the files supplied with `--prose`. **List all
other instruction, plan and decision files you depend on.** Missing baseline
files are recorded too, so later creation is visible. Files must be regular,
project-relative, outside Git/tool state, at most 1 MiB each, without symlink
traversal. The complete redacted receipt is limited to 1 MiB. Patterns redact
before persistence; redacted text is evidence, never an exact restore source.
Keep original prose in Git. Inspect sensitive content before supplying it.

During a trial:

- Original prose remains available for review and handback. Root instructions
  retain authority. Do not delete useful docs or require duplicate maintenance
  of every operational plan; review what the DB will maintain.
- With mode `full`, normal hooks observe activity, deliver the saved task at
  SessionStart, surface checkpoint reminders and run configured roadmap scans.
  Checkpoints and explicit CLI/MCP bootstrap recover the same saved state.
- The onboarding agent imports current tracks/plans with source evidence and
  existing plan-review policy. Explicit CLI/MCP reads and writes remain available.
- Mode `audit-only` still selects passive hooks and refuses new MCP startup.
  Use the separate [baseline path](usage.md#audit-only-baselines-and-comparisons)
  for a comparison without plugin/skill context.
- History sync setup and execution are refused. This is independent of plugin,
  reporting or external tool permissions; do not enable optional reporting as
  part of the trial.
- Plugin skills are still installed client instructions. A baseline comparison
  without their influence requires disabling those skills/plugin/MCP as well.

Preview client installation before applying it. Save its receipt and record the
previous plugin identity, permissions, hook trust, MCP registrations, global
guidance and launch/PATH configuration. These can be shared across projects;
the trial does not own them or promise automatic global rollback. Project-local
settings in the baseline can be compared, but are never blindly restored.

## Adopt explicitly

```sh
llewm trial adopt --reviewed
```

Adopt after reviewing the trial and choosing ongoing use. Local hooks and task
recovery stay enabled; repository instruction authority remains unchanged. It does not
remove prose, enable sync, enable reporting, or alter approvals. Restart clients
and qualify exact checkpoint recovery in a fresh session. Keep prose sufficient
for recovery while deciding what ongoing responsibilities belong in Continuity.
The baseline remains available after adoption.

Existing installations without a trial receipt retain their behavior. Their
presence is not retroactive evidence of a reviewed adoption. To hand them back,
follow the same review/verification principles manually; do not invent an old
baseline or delete their store to manufacture a trial.

## Prepare a handback

From a trial or an adopted trial project:

```sh
llewm trial prepare --out docs/handback-review-1
```

Output must be new; previous evidence is never overwritten. The review contains
original/current redacted prose with hashes, all curated record revisions with
identity/authority/timestamps, missing references, and an explicit omission list.
It includes learning accumulated during the trial. It excludes raw events,
per-request usage, transcripts, credentials, global config bodies and unresolved external
source bodies. The SQLite store and backups remain the recovery source for those
local observations. Historical revisions are evidence, not all current truth.

Reconcile this evidence into standalone prose such as `docs/HANDBACK.md` and the
project's real instruction/plan files. Explain current work, next actions, plans,
decisions, unresolved issues and relevant procedures. Resolve changed instructions,
concurrent edits, missing references and contradictory records explicitly. Do not
restore the starting files over newer work. Remove stale “resume from Continuity”
directions where appropriate. Generated review evidence is not a substitute for
this reviewed prose.

After prose edits, prepare a new review so the snapshot covers their final state.
Inspect client setup changes against the saved installation receipt. Disable only
integrations you own. A shared plugin may be needed by other projects; use a
separate client configuration/profile for the prose-only verification when needed.
Never remove an entire client config or permissions directory.

Start a fresh client session with Continuity plugin/MCP/hooks/skills disabled and
load the standalone project prose. Verify it identifies the current objective,
next action and required constraints without querying Continuity. Record what was
actually observed, client identity and any remaining gaps. No model is required
by the implementation; semantic completeness is a reviewed assertion, not a
property the renderer can prove.

## Disconnect and retain recovery

```sh
llewm trial exit --review docs/handback-review-2 \
  --handback docs/HANDBACK.md --reviewed --writers-stopped \
  --verification 'Fresh CLIENT session with integrations disabled loaded the prose and recovered OBJECTIVE/NEXT/CONSTRAINTS'
```

Drain all project CLI, hook, MCP and sync writers before the final snapshot check.
`--writers-stopped` is an explicit attestation, not process discovery. In-flight
operations must finish before exit. Already-open compatible idle MCP servers
refuse later calls; old binaries must be stopped.

The handback must be a separate nonempty prose file outside the generated review.
Exit verifies review hashes and refuses if baselined prose, curated knowledge,
identity or phase changed since preparation. This detects stale reviews; it does
not prove the human's semantic review or fresh-session report.

Exit records the handback path/hash and attributed verification. Project hooks
become inert; new writable CLI/MCP opens and calls from this build's already-open
MCP servers are refused. Read-only `trial prepare` evidence export and `trial status` remain
available. Stop old processes and restart clients: older binaries do not know this
receipt. There is no automatic reactivation or purge command. Retain the store,
review evidence, original Git history and verified backups until a separately
reviewed recovery/adoption or deletion decision. Shared/global configuration and
remote History-sync repositories remain untouched.

Adoption transitions are device-local, serialized with a short-lived exclusive
receipt lock and preserve prior transitions. A crash can leave `.adoption.lock`;
verify no transition is running before removing that lock. The receipt is not an
OS security boundary against someone editing state or using an older executable.
