<!-- continuity:roadmap-node continuity-work-narratives -->
# Work narratives

Status: implemented in source at migration 018 and MCP API 26, described below in
[implemented first storage contract](#implemented-first-storage-contract) and
[implemented trigger behavior](#implemented-trigger-behavior). Non-commit
recovery, verification and decision episodes informed this slice; their detailed
observations are retained privately. The general work-narrative record family and its explicit
capture policy now exist alongside the existing specialized commit-narrative form.

## Product decision

The durable semantic unit is a work narrative, not a Git commit. A project may have
many meaningful decisions between commits, several commits for one decision, or no
Git repository at all. Continuity therefore must not use commit frequency as a proxy
for reasoning density or progress.

A work narrative preserves curated context at a meaningful boundary:

- what changed or was decided;
- why it mattered and which alternatives were rejected or deferred;
- constraints, failed approaches, and verification evidence;
- known limitations and the next decision or action; and
- attributable actor and invocation references when available.

This is a concise account supplied or reviewed by an actor. It is not a raw
transcript, hidden chain-of-thought, inferred intent, or automatic claim that work
is correct.

## Relationship to checkpoints and roadmaps

A checkpoint remains the compact resume kernel: current objective, state, and next
action. A work narrative holds the richer decision and implementation context that
would make every checkpoint too large. Checkpoints and roadmap nodes may link to one
or more narratives through stable references.

The intended drilldown is:

```text
project pulse
  -> checkpoint or roadmap node
    -> narrative metadata
      -> exact bounded narrative
        -> exact source and verification anchors
```

Roadmap nodes express planned structure and state. Narratives explain how and why
that state changed. Neither silently rewrites the other.

The accepted [roadmap provenance follow-on](roadmap-provenance-and-coordination.md)
uses these anchors to associate meaningful work episodes with exact roadmap
revisions and machine-observed file changes. It keeps expected roadmap bindings,
observed touches, and narrative interpretation separate; it does not turn every
tool call or transient edit into a durable narrative.

## Anchors and source precision

A narrative may carry several independent anchors:

- checkpoint ID or bounded checkpoint interval;
- task ID;
- roadmap node ID and exact revision;
- Git commit ID when a commit is relevant;
- project-relative file path plus content hash;
- symbol, line span, or other deterministic region locator;
- actor invocation; and
- verification or portable-evidence reference.

File-region anchors must retain the producer-side content hash. If the current file
has drifted, retrieval reports that fact; it does not silently relocate the region
or present historical lines as current. Symbol lookup can be a convenience view but
cannot replace the immutable anchor.

## Capture policy

Projects choose explicit narrative triggers. Supported policy vocabulary should
include meaningful checkpoint, accepted decision, completed roadmap slice, commit,
and manual capture, with combinations allowed. A commit-only policy remains valid
for teams that prefer it. Agent-heavy projects can instead require narratives when
decisions or checkpoint boundaries carry durable context.

Automatic lifecycle checkpoints do not automatically deserve a rich narrative.
Capture must remain bounded and attributable, and any model-authored narrative is a
caller assertion until reviewed under the selected project policy.

## Storage and transport independence

Narrative semantics are independent of transport. The same immutable records may
be kept in the local Continuity store and transported through:

- an export tracked in the project repository;
- a separate private Llewm repository;
- filesystem or removable-media bundles; or
- later authenticated object or peer transport.

Git may be used internally if it proves useful, but Git identity and repository
layout are not part of the semantic contract. No choice between an internal Git
store and another content-addressed store is made by this document.

## First implementation slice

Before adding a new record schema, exercise several real non-commit work episodes
and define the smallest fields and trigger behavior that recover their decisions.
Then extend the existing metadata-only list and bounded exact-show pattern with
precise roadmap/checkpoint/source anchors. Acceptance requires deterministic
round-trip, redaction, explicit current-source drift, project isolation, and useful
route-to-exact drilldown without loading all narrative bodies.

The four observations cover explicit cross-client recovery, installed fresh-client
workflow qualification, post-restart live verification, and a decision with several
rejected write/authority alternatives. They establish the following minimal first
storage contract.

## Implemented first storage contract

One immutable `work_narrative` record carries only routing and integrity metadata:

- stable ID, project ID, bounded title, and a semantic hash over the canonical
  title, trigger, occurrence boundary, claims, and anchors;
- explicit trigger: `meaningful_checkpoint`, `accepted_decision`,
  `completed_roadmap_slice`, `commit`, or `manual`;
- optional occurrence start/end timestamps, distinct from recorded time;
- creator and optional exact actor invocation;
- optional superseded narrative ID for correction without rewriting history;
- schema version; and
- one bounded canonical payload containing ordered claims.

Each payload claim has a small kind (`intent`, `outcome`, `constraint`,
`alternative`, `verification`, `decision`, `next_action`, `limitation`, `interface`,
or `note`), bounded text, and its
own authority. Claim-level authority is required because one episode can combine
machine observations, actor interpretation, caller-asserted checkpoint content, and
user-reported evidence. The initial authority vocabulary is `machine_observed`,
`caller_asserted`, and `user_reported`; model-authored interpretation remains
`caller_asserted`. A record-wide content-authority field would erase that distinction.

Anchors are ordered child records so metadata-only discovery can filter without
loading narrative bodies. An anchor carries kind, stable target, optional exact
revision or content hash, optional bounded region locator, and observation authority.
Initial kinds are `checkpoint`, `task`, `roadmap_node`, `commit`, `file`, `region`,
`invocation`, `verification`, and `portable_evidence`. Commit is optional. File and
region anchors require a normalized project-relative path and producer-side content
hash; exact show computes `current`, `missing`, or `drifted` against the current
project without mutating the stored anchor.

List returns routing metadata and anchor summaries only. Exact show loads one
bounded payload plus anchor drift observations. Existing `commit_narrative` records
remain immutable specialized records and appear in unified discovery; no importer,
rewrite, or Git-note reconciliation is added.

## Implemented trigger behavior

The first implementation exposes explicit manual record creation with a required
trigger reason. It does not automatically create narratives from hooks, commits,
checkpoints, tool calls, or roadmap changes. A later project policy may request a
narrative at selected meaningful boundaries, but the actor still supplies or
reviews the bounded claims. Model-authored content remains caller-asserted unless a
separate accepted review record says otherwise.

Trigger selection never changes checkpoint, roadmap, Git, or authority state. A
failed capture attempt may be observed as activity, but absence of a narrative does
not imply the underlying work failed. Automatic capture, ranking, summarization,
and startup injection remain out of scope for the first slice.

Acceptance requires deterministic canonical round-trip, redaction before
persistence, project isolation, correction by supersession, claim-authority
preservation, metadata-only route to bounded exact show, file/region drift across a
real source change, a narrative spanning several checkpoints, and compatibility
with existing commit-narrative discovery and portable export.

Commit-linked narratives now render their Git note as described below. The
engineer/project commit policy determines which boundaries require capture;
record/projection design does not change that policy.

Migration 018 and MCP API 26 implement this slice. The CLI exposes explicit
`narrative record`, metadata-only `narrative list`, and bounded exact
`narrative show`; MCP exposes the corresponding record/list/show tools. Unified
list results include both general work narratives and the existing specialized
commit narratives. Curated portable export/import preserves work narratives,
ordered anchors, exact referenced invocations, and explicit supersession edges.

## Commit narratives write the Git note

Per decision `narrative-writes-git-note`, a narrative recorded with `--commit`
(MCP `commit`) is the commit's canonical record. MCP API 83 adds this; there is
no migration.

- It needs `outcome`, `intent`, `verification`, and `limitation` claims. When
  one does not apply, a claim of that kind says why; the record is refused
  otherwise.
- An interface change (MCP API version, CLI commands, migrations) is its own
  `interface` claim (migration 054, MCP API 84), rendered under its own
  "Interface changes" heading after What changed. It is not required.
- After the narrative is appended, Continuity renders the note: a
  `# <full hash> — <title>` line, then the commit-policy sections in claim order:
  What changed (`outcome`), Interface changes (`interface`), Why (`intent`), Design decisions (`decision`,
  `constraint`, `alternative`), Verification, Limitations and remaining work
  (`limitation`, `next_action`), and Notes (`note`). Sections without claims are
  left out. Verification is split by authority: "run in this session"
  (`caller_asserted`), "observed by Continuity" (`machine_observed`), and
  "inherited or reported, not rerun" (`user_reported`).
- The note goes to `refs/notes/commits` in the member repository holding the
  commit. If the commit has no note, it is added. If the note already contains
  these sections, it is left alone. Otherwise the existing note is kept and the
  rendering is appended under `# Amendment from narrative <date>`.
- `--no-note` (MCP `skip_note`) records the narrative only. The result is the
  record plus `note` (`ref`, `commit`, `action`: `added`, `appended`,
  `unchanged`, or `skipped`).
- The narrative is written first. If the note write then fails (for example, no
  Git identity), the record is kept and the error names it.

Generated notes parse back into the same claim kinds, so `narrative
import-git-notes` and the note check still read them.
