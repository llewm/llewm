# Guided onboarding

Start here to install or update Llewm, then optionally configure a project.
Installation does not require a Git project or an adoption decision. Llewm is a
local Go executable with no required model or installation service.

Paste this request into your coding agent from any folder. No Llewm checkout,
executable, plugin, or Go installation is assumed:

```text
Help me install or update Llewm in the environment running this session.
Follow https://github.com/llewm/llewm/blob/main/components/continuity/docs/onboarding.md
Read its agent workflow first and follow only the links needed for my setup.
Complete installation/update separately from project configuration. If I want
project setup, identify the project topology and explain the supported settings;
do not assume this workspace folder is a Git repository. Preserve existing
configuration and history, and report installed versus live-verified results.
```

## Agent workflow

The canonical source is https://github.com/llewm/llewm. If the guide cannot be
read through GitHub, use its [plain-text copy](https://raw.githubusercontent.com/llewm/llewm/main/components/continuity/docs/onboarding.md).
If both fail, report the exact failure and request the guide or an identified
source checkout; do not guess. For a pinned build, replace `main` in the guide
URL with its commit or tag. Read linked docs from that same revision, resolving
relative links within `components/continuity/docs/` unless the link says otherwise.
Read only the relevant OS, installation method, client and selected project path;
do not search for the product, scan unrelated projects, read the whole repository,
or reconstruct project history.

### 1. Install or update independently

Observe the client, OS/architecture, execution environment (native, WSL, container
or remote host), existing executable/PATH and relevant installation receipts.
Ask only for facts or choices that cannot be observed. Install in the environment
that actually runs the client; keep the Llewm source checkout separate from any
project being configured.

Follow [installation](installation.md) for a new executable and
[updates](updates.md) for an existing installation. Reuse its actual method,
location, pinned source/channel and plugin owner. The documented user directories
are examples, not required locations. A source build needs Git and the Go version
in the selected revision's `components/continuity/go.mod`; obtain missing Go from
https://go.dev/doc/install. Use an identified preview bundle when available and
appropriate; do not invent a public release, package-manager channel or signature.
An existing package-manager-owned installation must be updated through its owner.

Verify `llewm --help`, `llewm version --output json` and PATH from the client
launcher. Installation/update alone must not initialize project state, create a
trial, select adoption, import prose, enable History sync or require a project
checkpoint. For an update affecting existing stores or active clients, retain the
backup, compatibility, migration and restart checks in the update guide; absence
of a project in the current folder does not prove no stores are affected.

The executable and client integration are separate. If explicitly requested,
preview `llewm install --client CLIENT --dry-run` and have the user run
`llewm install --client CLIENT` at a terminal. User-scoped plugin registration
can happen without a project; omit `--permissions` until a project is initialized.
An audit baseline leaves full plugin/MCP/skills disabled. Preserve unrelated
settings and distinguish plugin registration, hook trust, permissions and live
activation. Stop here for installation/update-only requests.

### 2. Select project scope and settings

When project configuration is requested, follow [project selection](#choose-the-project-topology)
before choosing the [starting path](#choose-a-starting-path). If no project is
available yet, finish installation and leave project configuration pending;
do not run `git init`, choose the Llewm source checkout, or create a dummy project.
A disposable trial demo is a separate explicit choice.

Explain the applicable settings together: repository/workspace membership and
store ownership; join existing history versus new local history; audit baseline,
trial or adoption; Codex and/or Claude Code integration; shared versus personal
Git exclusions; and optional global guidance, usage ingestion and History sync.
Use existing settings when rerunning setup. Optional capabilities remain disabled
unless requested. Show exact permission changes and terminal-only commands.
Follow the chosen path below; an audit baseline does not authorize adoption.
For trial/adoption, import selected source-linked prose, review the interpretation,
show the populated TUI and verify exact checkpoint recovery in a fresh session.
Keep original trial docs and preserve all existing history. Report prepared,
installed and observed results separately; unobserved fresh-client checks remain
pending.

## Choose the project topology

An editor workspace and a Llewm project are different scopes. Inspect the selected
folder and explicitly identified member paths, not unrelated directories. Do not
infer a shared project from sibling repositories or an editor workspace file.

| Layout | Setup and store ownership |
| --- | --- |
| One Git repository, including a monorepo | Initialize its resolved project root; components do not need separate stores. |
| Linked worktree or submodule | Normally uses the main checkout/superproject store; an existing local project footprint establishes independent scope. Inspect before initializing. |
| Folder holding several Git repositories, no Git repository at the folder root | Choose one member, a unified workspace (one shared store), or a federated workspace (independent member projects/stores). No `git init` at the folder root is needed. |
| Empty folder or ordinary non-Git folder without member repositories | Installation/update only until a supported project is selected. An arbitrary non-Git folder is not a standalone Continuity project. |

For multiple repositories, read [repository topology and workspace setup](../../../docs/multi-repo-and-sync.md#repository-topology).
From the containing folder, preview `llewm workspace init --mode unified` or
`--mode federated`; inspect membership and existing-store warnings before repeating
with `--write`. The CLI defaults to unified, but the onboarding agent must establish
the intended ownership instead of silently accepting that default. Automatic
proposal discovers only immediate, non-hidden child Git checkouts, not recursive
or arbitrary editor-workspace folders. Explicit manifest members must be distinct
relative paths inside the containing folder and may not nest. For unrelated paths
outside a common supported workspace, configure each repository separately.

With unified mode, run `llewm init` at the workspace root; with federated mode,
initialize each selected member separately. Run project commands inside the member
or bind them with `llewm --project-root PATH`. At a federated group root, use
`workspace show`/`workspace route` rather than opening a shared store. Existing
member history is not automatically merged by workspace initialization; preserve
it and use the reviewed topology-change workflow when appropriate. Git-anchored
commands at a unified root require the member selector (`--repo`, or `repo` over
MCP); the member containing the working directory supplies the default.

Apply [Git exclusion checks](project-layout.md#keep-local-state-out-of-git) wherever
state is inside a Git working tree, including federated members. A workspace
root outside Git has no Git index or effective root `.gitignore`; do not claim
those checks passed or initialize a repository just to satisfy them. Keep its
state private and outside file-sync replication. **Current limitation:**
`trial start` still requires Git exclusion checks and therefore blocks a unified
workspace trial outside Git. Explain this before trial setup; use a selected
member/disposable Git demo for a trial, or deliberate full adoption for the
unified workspace. `init`/`doctor` can also report unavailable Git-protection
checks at that root. Do not present these diagnostics as live qualification.

For a new machine joining existing project history, use the
[shared-folder joining workflow](shared-folder-history.md#start-a-folder-endpoint)
or the [LAN receiving-install workflow](lan-history.md#new-installs-expecting-existing-history)
before choosing trial or prose adoption.

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
2. First complete the [Git exclusion prerequisite](project-layout.md#keep-local-state-out-of-git)
   wherever state is inside a Git working tree.
   From the selected project root, run `llewm init`, then `llewm mode audit-only`.
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

1. **Choose the project and environment.** Complete the independent installation
   phase and [topology selection](#choose-the-project-topology). Use the resolved
   project/workspace root, or each federated member, in the correct environment.
2. **Install the executable.** Follow [installation](installation.md) for source
   builds, persistent PATH, update and removal. [Preview bundles](preview-distribution.md)
   include a matching plugin package. Public remote installation is available
   only after the reviewed source revision is published.
3. **Protect and initialize local state.** For state inside a Git working tree,
   complete the [Git exclusion prerequisite](project-layout.md#keep-local-state-out-of-git):
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
   them in `llewm tui`. Follow source continuation pages through large documents;
   preserve original docs and use stdin for bundles to avoid scratch files.
   Interpretations stay proposed unless review/delegation
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
and [History sync](memory-repository.md) are separate choices.
For independent projects shared with different groups, review
[portfolio routing](portfolio-history.md) and each destination's access separately.
A private folder container can aggregate independent project lineages just as a
Git memory repository can. Group audiences need separate destinations and access
controls. Verify folder access in the actual IDE/agent launcher; terminal access
does not establish that a sandboxed client can reach the same path.
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
