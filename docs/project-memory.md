# Project memory, routines, and portable state

Continuity preserves the smallest attributable state needed to resume work.
Current interfaces are deterministic and project-scoped; future automation must
keep separate approval and execution boundaries.

## Responsibility

| Question | Current surface |
| --- | --- |
| What should this session resume? | Bootstrap, exact task brief and checkpoint |
| What is planned or unresolved? | Work views, track plans, roadmap and checkpoint actions |
| Why was a direction chosen? | Decisions, narratives and bounded history search |
| What proposal should be retained? | Work items with explicit dispositions |
| What repeated failure was observed? | Derived friction report with unknown cause where appropriate |
| How can knowledge move or recover? | Portable bundles, optional History sync and exact database backups |
| What reviewed sequence can be recalled? | Inert procedures/runbooks, resources and scoped pins |

Separate reference-card and idea/suggestion stores were consolidated before
release. Use work items, narratives, decisions and pinned records rather than
retired card or idea command surfaces.

## Evidence depth and decision research

Current task state is the small resume kernel. Typed links and metadata locate
detailed attributable claims; explicit bounded reads expand selected evidence.
A research session should save its durable conclusions and source links, not its
entire search stream or hidden reasoning.

Raw transcripts are not canonical project state. Detailed diffs or tool results
may remain external source artifacts; the current narrative layer does not promise
to retain them all. A future high-detail source archive needs an explicit inclusion,
redaction, access, retention and size contract.

### Bootstrap budget and staged expansion

Checkpoint dry-run compiles the real JSON/Markdown startup packet and accounts for
guidance, kernel and optional detail. Save validates that payload again. Readback
confirms the exact checkpoint and next action. Optional omission is attributable;
a budget failure must not silently discard mandatory guidance.

Lifecycle events and reminders do not manufacture semantic checkpoints.
Compaction may discard transient model context. Recovery uses saved facts,
stable references and current Git evidence, rather than restoring the full
conversation. See [checkpoints](../components/continuity/docs/checkpoints.md).

## Deterministic project pulse

`llewm work` and `llewm brief task:ID` expose selected saved task state and
bounded routing information. They distinguish caller-reported verification from
machine-observed freshness and ambiguity. Retrieval does not reevaluate intent,
repair state or authorize the saved next action.

Explicit task selection wins; default selection uses the documented session,
branch and recency rules and may require a choice among recent active tasks.
Current Git/source checks remain necessary where the saved state may have drifted.

## Routines, procedures and observed patterns

Roadmap review routines define explicit due thresholds and reviewed scope.
A due result is an advisory, not a schedule that invokes a model or edits a file.

Procedures retain versioned checks or runbooks with parameters, working-directory
rules, steps, declared effects, validations, sources and review status.
Resources hold hosts/repositories/services without credentials; pins select bounded
records for project/node/task context. Standing grants express separately scoped
human permission tied to an accepted procedure revision. Changing that revision
can invalidate the grant.

Continuity stores and renders these records but never executes their steps.
Command normalization and procedure-candidate data are bounded foundations;
general mining of command sequences, generated task-file export and scriptlet
runners are not complete public capabilities.
See [resources and runbooks](../components/continuity/docs/resources-and-runbooks.md).

## Work items and explicit disposition

Capture ideas, bugs, tasks and research questions without changing the active task.
List/audit reads are bounded; disposition follows explicit reviewed choices.
Acceptance retains a direction, not proof of implementation or permission to run.
Links preserve integration/merge targets and original attribution.
See [work items](../components/continuity/docs/work-items.md).

## Storage profiles

### Private project-local runtime

The default store lives under the Git root's ignored `.continuity/`.
Configured paths and `CONTINUITY_HOME` can select another local location.
A project-local database is not a Git artifact or a mergeable collaboration file.

Use `llewm database backup` for a consistent integrity-checked snapshot.
Never copy only a live database file while WAL/transactions may be active.
Backup retention is explicit and previewable; export and backup serve different
purposes. See [installation and backups](../components/continuity/docs/installation.md).

### Portable curated history

Complete redacted bundles preserve immutable record identities, revisions,
authority and references. Inspect/plan are read-only; confirmed apply is
transactional and collision-aware. Raw events, sessions, per-request usage,
credentials, WAL/SHM files and derived indexes are excluded.

Optional History sync manages a separate memory repository after explicit setup.
It can then run automatically at the documented lifecycle boundaries.
The machine registry is routing metadata, not a shared semantic catalog.
See [federated memory](federated-memory.md) and
[memory repository rules](../components/continuity/docs/memory-repository.md).

### Reviewable exports and private archives

A Git-reviewed export creates an inert snapshot for explicit review without Git
or network operations. A separately created database backup may be transported
under a private recovery policy; no LFS setup is automatic or required.

This source project's original development history and notes are preserved in a
verified private archive before source-history cutover. That archive is independent
of the managed memory repository and never reconnects to the public source trunk.
See [source publication](source-publication.md).

## Future host and execution integration

Host protocols may supply allowlisted, redacted session/tool/change observations
through adapters. Provider SDKs, network delivery and credentials belong outside
Continuity core. Observation does not prove that every action was captured.

A future scriptlet runner or execution broker requires explicit resolved commands,
scope/approval binding, isolation, bounded output and attributable results.
Optional local-model summarization or analysis remains derived proposed knowledge,
not an instruction or autonomous action. See [agent-host security](agent-host-security.md).
