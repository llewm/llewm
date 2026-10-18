# Guided onboarding

Start here to set up Llewm for one Git project. You can follow the steps yourself
or ask your coding agent to guide them. The agent checks prerequisites, prepares
specific changes, and verifies results. Llewm remains a local Go executable with
no required model or installation service.

Before the plugin is installed, paste this request into your coding agent while
working in the Git project you want to adopt. No Llewm checkout, executable,
plugin, or Go installation is assumed:

```text
Set up Llewm Continuity for the Git project open in this workspace and the
coding-agent client running this session.
My starting path is: [usage-audit baseline / full local trial / full adoption].
If I have not chosen one, explain these three choices briefly and ask before
configuring the client. Do not treat usage auditing as permission for adoption.

Canonical source: https://github.com/llewm/llewm
Start with this onboarding guide:
https://github.com/llewm/llewm/blob/main/components/continuity/docs/onboarding.md
Plain-text copy if needed:
https://raw.githubusercontent.com/llewm/llewm/main/components/continuity/docs/onboarding.md

Read that guide's setup steps, not the rest of the repository. Follow its
installation.md and client-integrations.md links only for the chosen OS,
installation method, and client; read other linked sections only when a step
requires them. Resolve relative links within components/continuity/docs/ at the
same source revision. Do not search for the product, scan unrelated projects,
read all documentation, or reconstruct this project's history. If these URLs
are inaccessible, report the exact failure and ask for the guide or a source
checkout instead of guessing what Llewm is.

Confirm the target Git root, client, OS/architecture, and whether the client runs
natively, in WSL, in a container, or on a remote host. Inspect only the relevant
existing configuration and prerequisites. Ask only for facts you cannot observe.
Assume Llewm, its plugin, and Go may all be absent. For a source build, use the
canonical repository above, build from components/continuity/, and obtain the
required Go version from that revision's go.mod; use https://go.dev/doc/install
if Go is missing. Keep the Llewm source checkout separate from the target project.

Before creating project state, follow the guide's Git exclusion prerequisite.
Verify that the whole .llewm/ directory is ignored and has no tracked files.
Default to /.llewm/ in the root .gitignore; use the local Git exclude file for
personal-only setup. Preserve existing rules, show the change, and do not
automatically untrack or delete existing state.

Follow only the guide's chosen path. For usage auditing, install the executable
and collect a baseline without the full plugin, MCP, skills, or an adoption
checkpoint. For both trial and adoption, import selected project prose into
source-linked tracks/plans, review the interpretation, show the populated TUI,
and verify a first checkpoint that a fresh session can recover. Preserve
original trial docs and defer ongoing adoption. Preserve existing configuration
and history. Show me the exact permission changes and the commands
the guide requires me to run myself. Keep optional reporting and History sync
disabled. Distinguish prepared changes, successful shell checks, and behavior
actually observed in the running client; leave fresh-session recovery pending
until it is observed when that check belongs to the chosen path.
```

The URL identifies the product and bounds the reading; local prerequisite and
configuration checks are still necessary. A full documentation or source audit
is not part of onboarding. For a pinned build, replace `main` in both guide URLs
with its commit or tag and use that same revision for the source and linked docs.

Once installed, the plugin's `onboard` skill supports the same process. In Codex,
invoke `$onboard`; in Claude Code, invoke `/llewm:onboard`, or ask to onboard this
project in ordinary language. Fresh-client skill routing still needs qualification.

## Choose a starting path

