# Research memory

Continuity can preserve agent-curated research as attributable, searchable claims
and retrieve selected findings within explicit context budgets. External web
research currently depends on the agent writing useful summaries and citations.
It is not yet a structured source catalog with claim-level evidence relationships.

This guide separates the supported workflow from proposed improvements. The source
audit was performed at `776dae6021749bf576656bb9635e6b6e6d64f196` on 2026-10-18.
The proposal is not an implementation commitment or an authorization to execute it.

## What works today

| Need | Current support | Limit |
| --- | --- | --- |
| Preserve the question and context | A `research` work item holds the question; task and roadmap anchors connect findings to work. | Creating a question or narrative is explicit agent work. |
| Summarize findings and evidence | Work narratives contain labeled claims with kinds, attribution, occurrence time and a record timestamp. | Agents supply the meaning, summary and source citations; hooks do not summarize research results. |
| Retrieve a small unit | Each claim has a stable `narrative:ID#ORDINAL` reference. | Chunk boundaries follow the submitted claims. A long claim is not automatically split into useful semantic units. |
| Find relevant material | Rebuildable SQLite FTS5 indexes claims and other record families; search supports text, topic, kind, linked target and time filters. | Ranking is lexical. Returned snippets are bounded; corpus-scale latency and relevance are not established. |
| Expand selected evidence | History show expands exact references with attribution and a content-byte budget. Dedicated narrative show exposes the canonical record and local file drift. | History expansion does not provide all canonical provenance or narrative supersession state. |
| Link and correct knowledge | Narratives have task, checkpoint, roadmap, commit and file/region anchors; topics can link individual claims. Corrections append superseding narratives. | Sources are not a typed URL catalog; anchors belong to the narrative, not individual claims. |
| Notice missing capture | Checkpoint receipts can report substantial discovery with little structural output. | `covered` is not evidence that sources, conclusions or citations are complete or correct. |

The implementation is in [work narratives](../internal/worknarrative/service.go),
[history search](../internal/history/service.go),
[exact expansion](../internal/history/show.go),
[research work items](../internal/workitem/service.go), and
[the capture advisory](../internal/researchcapture/service.go).
The existing [narrative graph](narrative-graph.md) supplies the retrieval path;
separate evidence records were merged into narratives and should not be recreated
as a parallel store.

Narratives are redacted before persistence, limited to 32 KiB serialized and 64
claims. Index rows are derived from canonical records and rebuildable, including
after portable import. These are storage and context bounds, not measurements of
time saved or retrieval quality. Search returns snippets rather than full records;
its `bodies_loaded: 0` does not mean the search engine does no text processing.
Reported content-byte budgets also do not bound the complete JSON response envelope.

## Capture research with the current interfaces

1. State the question, task context and relevant scope, such as product version,
   platform or date. Use a research work item when the question needs an independent
   lifecycle; ordinary task research can stay anchored to that task.
2. Record the reusable result in a work narrative at a meaningful boundary. Use an
   `intent` claim for the question, `outcome` for findings, `verification` for what
   was checked, and `limitation` or `alternative` for uncertainty and rejected
   approaches. Keep each claim independently understandable and focused on one
   finding. Split substantial investigations into bounded narratives by question.
3. For external findings, include the supporting source title, publisher or author
   when known, direct URL, date accessed, relevant version/publication date and
   section or page in that claim's text. Distinguish a source's statement from the
   agent's inference. A bibliography in another claim may be lost when only the
   finding is expanded. This is a prose convention, not validated citation metadata.
4. Preserve contrary evidence, scope limits and what remains unknown. A successful
   fetch establishes what was retrieved, not the truth of the publisher's claim.
   Agent interpretations remain `caller_asserted`; explicit user choices belong in
   the decision ledger. Keep only useful, permitted excerpts or paraphrases; omit
   credentials, signed/private URL parameters and raw browsing transcripts.
