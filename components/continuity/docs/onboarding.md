# Guided onboarding

Start here to set up Llewm for one Git project. You can follow the steps yourself
or ask your coding agent to guide them. The agent checks prerequisites, prepares
specific changes, and verifies results. Llewm remains a local Go executable with
no required model or installation service.

Before the plugin is installed, give your agent this request with the matching
source checkout or documentation available:

> Set up Llewm Continuity for this Git project and my coding-agent client. Follow
> the installation and client-integration guides. Preserve existing configuration
> and history, show me the permission changes, and verify a first checkpoint that
> a fresh session can recover. Keep optional reporting and history sync disabled.

Once installed, the plugin's `onboard` skill supports the same process. In Codex,
invoke `$onboard`; in Claude Code, invoke `/llewm:onboard`, or ask to onboard this
project in ordinary language. Fresh-client skill routing still needs qualification.

## Choose a trial before adoption

Start with a [disposable demo or reversible project trial](trials.md). For a real
trial run `llewm trial start --prose PATHS` before changing client setup. Existing
prose remains in control, task bootstrap is suppressed and History sync is disabled.
At the review point, choose `trial adopt --reviewed` or the reviewed prose-handback
workflow. A review reminder never automatically adopts or deletes anything.
The steps below describe full adoption; do not silently perform that cutover for
someone who only asked to try the product.

## Setup and verification

1. **Choose the project and environment.** Use the Git root of the project to
   adopt, not automatically the Llewm source checkout. Install separately inside
   WSL, remote development hosts, and containers that run the client.
2. **Install the executable.** Follow [installation](installation.md) for source
   builds, persistent PATH, update and removal. [Preview bundles](preview-distribution.md)
   include a matching plugin package. Public remote installation is available
   only after the reviewed source revision is published.
3. **Initialize local state.** From the project run `llewm init`, `llewm doctor`,
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
6. **Save an adoption checkpoint.** Name the current objective, authoritative
   project instructions, observed checks, and remaining limits. Use the checkpoint
   skill's dry-run/save/readback flow. Adopt existing history progressively; do
   not manufacture knowledge from every old commit. Retain an existing lineage.
7. **Recover in a fresh session.** Ask to continue. Confirm the exact checkpoint,
   objective and next action, plus the observed SessionStart event. Record the
   client/plugin versions and OS/architecture that passed.

The result should distinguish what was prepared, installed, and observed in a
live client. If the agent cannot observe a fresh session, that final check remains
pending. Expiring presence from a previous session is advisory, not proof of a
second writer.

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