- **Usage-audit baseline:** measure ordinary agent usage before introducing
  Continuity instructions or tools. Follow [audit-first setup](#start-with-usage-auditing).
- **Full local trial:** evaluate populated tracks/plans, normal observation and fresh-session
  recovery while retaining original docs for review and handback. Follow [trials](trials.md) before changing client setup.
- **Full adoption:** choose ongoing use of the same local workflow with
  the [setup steps below](#setup-and-verification).

You can stop using Llewm after either trial or adoption. Adoption does not
automatically reorganize project docs. Disable the integrations and start a
fresh session; preserve any knowledge you still need in ordinary project docs.
See [leaving Llewm](installation.md#remove-or-disable-an-installation).

### Start with usage auditing

1. Install only the executable using the OS-specific [installation instructions](installation.md).
   A source build needs Git and the Go version declared in `go.mod`; an audit
   baseline does not require the client plugin or MCP server.
2. First complete the [Git exclusion prerequisite](project-layout.md#keep-local-state-out-of-git).
   From the target Git root, run `llewm init`, then `llewm mode audit-only`.
   Preserve existing state. This mode does not create
   a trial receipt or record adoption.
3. For the cleanest baseline, leave all Llewm client integrations disabled and
   run ordinary work in a fresh session. Afterwards, run `llewm usage ingest`
   at a terminal and inspect `llewm usage report`. Optional hook-only collection
   and comparison commands are in [usage baselines](usage.md#audit-only-baselines-and-comparisons).
4. Record the session IDs, task, starting revision, client/model/settings,
   coverage warnings and correctness checks for later comparison. A Continuity
   checkpoint or MCP recovery check is not required for this path.

To proceed later, retain the store and usage observations. Start a trial using
the instructions below, or deliberately choose full adoption. When enabling the
full client, remove any manually registered audit-only hooks, run `llewm mode full`,
then follow client setup and restart verification. A trial supports the same local task
recovery with mode `full`; optional History sync remains disabled during trial.

Already onboarded another way? Follow [switching an existing setup to audit-only](usage.md#switch-an-existing-setup-to-audit-only).
This also covers returning to full operation; changing mode preserves history
and does not change trial/adoption state.

### Choose a trial before adoption

Start with a [disposable demo or full local project trial](trials.md). For a real
trial run `llewm trial start --prose PATHS` before changing client setup. Original
prose is retained, full local task recovery is available, and History sync is disabled.
At the review point, choose `trial adopt --reviewed` or the reviewed prose-handback
workflow. A review reminder never automatically adopts or deletes anything.
The setup steps below also verify a trial; ongoing adoption still requires an
explicit choice. Neither path requires maintaining duplicate operational plans.

## Setup and verification

1. **Choose the project and environment.** Use the Git root of the project to
   adopt, not automatically the Llewm source checkout. Install separately inside
   WSL, remote development hosts, and containers that run the client.
2. **Install the executable.** Follow [installation](installation.md) for source
   builds, persistent PATH, update and removal. [Preview bundles](preview-distribution.md)
   include a matching plugin package. Public remote installation is available
   only after the reviewed source revision is published.
3. **Protect and initialize local state.** Before creating state, complete the
   [Git exclusion prerequisite](project-layout.md#keep-local-state-out-of-git):
   ensure the entire `.llewm/` directory is ignored and no state is tracked.
   Default to `/.llewm/` in the root `.gitignore`; use Git's local exclude file
   for personal-only adoption. Preserve existing rules and show the diff.
   Review already-tracked state separately; never automatically untrack or delete it.
   From the project run `llewm init`, `llewm doctor`,
   `llewm database status`, and `llewm version --output json`. Init preserves an
   existing store. Pending migrations need the separate backup-gated workflow.
   `doctor` expects root `AGENTS.md`; root `CLAUDE.md` is optional and its absence
   does not fail the check. Keep applicable component guidance referenced from
   the root instructions.
4. **Enable the client.** Preview `llewm install --client codex --dry-run` (or
   `--client claude-code`), then run `llewm install --client CLIENT --permissions`
   yourself at a terminal. The binary carries its matching plugin package; no
   separate source checkout is needed. See [client integrations](client-integrations.md).
   Plugin installation, hook trust, tool approvals, and filesystem/network
   sandbox permissions are separate. Preview generated permission rules; apply
   them yourself at a terminal as described in [client permissions](installation.md#client-permissions).
5. **Verify the running client.** Restart the parent IDE and client when required.
   Compare MCP `continuity_info` with the installed version. A successful shell
   invocation or doctor check does not prove that hooks ran in the client.
6. **Populate and review the project model.** Follow [prose import](prose-import.md)
   for trial and adoption: the onboarding agent reads a bounded set of current
   authoritative docs once, imports source-linked tracks and plans, and shows
   them in `llewm tui`. Interpretations stay proposed unless review/delegation
   permits otherwise. Review uncertainties and the intended current work once;
   do not manufacture approvals or reconstruct all historical commits.
7. **Save a first checkpoint.** Name the current objective, authoritative
   project instructions, observed checks, and remaining limits. Use the checkpoint
   skill's dry-run/save/readback flow. Adopt existing history progressively; do
   not manufacture knowledge from every old commit. Retain an existing lineage.
8. **Recover in a fresh session.** Ask to continue. Confirm the exact checkpoint,
   objective and next action, plus the observed SessionStart event. Record the
   client/plugin versions and OS/architecture that passed.

The result should distinguish what was prepared, installed, and observed in a
live client. If the agent cannot observe a fresh session, that final check remains
pending. Expiring presence from a previous session is advisory, not proof of a
second writer.

## See the current operating state

Run `llewm doctor` for hook mode, recorded trial/adoption state, effective hooks
and session-recovery behavior, alongside normal health checks. Use
`llewm doctor --output json` for project state and readiness as JSON.
`not_recorded` means no trial/adoption receipt; it does not prove reviewed adoption.
A disposable demo has the same state as any other trial. Audit mode and adoption
state are independent: an adopted project may temporarily collect only usage.
Live client activation is reported as `not_verified`; verify the actual client
separately. Doctor reads without registering a project or opening a writable
store and can report state after exit or before initialization (missing health
prerequisites still produce a diagnostic error).

## Optional capabilities

Personal [global guidance](global-guidance.md), [usage ingestion](usage.md),
[History sync](memory-repository.md), and the
[private reporting adapter](../../reporting/README.md) are separate choices.
Ordinary onboarding does not activate them. Review each capability's data,
destination and authorization before enabling it.

## Native qualification receipt

For Windows and macOS, record the exact source commit, clean/dirty flag, binary
and plugin versions, OS/architecture, terminal and client versions. Verify PATH
from the actual client launcher, hook trust and delivery, permissions, checkpoint
save/readback, fresh-session recovery, and interactive console navigation/back/
resize/exit. Then verify update and removal without deleting the project's store.
Use the [console checklist](console.md) for the interactive cases. A Linux check
or successful cross-compilation does not qualify a native client.
