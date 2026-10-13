# Llewm vision

Llewm aims to make agent-assisted work durable across sessions, clients, and models.
Continuity is the first deliverable and the complete MVP: preserve attributable
context, report the saved project pulse and recurring routines, expose activity and
repeated work, and support bounded historical reuse without requiring a daemon,
cloud account, or local model. See [project memory](project-memory.md) for the
distinction between saved “what's next,” reevaluation, routines, and mined procedures.

The durable asset is curated project knowledge, not a model's full conversational
state. Models may discard transient reasoning and conversational cruft at session or
compaction boundaries. Continuity should retain the attributable facts, decisions,
constraints, attempts, verification, and source links worth recovering, then retrieve
only the smallest relevant set for the next action. Replaying pre-compaction context
wholesale would defeat that purpose and can make continuation less accurate by
crowding current evidence out of the context window.

The larger project will extend into human-reviewed automation, agent/task
orchestration, and local LLM interoperability. Storage, model routing, execution,
and shared infrastructure may evolve as those capabilities become concrete.
These are future directions, not implemented features or commitments to specific backends.

Llewm should also become a third-party transparency and control layer around agent
hosts. Open host protocols can supply normalized session, tool, terminal, and
changeset state; Llewm can preserve attributable evidence, compile policy, and
eventually broker approved work into isolated environments. The goal is portable
oversight across harnesses, not another closed agent runtime. See
[agent host interoperability and security](agent-host-security.md).

Continuity's current restriction against arbitrary execution is a component MVP
boundary. Future automation should have explicit approval and execution boundaries
and report results back as structured observations. Local models should be optional
for users who only need Continuity.

Security claims must distinguish best-effort observation, actions submitted through
an Llewm broker, and actions that an operating-system boundary actually prevents an
agent from bypassing. Containers and host-protocol event streams improve visibility;
neither alone proves confinement.

Start with evidence: measure repeated discovery, failed attempts, verification,
usage, and recovery overhead before choosing what to automate or delegate.
See the [Continuity product context](../components/continuity/docs/product-context.md)
for the original user problem and MVP decisions.
