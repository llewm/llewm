<!-- continuity:roadmap-node continuity-idea-inbox -->
# Work items

Status: implemented in 6e4a of `continuity-multi-repo-sync` (migration 056, MCP
API 85). Work items replace the idea inbox, the agent-suggestion ledger, and
friction dispositions, which were three record families for one job.

A work item retains something to look at later without changing the active
checkpoint, roadmap, or execution state. Items are project-scoped, append-only,
redacted before storage, and attributed. They are not instructions, decisions,
or authorization.

## Kinds

| Kind | Use | Extra fields |
| --- | --- | --- |
| `idea` | A tangent or proposal to discuss later. Former ideas and agent suggestions. | none |
| `bug` | Observed misbehavior. | `observed`, `reproduction`, `environment`, `severity` (`low`, `medium`, `high`, `critical`) |
| `task` | Concrete work to do later. | none |
| `research` | An open question; the title is the question. It closes as `answered` with links to its findings (migration 067, plan item 8j). | none |

Every item has a title, a body, tags, and optional originating task, checkpoint,
and roadmap node. Authority is `caller_asserted` for captures; converted agent
suggestions keep `model_suggested` or `user_reported`.

## Workflow

```text
llewm item add --kind idea --title "..." --body "..." [--tag TAG]
llewm item add --kind bug --title "..." --body "..." --observed "..." \
  --reproduction "..." --environment "..." --severity high
llewm item add --kind task --title "..." --body "..." --from work_item:ID
llewm item list --status open [--kind bug] [--tag TAG]
llewm item list --audit --max-bytes 8192
llewm item list --friction --min-count 2
llewm item show work_item:ID
llewm item dispose work_item:ID --status accepted --reason "..." --target-ref plan:PLAN/ITEM
```

MCP exposes the same workflow through three tools: `continuity_item_capture`,
`continuity_item` (show with `id`; list without it; `audit` for the pending
packet; `friction` for repeated-friction candidates), and
`continuity_item_disposition`. The bootstrap shows one machine-observed line with
the count of open items by kind; item content is never injected into it.

References use `work_item:<id>`. Former `idea:<id>` and `suggestion:<id>` records
kept their IDs, so `work_item:<former id>` resolves them.

## Selected bug reports

MCP API 115 adds a derived report over an existing bug:

```sh
llewm item report work_item:ID --expected "..." --hypothesis "..."
llewm item report work_item:ID --expected "..." --output json
```

Use `continuity_item` with `id` and `report: true` for the same v1 JSON report.
Optional `expected` and `hypothesis` are report-only caller claims. Source
authority, creator, model and time remain explicit. Missing details are listed,
never invented. Machine-derived reporting-runtime diagnostics are separate from
the source's affected-environment claim.

Only selected bug text and provenance are included: no links, dispositions,
task/checkpoint content, transcripts, repository files or arbitrary environment.
Projection redacts again and caps indented JSON at 32 KiB; oversized reports are
rejected intact. The SHA-256 ID identifies projected content before the ID is
populated, not verified causality, a semantic duplicate or submission permission.

Preparation is read-only and makes no network request. The independent
[Forgejo adapter](../../reporting/README.md) owns queueing, credentials, opt-in
delivery and receipts. Local bug records still travel through memsync; delivery
does not change their lifecycle.

## Lifecycle

An item starts `pending`. Dispositions are append-only:

- `accepted`: the direction is endorsed and mapped to a target (a plan item,
  roadmap node, or another work item); the item stays open.
- `deferred`: not now; the item stays open and can be decided later.
- `integrated`, `discarded`, `merged`: terminal; they cannot be rewritten.
- `answered`: terminal, research items only; the targets are the findings (a
  narrative, decision, document, or report).

`accepted`, `integrated`, `merged`, and `answered` require at least one target
reference. The bootstrap counts open research items as open questions, apart from
ideas.
`open` in list filters means pending, accepted, or deferred. Exact show renders
the full disposition history and the item's links.

Promotion: `--from work_item:ID` (MCP `from`) captures a new item, adds a
`derived_from` link to the source, and records the source as `integrated` into the
new item in the same transaction. An idea becomes a bug or task this way; a bug or
task becomes plan work by an `accepted` or `integrated` disposition whose target is
the plan item. A track plan revision names the items it takes up in
`work_item_ids` (plan level and item level).

Links are typed, caller-asserted relationships from an item to a target:
`derived_from`, `follow_up_to`, `supports`, `depends_on`, `alternative_to`,
`duplicates`, and `belongs_to_track`.

<!-- continuity:roadmap-node continuity-friction-signals -->
## Repeated friction

`item list --friction` (MCP `continuity_item` with `friction`) reports repeated
failure/recovery shapes derived from recorded tool events. It groups a failure
with a later successful retry of the same command family in the same client
session by command family, redacted failure signature, and recovery shape
(environment variable names kept, values replaced with `*`). The reference is
`friction:<id>`, a stable hash of those shapes. Every candidate reports
`cause: unknown`; the report diagnoses nothing and applies no remedy.

Candidates stay derived and are never stored. To track one, capture a bug with
`--from friction:ID`. The report then lists the items tracking each candidate
with their kind and status; an untracked candidate says how to track it.

Bounds: hook ingestion keeps at most 512 bytes of redacted failure signature;
each report scans at most the newest 5,000 project tool events and reports
scanned and total counts; defaults are two occurrences, 20 candidates, six
representative event references per candidate, and 8,192 rendered bytes.

## Bounds and exclusions

- Text fields are redacted and bounded to 16 KiB each; at most 32 tags and 32
  links or disposition targets per record.
- Lists return at most 100 metadata records and never load bodies.
- Show, audit, and friction Markdown accept 512–32,768-byte budgets and report
  truncation.
- Out of scope (decision `coverage-matrix-and-work-items`): assignees,
  estimates, sprints, boards, notifications, priority beyond bug severity, and
  two-way sync with GitHub Issues.

## Portability

Work items travel through the memory repository as `work_item`,
`work_item_disposition`, and `work_item_link` records. Import validates kinds,
bug fields, dispositions, and that dispositions, links, and plan provenance name
an existing or bundled item.

## Conversion (migration 056)

Migration 056 converted the earlier records in place on each machine:

- ideas, their tags, and dispositions became `idea` items with the same IDs;
- agent suggestions became `idea` items tagged `suggestion` and their former
  kind (for example `next-action`); summary became the title and rationale the
  body; `rejected` became `discarded` and `superseded` became `merged`; an
  `idea_id` became a `follow_up_to` link; suggestion links were kept, with
  `suggested_while` mapped to `derived_from`;
- friction dispositions (none existed) were dropped;
- decision and topic links to `idea` or `suggestion` targets now name
  `work_item`, and the affected decisions were rehashed;
- plan provenance (`suggestion_ids`) became `work_item_ids`; and
- history search indexes items under the `work_item` family.
