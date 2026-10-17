---
name: onboard
description: Set up or verify Llewm Continuity for a chosen Git project and coding-agent client, including first-checkpoint recovery. Use for installation, project adoption, or onboarding verification; not for resuming an existing task or changing permissions without user review.
---

# Guide Continuity onboarding

For a request to try the product, use the matching `docs/trials.md` workflow.
Prefer a disposable Git demo with no plugin setup for a first look. For a real
project, run `llewm trial start --prose PATHS` before client setup and list the
project prose that must survive. Keep prose in control; do not delete instructions
or replace them with Continuity queries. Trial hooks are silent and automatic
task bootstrap is refused. Save client setup receipts separately because shared
configuration is not owned by the trial. The review point requires explicit
`trial adopt --reviewed` or a reviewed handback and fresh prose-only verification.
Do not treat trial expiry as consent or skip the handback review. Full adoption
steps below apply only when adoption is requested.

Help the user reach a verified first checkpoint and fresh-session recovery. This
skill guides project adoption and live verification; the scoped installer registers
its bundled plugin through supported client managers. Preserve existing configuration
and project history. No model or arbitrary execution plan is required.

Establish the intended Git root, execution environment (including WSL or a remote
host), and client from context; ask only for missing choices. Inspect Git state,
`llewm --help`, `llewm version --output json`, and the selected client's installed
plugin identity. Do not assume the source checkout is the project to adopt.

If the binary or plugin is missing, read the installation and client-integration
guides from the selected Llewm source checkout, or their matching published
revision under `components/continuity/docs/`. Prepare the exact paths, version,
and configuration changes before applying authorized setup. Do not invent a
public release, marketplace command, or signing qualification. A remote Go build
installs only the executable, not these skills or hooks. Verify executable access
from the process that launches the client, not only an interactive shell.

From the chosen project, or with `llewm --project-root PATH` before each command:

- Run `llewm init` for initial adoption, then `llewm doctor` and
  `llewm database status`. Init is idempotent; existing state is retained.
- If migrations are pending, follow the documented backup/verify/migrate flow.
  Do not let a build, hook, or MCP startup substitute for migration verification.
- Preview `llewm install --client CLIENT --dry-run` with `codex` or `claude-code`.
  A person runs installation at a terminal; `--permissions` can reuse the existing
  reviewed permission setup after project init. Builds predating this command use
  their matching source-package workflow. Never simulate terminal input to install.
  Review hook activation separately from approvals; preserve unrelated settings.
- Preview `llewm client permissions --client codex` or
  `llewm client permissions --client claude-code`. Explain the actual proposed
  read/write approvals. The user runs `--write` at a terminal and reviews them;
  never simulate confirmation, create grants, or remove sandbox restrictions.
- Restart the client and parent IDE when necessary. Compare MCP
  `continuity_info` with the installed binary's version receipt; record unknown
  plugin identity as unknown. `doctor` alone does not prove live hook delivery.

Adopt an existing project progressively. Do not reconstruct semantic history
from all commits. Reuse an existing Continuity lineage when appropriate; do not
configure history sync, usage ingestion, reporting, or personal global guidance
unless requested. Reporting is a separate explicit opt-in, never a setup default.

Read the checkpoint skill to save a concise first adoption checkpoint with the
current task, authoritative sources, checks and unresolved setup limits. State
that older semantic history was not imported. Do not replace an existing task's
checkpoint with an onboarding report; create a distinct onboarding task when
needed. Dry-run, save, and read it back through the available supported surface.

Have the user open a fresh session and request continuation. Verify that it
recovers the exact checkpoint, objective and next action, and that SessionStart
was observed. Distinguish prepared setup, installed configuration, current MCP
identity, and observed recovery. If a client is unavailable, leave its live
verification pending rather than claiming success. An unexpired presence record
from a prior session does not prove another writer; honor the user's report of
single-session operation and inspect Git state before proposing a worktree.

Finish with the installed version, adopted project, client, checks passed, any
remaining user action, and the checkpoint reference. Keep native platform or
client qualifications limited to the versions and environments actually observed.
