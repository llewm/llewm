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

The JSON contains `sources` with original paths and SHA-256 hashes, `documents`
with redacted text chunks, `complete`, and `next_cursor`. Each chunk has `path`,
`sha256`, `text`, and `offset`/`end` byte positions in the **redacted** document.
Paths are project-relative regular UTF-8 `.md`, `.txt` or `.rst` files, outside
Git/tool state and without symlink traversal. Missing files are errors: select
the files that exist. Select up to 16 sources per sequence. Defaults bound each
page to 48 KiB of redacted text, with at most 64 KiB per chunk. `--max-bytes`
sets a page budget from 4 bytes to 256 KiB; JSON metadata/escaping adds overhead.

Large documents and selections automatically continue across pages. When
`complete` is false, repeat the **same ordered source selection** with the
returned `next_cursor` until `complete` is true:

```sh
llewm import-prose --sources AGENTS.md,README.md,docs/PLAN.md --cursor NEXT_CURSOR
```

This is the default for oversized inputs: process all chunks instead of skipping
large roadmaps, asking the user to restructure docs, or stopping the import.
Chunks exist only in memory and command output; no temporary source files or
Git changes are needed. Paragraph/line boundaries are preferred, with UTF-8 and
CRLF preserved. Read all pages before finalizing the interpretation, combining
sections that span chunks. A cursor rejects changed sources or a changed source
selection; restart from the first page and reconcile the interpretation if that
happens. For more than 16 sources, use further explicit selections.

Hashes identify complete original files, and bundles reference those original
paths once, regardless of how many chunks they produced. Validation reads one
file at a time with a separate 16 MiB per-file resource limit; it redacts the
whole file before chunking so a split cannot expose a partial credential. This
limit is distinct from the response budget. Inputs above it require a scoped
follow-up; do not silently omit them or rewrite originals. Interpreted fields
are also redacted before persistence. Inspect source selection and redacted
output before sharing.

## Prepare and preview the interpretation

Use stable IDs for tracks, plans and items. On existing installations, inspect
current records first and reuse their IDs. Summaries should distinguish explicit
source statements from interpretations, uncertain status and unresolved conflicts.
Do not infer task priority or dependencies from document order. Preserve useful
project docs; importing is not permission to delete or restructure them.

Prepare a version-1 bundle. Prefer `--file -` to pass UTF-8 JSON on stdin for
both preview and apply, avoiding scratch files and direct writes to protected
`.llewm` state. Use literal shell input or a structured process invocation;
do not interpolate source prose into executable shell text. The following
example needs its source hash and actual model identity filled from the snapshot:

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

If the interpretation exceeds a bundle limit, preview/apply successive batches
of complete tracks with distinct import IDs and the same original-file evidence.
Keep a track's plan together; reconcile against current revisions if a later
batch revisits it. Report coverage across all batches instead of claiming that
the first batch represents the entire import.

```sh
llewm import-prose --file -
llewm import-prose --file - --apply
llewm tui
```

Supply the same JSON on stdin to both commands; without piped input they wait
for it. For PowerShell, pipe a literal JSON here-string using UTF-8 output
encoding; for POSIX shells use a quoted heredoc delimiter. `--file PATH` remains
available when a file is more practical. Use a unique directory in an already
writable OS temp location (on Windows, the process temp directory) if permitted.
If the sandbox only permits project writes, use an already ignored, writable
scratch directory outside protected state. Otherwise a unique untracked project
scratch directory is acceptable: it may appear in `git status` until cleanup.
Do not change permissions or Git exclusions just to conceal scratch files, and
do not treat a denied `.llewm` write as blocking stdin-based import. Remove only
scratch files this operation created, on success or failure, after recording
any useful diagnostics; never remove or split original source documents.

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

Read back the records/receipt and confirm original-file provenance. Clean up any
scratch bundle JSON created by the operation, including on failure. The
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
exercise the same local recovery workflow as adoption. History sync remains
an optional separate choice.

## Upgrade

Paged source snapshots and stdin bundles require an executable exposing
`import-prose --cursor` and matching onboarding skills (source plugin version
0.1.32). Update both the executable and plugin, then restart the client. These
changes need no additional database migration or writer API change.

This CLI workflow and doctor project-state reporting ship with MCP API 126 and
additive migration `078_prose_imports.sql`; writer API remains 125. Follow the
[backup-gated migration workflow](installation.md#migration-bearing-updates).
Install the matching bundled onboarding skills and restart clients before
claiming the new behavior is active in an existing session.
