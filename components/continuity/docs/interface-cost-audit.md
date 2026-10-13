# Interface cost and context budgets

Continuity keeps startup context and retrieval bounded. Detailed development
measurements remain in the private archive; they are not current performance
guarantees or evidence of causal token savings.

## Interface design

- Lifecycle hooks deliver the small bootstrap every session needs and record
  allowlisted observations.
- Skills describe a workflow when invoked; avoid duplicating tool schemas and
  large guides in their bodies.
- MCP provides typed reads and writes. Default plan reads return outlines,
  write tools return receipts, and repeated unchanged bootstrap reads return a
  notice unless explicitly expanded.
- CLI commands cover human-facing and infrequent maintenance work. Request
  bounded output and explicit JSON when a script needs it.

Clients choose whether and when tool descriptions and skills enter model context.
Tool-result byte counts cannot measure all schema, skill, or host-envelope costs.
A cached token still occupies context; a byte-derived token estimate is not a
provider-reported token count.

## Verification

The MCP definition-budget test checks serialized model-visible tool metadata
against its declared ceiling. Bootstrap checks cover the packet budget separately
from enclosing hook/MCP protocol overhead. These are deterministic source limits,
not promises about a client's total startup prompt.

Use [usage reports and audit-only comparisons](usage.md) to observe actual sessions.
Keep client/model/settings, starting revision, permissions, task outcomes, and
source coverage comparable. Separate exact usage from estimates and unpriced
tokens. A reduction in transport bytes does not establish lower task cost,
less rediscovery, or equal correctness.
