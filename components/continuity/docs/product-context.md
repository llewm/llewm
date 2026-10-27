# Product context and decisions

## Origin

Cloud coding agents repeatedly lose operational and project context across session boundaries and sometimes during compaction. Users must tell new sessions to read autoload rules, re-explain sandbox restrictions, and watch agents rediscover the same build and deployment procedures. Large models also spend costly tokens supervising deterministic commands.

Llewm originally explored a broad local context-control plane with persistent evidence, vector retrieval, temporal graphs, local models, local/cloud escalation, and trusted execution. That broader architecture remains credible, but it is not the product wedge.

## MVP thesis

Reducing repeated prose discovery, project-doc clutter, and documentation rot is
a major product goal. Fresh sessions should recover the current task and relevant
plans without repeatedly hunting through prose. Maintaining operational state in
Continuity should reduce duplicate handoffs and plan copies while keeping useful
project docs. Evaluate this through measured discovery and maintenance costs and
successful continuation, rather than treating record capture as the outcome.

Treat cloud sessions as disposable reasoning processes. Durable project continuity belongs in a local, structured controller.

The controller should make re-sessioning nearly frictionless while keeping startup context small. It should capture enough activity to expose repeated work and estimate the cost of using premium models for deterministic procedures.

Continuity preserves the gold, not the entire stream. Durable records should retain
relevant history as it evolves: attributable facts, accepted decisions, constraints,
failed attempts, verification state, and links to authoritative sources. Retrieval
is the central product difficulty and value: select a lean, task-specific packet
that is accurate enough to act on and exposes stable references for deeper queries.

Compaction and session replacement are useful garbage-collection boundaries for a
model's transient reasoning. Saving and replaying full pre-compaction state would
counteract that cleanup, increase context pressure, and revive stale or irrelevant
details. Hooks therefore provide lifecycle signals and recovery opportunities, not
a second transcript or opaque model-state archive. A fresh session grounded only in
the relevant durable records is the strongest form of compaction.

It should also answer “what's next” from a bounded saved project pulse at zero model
cost, with provenance, freshness, and an explicit statement that the answer was not
reevaluated. Project-specific recurring routines are retained as versioned,
human-accepted definitions; observed command patterns remain separate procedure candidates.
See the project-wide [project-memory design](../../../docs/project-memory.md).

## Session lifecycle

A session bootstrap contains only:

- critical rules;
- current objective and status;
- current repository state;
- active decisions;
- relevant failed attempts;
- confirmed environment constraints;
- verification state;
- recommended next action; and
- opaque references for deeper context.

Use sticky sessions for coherent work episodes. Refresh targeted rules after drift signals and roll over at task boundaries or when continuation cost exceeds a clean bootstrap. Do not create a session for every interaction and do not periodically resend the entire harness.

Recovery should be query-led. Start with the bounded current checkpoint or project
pulse, follow typed links and exact references as needed, and stop loading when the
next action is sufficiently grounded. A lifecycle hook may request that retrieval or
warn that semantic state is stale; it must not infer missing intent from activity or
reload the conversation it replaced.

## Rules

Separate enforceable boundaries from semantic guidance. The MVP mostly handles semantic guidance and observation; it may include low-risk validation that naturally falls out of hook integration. Security-sensitive policy and remote execution are later work.

Rule usage labels must remain honest:

- loaded;
- retrieved;
- matched;
- applied by a validator;
- violated; or
- influence unknown.

## Activity and economic visibility

Capture provider-neutral structured events for lifecycle changes, tool calls, commands, file activity, builds, tests, Git operations, errors, retries, verification, instruction loading, user corrections, model changes, and compaction.

Audit categories include:

- reasoning;
- context reconstruction;
- retrieval;
- deterministic execution;
- output transport;
- repair overhead;
- verification;
- user correction;
- re-session overhead; and
- potentially automatable work.

Provider-reported token usage is exact. Locally tokenized or character-derived usage is estimated and must carry source and confidence. Preserve the price-catalog version used for monetary estimates.

Timestamps are inexpensive exact observations and should be retained for session,
operation, tool, retrieval, retry, compaction, and time-to-first-productive-action
analysis. They reveal latency, repetition, recovery overhead, and idle gaps, but do
not substitute for token or price data. Retrieval reports should eventually expose
records considered and loaded, bytes, estimated tokens, expansion depth, and whether
the retrieved context contributed to a completed or corrected action. Optimize for
continuation quality per cloud token while keeping locally derived estimates clearly
separate from provider-reported usage.

High-detail history may be useful for human audit, deliberate decision research, or
an explicitly approved future training export. It is not normal session context.
Deterministic filters should narrow by task, time, commit, path, record type, and
typed links before an optional local model summarizes, tags, deduplicates, clusters,
or reranks candidates. Local-model products remain attributed derived proposals;
accepted current state is updated only after review.

## Procedure discovery

Detect repeated normalized action sequences and propose named procedures only after considering frequency, recency, sequence consistency, verified success, failures, user corrections, environment similarity, and documentation agreement.

Repetition alone is not correctness. A candidate becomes a durable runbook only after review. Later export targets include shell, Markdown, `justfile`, Taskfile, Make, PowerShell, and Ansible.

The initial motivating procedure is a recurring development deployment resembling:

```text
SSH to development host
→ change to repository
→ inspect/update Git state
→ run Docker Compose with numerous development flags
→ verify container health
```

The user wants this visible, auditable, available offline, and eventually replaceable with a near-free script rather than repeatedly delegated to an expensive model.

## Future architecture, explicitly deferred

Potential later components include:

- PostgreSQL plus pgvector for shared durable evidence;
- relational graph tables, with Apache AGE evaluated as a first-class PostgreSQL graph option;
- optional external graph projection only after benchmarks;
- local embeddings, reranking, or local models;
- typed execution capabilities and credential brokerage;
- isolated build/deployment workers;
- sanitized screenshots and browser automation;
- task-specific containers, microVMs, and declarative promotion.

For sandbox experimentation, promote only reviewed declarative artifacts—not command history or mutated runtime state. A cloned production environment is not automatically private and must not contain unrestricted real secrets or data.

## Product positioning

The initial product is:

> A local continuity and activity controller for coding agents that preserves task state, verifies instruction loading, exposes what agents repeatedly do, and turns successful recurring work into durable, human-usable procedures.

The product should work without a cloud account, database server, local model, or container runtime. Broad adoption should come through Codex and Claude Code plugins/hooks plus generic MCP, with deeper API/client integrations later.

`llewm log` shows Git commit metadata with current commit-linked Llewm narratives,
using the same section rendering as narrative-generated Git notes. It works when
notes were never written; commits without a narrative show their Git metadata
only. Use `-n` to limit commits, an optional revision to choose the starting
commit, and `--repo` to select a workspace member. Git notes are not read by this
command. Narrative recording and note writing are unchanged.
