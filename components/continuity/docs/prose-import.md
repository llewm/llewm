# Populate a project from prose

Trial and adoption onboarding should populate useful tracks and plans before
asking a new user to evaluate the TUI. The onboarding agent interprets a bounded
set of current project docs once. `llewm import-prose` supplies redacted source
text, validates the interpretation and records it; it never calls a model or
rewrites project docs. Audit-only baseline setup skips this step.

## Read selected sources once

After `llewm init` or `llewm trial start`, select the authoritative instructions,
plans, roadmap and current-work docs referenced by the project. For example:

```sh
llewm import-prose --sources AGENTS.md,README.md,docs/PLAN.md
```

The JSON contains `documents`, each with `path`, `sha256` and redacted `text`.
Paths are project-relative regular UTF-8 `.md`, `.txt` or `.rst` files, outside
Git/tool state and without symlink traversal. Missing files are errors: select
the files that exist. Defaults bound the response to 48 KiB of original source,
64 KiB per file and 16 files. `--max-bytes` can raise the total to at most 256 KiB.
Exceeding a limit reports an error instead of silently importing partial prose.
Hashes identify original bytes; source text and interpreted fields are redacted
before persistence. Inspect source selection and redacted output before sharing.

## Prepare and preview the interpretation

Use stable IDs for tracks, plans and items. On existing installations, inspect
current records first and reuse their IDs. Summaries should distinguish explicit
source statements from interpretations, uncertain status and unresolved conflicts.
Do not infer task priority or dependencies from document order. Preserve useful
project docs; importing is not permission to delete or restructure them.

The agent writes a version-1 bundle, preferably as scratch JSON under ignored
`.llewm/continuity/`. This example needs its source hash and actual model identity
filled from the snapshot/session:

```json
{
  "version": 1,
  "id": "initial-onboarding",
  "creator": "onboarding-agent",
  "model": "ACTUAL_SESSION_MODEL",
  "sources": [{"path": "docs/PLAN.md", "sha256": "SHA256_FROM_SNAPSHOT"}],
  "tracks": [{
    "id": "parser",
    "plan_id": "parser-plan",
    "title": "Build the parser",
    "summary": "Source says parsing is next; implementation status needs review.",
    "source_path": "docs/PLAN.md",
    "section": "Parser roadmap",
    "expected_node_revision": 0,
    "expected_plan_revision": 0,
    "items": [{
      "id": "parse-input",
      "position": 1,
      "title": "Parse input",
      "detail": "Implement the input parser described in the source.",
      "acceptance_criteria": "The documented valid and invalid examples pass.",
      "status": "pending"
    }]
  }]
}
```

`section` is an exact heading or short excerpt present in the redacted source.
Each track selects a source in `sources`; all listed source hashes must still
match. Use item statuses explicitly supported by the source; unknown is `pending`
with an explanation in the summary. Normal item hierarchy/dependencies and
evidence fields follow [track plans](track-continuity.md). Bundles are limited
to 512 KiB and 32 tracks, with the existing 100-item limit per plan.

```sh
llewm import-prose --file .llewm/continuity/onboarding.json
llewm import-prose --file .llewm/continuity/onboarding.json --apply
llewm tui
```

Preview uses the same validation and plan policy as apply, then rolls back the
batch. Apply appends nodes, plans and an import receipt atomically. It preserves
source path, section and SHA-256 evidence on the records. Plans retain the agent's
model identity and follow the [existing plan-review policy](plan-agency.md).
New roadmap nodes are proposed interpretations; neither imported item statuses
nor visibility in the TUI authorize executing work. A project without a current
checkpoint opens the TUI on recorded tracks when its current-work view is empty.

## Review and continue

Show one concise review of imported tracks, intended work, uncertain statuses
and omitted sources. Record explicit plan review through the existing interface:

```sh
llewm track-plan record parser-plan --creator REVIEWER --status active
```

Use `llewm roadmap node` to record reviewed node status, preserving its source
reference and other fields. Review is not invented by the importer. Existing
delegation applies only within the configured policy; onboarding never grants
itself planning authority. Relevant facts can be captured through ordinary
narrative/resource tools. Historical prose is not a new user decision or proof
of verified work. Once current work is grounded, save a first checkpoint and
verify fresh-session recovery during both trial and adoption.

Read back the records/receipt and remove scratch source/bundle JSON. The
device-local receipt stores replay identity and source hashes; normal portable
records preserve the curated nodes/plans and their provenance. Usage and raw
documents are not added to History sync by this operation.

## Repeated onboarding and reconciliation

The same import ID and content returns `already_applied` without changing later
work. Reusing an ID with different content is an error. For an updated import,
use a new import ID and the exact latest node/plan revisions. Changed sources,
stale records, invalid sections and invalid items reject the entire batch.
New plan intent remains a proposal when review is required, leaving the last
operative plan intact. Changes to reviewed roadmap title/source need the
ordinary roadmap review interface rather than silently replacing that state.

There is no continuous prose-to-DB synchronization. Establish which operational
state the DB will maintain and avoid duplicate plan maintenance by default.
Trial retains original prose for comparison and [handback](trials.md); it can
exercise the same local recovery workflow as adoption. Optional History sync
and reporting remain separate choices.

## Upgrade

This CLI workflow and doctor project-state reporting ship with MCP API 126 and
additive migration `078_prose_imports.sql`; writer API remains 125. Follow the
[backup-gated migration workflow](installation.md#migration-bearing-updates).
Install the matching bundled onboarding skills and restart clients before
claiming the new behavior is active in an existing session.