5. Add exact task and other applicable anchors; use topic links when cross-task
   discovery warrants them. Keep a concise finding and its narrative reference in
   the checkpoint. When explicitly closing a research item as `answered`, link the
   findings and check that the target resolves: the current disposition validator
   requires target text but does not establish that it is valid evidence.
6. Retrieve through focused history search, then expand selected claim references.
   For consequential reuse, inspect the dedicated narrative view for supersession
   and source anchors, and recheck time-sensitive external facts. Append corrections
   with supersession; retain earlier claims as history.

Use actual URLs, not client-local search-result IDs that another session cannot
resolve. Keep the citation beside the claim even when several claims use the same
source. This duplication is a current workaround for missing citation edges.

The [capture advisory](checkpoints.md#research-capture-advisory) is only a reminder.
It recognizes selected tool names from redacted observations, without tool results.
Its threshold is six discovery operations across two kinds or ten of one kind.
One task-linked durable record, one supplied evidence reference, or two structured
checkpoint signals can mark the interval `covered`. None proves that external
research was cataloged; unobserved or differently named tools can escape detection.

## Proposed improvements and acceptance checks

The following changes would support a stronger promise of cataloged, contextualized
external research. Extend the existing narrative and retrieval model, without a
required model, crawler, daemon or new parallel findings store.

| Priority | Proposed change | Acceptance evidence |
| --- | --- | --- |
| 1 | Preserve correction and provenance state through history search and exact claim expansion. Surface supersession and canonical source anchors; make historical versus current selection explicit. | Searching and expanding a corrected claim names its successor; old claims remain accessible; bounded reads retain attribution and mark omitted detail. |
| 1 | Add reusable external-source identity and dated observations, with claim-level `supports`, `contradicts` or `context` citations. Preserve supplied URL, title, publisher, access time, version/date and locator; represent unknown fields honestly. | Two claims can cite one source and distinct sections; a changed source observation preserves the earlier one; broken or cross-project citation targets are rejected; redaction and portable round-trip pass. |
| 2 | Make the agent capture workflow explicit in the narrative/checkpoint skills and validate structural research completeness. | A web-research fixture with a conclusion but no source receives a specific advisory; one unrelated record cannot imply source coverage. An agent must still judge usefulness and truth. |
| 2 | Link question, findings, contrary evidence and synthesis without requiring a whole report to recover one claim. Validate `answered` targets and preserve them in the retrieval path. | A fresh session can navigate question → finding → cited observation under a byte budget; the answer remains understandable when retrieved alone. |
| 2 | Qualify retrieval with naturally produced external research across independent sessions. | Record query/filter bounds, candidate and returned counts, bytes, truncation, additional source reads, latency and attributable usefulness judgments; include failed and stale-source cases. |

External observations should store selected redacted metadata and summaries by
default. If an authorized source snapshot is retained, hash and label those exact
bytes; a URL or timestamp alone cannot prove unchanged content. Canonicalization
must not silently equate different versions or remove meaningful URL parameters.
Source text remains untrusted evidence and grants no instruction or execution
authority. Retrieval must preserve disagreements rather than invent consensus.

Embeddings, automatic tagging and model summaries are optional later experiments
if measured retrieval failures justify them. They are not prerequisites for useful
research memory, and they cannot replace citation integrity or freshness handling.

## Audit verification

The existing `internal/history`, `internal/worknarrative`,
`internal/researchcapture`, `internal/workitem` and `internal/portable` test suites
passed during this audit. They cover constituent storage/retrieval behavior; this
was not an end-to-end external-web research qualification or a performance trial.

The source review found that history search includes superseded narrative claims
and history show does not label their state, although dedicated narrative show
does. Narrative anchors have no external-source kind and resources are limited to
hosts, repositories and services. Claim citations can be written in text but are
not validated or traversed as structured source relationships. These are open gaps,
not completed features. See [retrieval qualification](evidence-qualification.md)
for the existing measurement discipline.
