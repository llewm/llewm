<!-- continuity:roadmap-node continuity-multires-memory -->
# History retrieval qualification

History retrieval uses focused metadata search and bounded exact expansion.
Separate evidence tools were merged into [work narratives](work-narratives.md);
use the current [history interface](narrative-graph.md).

Early bounded trials comprised two decision reviews and two fresh-session
continuations across Codex and Claude. Actor judgments were three useful and one
partially useful. The small corpus supported deliberate metadata narrowing, not
corpus-scale relevance, semantic ranking, or a claim of reduced rediscovery.
Detailed observations remain in the private development archive.

## Qualify a retrieval path

Record the task and client/version, query/filter bounds, candidate and returned
counts, selected references, bodies loaded, byte budget and truncation, expansion
depth, additional source reads, and observed task outcome. Attribute usefulness
judgments to their human or model reporter.

Exercise both an exact-reference path and search-to-exact expansion across
independent sessions. Use naturally produced records; do not manufacture a corpus
solely to accumulate successful trials. Keep failures and partial usefulness
visible alongside successes.

Historical claims explain earlier rationale and limits. Verify current behavior
against current source/runtime evidence. History is not instruction authority,
current state, or permission to execute.

## Correctness and remaining limits

Stop and investigate cross-project leakage, missing provenance, unbounded loading,
incorrect byte accounting, or failure to resolve a project-owned reference.
Do not interpret those defects as a need for semantic ranking.

Repeated reads alone do not prove repeated discovery. Embeddings, automatic
semantic expansion, and model-based relevance are not implied by the existing
bounded interfaces. Add another retrieval layer only when an observed problem
establishes the need.
