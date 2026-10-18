---
name: onboard
description: Set up or verify Llewm Continuity for a chosen Git project and coding-agent client, including first-checkpoint recovery. Use for installation, project adoption, or onboarding verification; not for resuming an existing task or changing permissions without user review.
---

# Guide Continuity onboarding

Establish the starting path: usage-audit baseline, full local trial, or ongoing
adoption. Infer it from the user's request; ask once if it is missing. Do not
turn audit-only setup into a trial/adoption or full plugin installation.

Before creating project state for ANY starting path, follow the canonical Git
exclusion policy in `docs/project-layout.md#keep-local-state-out-of-git`. From
the confirmed Git root, check `git check-ignore --no-index -q -- .llewm/` and
`git ls-files -- .llewm`. Require successful whole-directory exclusion and no
tracked state. If protection is missing, default to adding `/.llewm/` to the root
`.gitignore` under the user's onboarding authorization; use the Git local exclude
file for personal-only use in a shared repository. Resolve that file with
`git rev-parse --git-path info/exclude`, including in worktrees. Preserve existing
rules and line endings, show the resulting diff or local change, and verify again.
Reuse effective existing exclusions. Explain already-tracked state and obtain a
separate reviewed resolution before proceeding; never automatically untrack,
delete state, or rewrite history. `init` and `doctor` warnings do not replace this
onboarding prerequisite.

For a usage baseline, follow `docs/usage.md` and the audit-first section of
`docs/onboarding.md`: install only the executable, initialize the chosen project,
select `llewm mode audit-only`, leave full plugin/MCP/skills disabled, and collect
usage offline in fresh sessions. Passive hook registration is optional and
separate. No semantic prose import or checkpoint recovery is required.

For a request to try the product, use `docs/trials.md`. A disposable Git demo is
one option; a real-project trial runs `llewm trial start --prose PATHS` before
client setup and includes every source doc that must survive. Trials support the
full local workflow, including normal hooks and checkpoint recovery. Preserve
original docs as the baseline; do not require duplicate maintenance of every
operational plan or delete useful project guidance. History sync remains disabled.
Save shared client setup receipts separately. Review expiry never adopts or exits.
Both trial and adoption can be left through a knowledge-preserving prose handback.

During trial AND adoption onboarding, populate a useful project model by default:

1. Identify authoritative instructions, roadmap/plan and current-work docs from
   root guidance and explicit references. Select a bounded set; do not scan the
   whole repository or reconstruct history from every commit.
2. Follow `docs/prose-import.md`. Run `llewm import-prose --sources PATHS` to
   obtain redacted text and original source hashes. Follow `next_cursor` with
   the same ordered `--sources` and `--cursor` until `complete` is true. Large
   documents are chunked in memory by default: read all chunks, combining split
   sections, without skipping large files or splitting/restructuring originals.
   A changed-source cursor requires a fresh snapshot and reconciliation.
   Interpret these documents once using this session's model; the executable
   calls no model. Read current DB tracks
   and plans first on an existing installation and reuse their stable IDs.
3. Prepare a version-1 bundle, preserving each original source path once,
   heading/excerpt, source hash and exact expected node/plan revisions. Map
   explicit item statuses; mark unknown status pending and put uncertainty or
   conflicts in the plan summary. Never invent active work, dependencies or
   historical user approvals. Prefer UTF-8 JSON on stdin with
   `llewm import-prose --file -` for preview, then pass the same bundle with
   `--apply` under the user's onboarding instruction. No scratch source files
   or direct writes to protected `.llewm` state are needed. If a bundle file
   is more practical, use an already permitted OS temp directory or ignored
   writable project scratch directory; a unique untracked project directory
   is acceptable until cleanup. Do not change permissions or Git exclusions
   just to hide scratch files. See the prose-import guide for shell input,
   bounded multi-batch imports, and scratch cleanup.
   Applying imports records interpretations; it does not grant work execution.
4. Show tracks/plans in `llewm tui`; proposals are visible before review. Present
   one concise review of imported intent, uncertainties and missing sources.
   Activate plans only with explicit review or existing delegated policy; never
   change policy or name the user as a model-authored record's creator.
   Use the existing roadmap review interface for inferred node status. Explain
   retained source docs and which operational state will be maintained in the DB.
5. Preserve relevant source-backed facts with ordinary narrative/resource tools;
   record decisions as user decisions only when explicitly reviewed. Save the
   current objective and next action in a checkpoint once grounded. If there are
   several plausible current tasks, ask for selection instead of inventing one.
   Bind the chosen task to its track with `llewm roadmap link --kind task --node
   NODE --task TASK --reason REASON --request-key KEY` after checkpoint creation
   so joined views and TUI selection identify the same current work.
   Read back import receipts and original-file evidence. Remove only scratch
   files created for this operation on success or failure; preserve originals.
   On rerun, re-read changed sources and current record revisions; reconcile
   conflicts.


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

- Run `llewm init` for initial setup, then `llewm doctor` (or
  `llewm doctor --output json`) and
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

Read the checkpoint skill to save a concise first trial/adoption checkpoint with the
current task, authoritative sources, checks and unresolved setup limits. State
which current prose sources were imported and that older semantic history was not
reconstructed. Do not replace an existing task's
checkpoint with an onboarding report; create a distinct onboarding task when
needed. Dry-run, save, and read it back through the available supported surface.

Have the user open a fresh session and request continuation. Verify that it
recovers the exact checkpoint, objective and next action, and that SessionStart
was observed. Distinguish prepared setup, installed configuration, current MCP
identity, and observed recovery. If a client is unavailable, leave its live
verification pending rather than claiming success. An unexpired presence record
from a prior session does not prove another writer; honor the user's report of
single-session operation and inspect Git state before proposing a worktree.

Record a current-host installation resource with the method (source build,
binary bundle or package manager), source checkout/remote name/branch or pinned
revision, installed path, OS/architecture, commit/API/writer identity, affected
project store and plugin owner/marketplace. Include backup/migration/verification
receipt references, not credentials or raw configuration. This lets the `update`
skill reuse the actual installation path later without repeated discovery. Keep
other hosts' installation resources distinct.

Finish with the installed version, adopted project, client, checks passed, any
remaining user action, and the checkpoint reference. Keep native platform or
client qualifications limited to the versions and environments actually observed.
