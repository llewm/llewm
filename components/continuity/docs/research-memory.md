<!-- continuity:roadmap-node continuity-research-memory -->
# Research memory

Continuity preserves agent-curated research as attributable, searchable claims.
MCP API 132 and migration 079 add dated external-source observations and exact
claim citations to the existing work-narrative model. Agents supply the summaries
and evidence relationships; Continuity validates, stores and retrieves them. It
does not browse, summarize tool results, judge truth or archive whole pages.

## Storage and attribution

A source observation is a work narrative with optional structured `source` metadata:

| Field | Contract |
| --- | --- |
| `url` | Required HTTP(S) URL, preserved after surrounding whitespace is trimmed. Credentials and recognized secret/signed query parameters are rejected. Other parameters and fragments retain their meaning. |
| `accessed_at` | Required observation time, RFC3339, normalized to UTC. Separate from record time and publication time. |
| `authority` | Required `caller_asserted`, `machine_observed` or `user_reported`. This attributes the metadata; it does not establish the publisher's claim as true. |
| `publisher`, `version` | Optional bounded text. Omit unknown values. |
| `published_at` | Optional ISO date or RFC3339 timestamp. Omit if not known. |
| `content_sha256` | Optional hash of exact observed bytes. No page content is retained automatically; a URL hash is not a content hash. |

The narrative supplies the title, creator, summary claims, task anchors and immutable
ID. Several observations can describe the same exact URL at different times or
versions. Record a changed observation separately; use narrative supersession for
correction. Earlier cited observations remain accessible. URL equivalence is not
inferred and there is no second evidence-record store.

Each finding claim may have up to 16 `citations`. A citation names an exact
source-observation `narrative:ID`, a relation (`supports`, `contradicts`, `context`)
and an optional bounded section/page `locator`. Local writes and portable imports
reject missing or cross-project citation targets and narratives without source metadata.
Two findings can cite different sections of the same observation. The containing
claim's authority applies to its interpretation of that evidence.

Narratives retain the 32 KiB serialized limit and at most 64 claims. Agents choose
useful claim boundaries; no semantic splitter is implied. Text is redacted before
persistence. Source URLs must be supplied without private parameters; pattern-based
redaction and parameter checks cannot establish that an arbitrary URL is public.
Sources remain untrusted evidence, never instructions or execution authority.

## Capture workflow

1. State the question, task and scope, including relevant version, platform or
   date. A research work item is useful when the question needs its own lifecycle.
2. Search existing history before repeating research. Use a small text/topic/task
   query, or an exact `source_url` when the source is known. Recheck time-sensitive
   facts; a saved observation is evidence of an earlier reading.
3. Record the source observation and a concise scope summary. Keep useful permitted
   excerpts or paraphrases, not a browsing transcript. Preserve unknown publication
   metadata as unknown. Distinguish observed source text from the agent's inference.
4. Record findings in independently understandable claim chunks, with source
   citations beside each finding. Preserve contrary evidence, limitations and
   rejected approaches. Anchor findings to the active task and applicable roadmap.
5. Save the compact conclusion and narrative reference in the checkpoint. An
   explicit user choice belongs in the decision ledger, not an inferred research
   decision. Closing a research item as `answered` requires resolvable narrative,
   exact claim or decision targets in the same project. External documents are
   cataloged as source observations first.
6. Append corrected findings with supersession. Do not rewrite the historical
   observation or silently point an old citation at newer evidence.

The MCP narrative tool accepts `source` and per-claim `citations`. The CLI uses
`narrative record --source-json` and the existing `--claims-json`. For example,
a finding claim can contain:

```json
{
  "kind": "outcome",
  "label": "Finding within the investigated scope",
  "text": "State the specific finding and its applicable version here.",
  "authority": "caller_asserted",
  "citations": [
    {"source": "narrative:SOURCE_OBSERVATION_ID", "relation": "supports", "locator": "Section 2"}
  ]
}
```

Record the actual source observation first. IDs in examples are placeholders, not
valid evidence. Client-local search-result IDs cannot substitute for durable URLs
and project-owned observation references.

## Retrieval and navigation

The existing SQLite FTS5 index searches source titles, summary claims, URLs,
publishers and versions. `source_url` selects an exact URL's observations and citing
claims; `linked: narrative:ID` finds claims citing one observation. Search returns
bounded snippets and routing metadata, not whole narrative bodies. Topics, task
anchors, kinds, time filters and exact references narrow the next read.

API 131 defaults narrative/decision searches to `state: current`. Deliberate
`state: all` or `state: superseded` retrieves historical corrections and successor
references. Other families retain their own revision/lifecycle semantics. Exact
history expansion marks supersession before the claim, preserves file/region
hashes and attribution, and shows citation relations, locators and source correction
state. Follow the observation reference for dated metadata and its summary.
A work-item expansion includes its latest disposition and finding references,
and flags unresolved targets in older or imported answers. Portable histories
preserve their original caller-supplied answer links; the stricter answer-target
rule applies to new local dispositions. This does not relax citation integrity.

These hops remain explicit: question → finding claim → source observation. Source
bodies are not automatically expanded. Content budgets include truncation markers;
truncation is reported. The JSON envelope is additional overhead. `bodies_loaded: 0`
on search does not imply the search engine does no text processing. Dedicated
narrative show still supplies current local-file drift checks; no external page
is fetched or certified current by retrieval.

Canonical narratives and citations travel through curated portable memory.
Citation rows and the full-text index are rebuildable projections. Export/import
preserves exact references and source observations rather than inventing citations
for older prose-only records.

## Capture advisory and qualification

The [checkpoint advisory](checkpoints.md#research-capture-advisory) remains best
effort. When observed web discovery crosses its activity threshold, it reports
`cited_claims`, `uncited_findings` and `source_observations` for the task. An unrelated
record, plain evidence reference or structured checkpoint fields cannot alone mark
web-source capture covered. Outcome/verification claims without citations prompt
review of which findings actually depend on external evidence.

`covered` is structural coverage, not correctness or completeness. Tool-name
recognition and hook availability limit detection. The narrative/checkpoint skills
require useful research capture even when hooks miss the activity. Never invent
citations or filler to clear an advisory.

Qualification includes two claims citing one source, correction routing, missing
and cross-project target rejection, redaction, portable round-trip, index rebuild
and bounded question-to-source navigation. Independent agent-session recovery and
naturally produced research trials additionally record queries, candidates,
returned bytes, truncation, expansion depth, additional source reads, latency and
attributed usefulness. Passing fixture tests alone does not establish those live
results or corpus-scale relevance; see [retrieval qualification](narrative-graph.md#retrieval-qualification).

The original audit at `776dae6` found missing structured source relationships,
correction state in history and source-aware capture checks. The source changes
above address those contract gaps. Live rollout and qualification are tracked in
Continuity plan `research-memory`, not inferred from this document. Automatic
summarization, semantic deduplication, embeddings and ranking models remain optional
future experiments justified by measured failures, not prerequisites or promises.
