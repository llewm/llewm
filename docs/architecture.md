# Llewm architecture

## Repository boundaries

Llewm uses one Git repository with components under `components/`. Each component
owns its implementation, tests, dependencies, and documentation. Continuity and
Action Control are independent Go modules, `github.com/llewm/llewm/components/continuity` and
`github.com/llewm/llewm/components/action-control`.
The independent `github.com/llewm/llewm/components/orchestration` module implements
the manual shared-work milestone: a separately started authenticated coordinator,
reviewed plan snapshots, claims, reassignment and stale-result reconciliation.
It consumes public Continuity plan JSON and owns its own SQLite store; provider
accounts and manually launched execution stay on each engineer's machines.
GitHub supplies the canonical public module paths; Forgejo remains the source
authority. Component version tags include their repository subdirectory, such as
`components/continuity/v0.1.0-preview.1`. See [source publication](source-publication.md).

Root documentation covers the project as a whole; root build tasks and CI invoke
component checks. There is no root `go.mod`. Root `go.work` coordinates local
development without replacing module-specific checks or independent dependencies.

## Component responsibilities

Continuity owns session bootstrap, checkpoints, the activity ledger and audits,
roadmap nodes/bindings/review routines, freshness observations, reviewed plans,
work items, narratives, decisions, bounded history reads and portable memory.
Procedures, resources, pins and standing grants preserve reviewed operational
context without executing it.
Its [architecture](../components/continuity/docs/architecture.md) describes the current
implementation. [Orchestration](../components/orchestration/README.md) is now a sibling
component for manual coordination. Automated execution and local LLM interoperability
remain future capabilities. No shared runtime or library has been created speculatively.

Continuity is a curated state and retrieval layer, not a transcript-replay or model
state restoration layer. Persistent records keep provenance and stable references;
bounded reads select only what a task needs now. Lifecycle hooks may trigger
freshness checks, reminders, or deterministic recovery reads, but they must not
capture and re-inject an entire pre-compaction conversation. Compaction is allowed
to remove transient model context; Continuity's value is recovering the durable
facts without restoring that discarded bulk.

User-visible proposals are work items with exact task/checkpoint origin and typed
links. They do not capture hidden reasoning or become assignments, checkpoint
actions or execution grants merely by being accepted. Joined work views preserve
roadmap, task, checkpoint, action, plan and work-item status separately.
Explicit task lifecycle operations and saved return intent make tangents recoverable;
ambiguous session selection requires a choice. See
[track continuity](../components/continuity/docs/track-continuity.md).
The former separate suggestion store and interruption/return ledger were removed.
There are no roadmap authority modes or plan/generate/activate setup steps.

Persistent knowledge has distinct tiers: compact accepted current state; derived
retrieval metadata and summaries; detailed attributable evidence; and an optional
high-volume source archive. Derived records always point to stable sources and may
be rebuilt. Detailed or archived material is loaded only for explicit audit or
decision research, after deterministic filters have narrowed the candidate set.
Completing that research appends a new compact decision and evidence links; it does
not make the research session itself durable current context.

Checkpoint startup follows the same hierarchy. A compact task kernel is automatic;
structured checkpoint detail, historical evidence, and live source state are exact,
on-demand expansions. Readiness checks must account separately for mandatory
guidance and optional checkpoint detail so a budget failure cannot silently weaken
instruction precedence. This is multi-resolution context delivery, not a separate
retrieval subsystem.

The operational SQLite database may live under the project root without being a
Git artifact. The Local tier keeps project history local; optional History sync
shares curated immutable records through a dedicated managed memory repository.
Device-private events and per-request usage remain local. The machine registry
routes project/store locations without storing all project bodies; the old portable
metadata catalog was removed. Portable export, exact database backup and inert
Git-reviewed exports are distinct current interfaces. Object/peer transport is
future work; see
[project memory](project-memory.md) and
[federated project memory](federated-memory.md). Multi-repository projects, opt-in
History sync through a dedicated memory repository, and lease-based coordination are
defined in [multi-repository projects and sync](multi-repo-and-sync.md). Continuity does not silently edit
a roadmap or execute a routine merely because it can report either.

Future consumers should use explicit, versioned interfaces rather than import
Continuity's internal packages or mutate its SQLite tables directly. Cross-component
changes can be reviewed together; components can still produce separate executables
and releases. Consider separate repositories only when ownership, permissions, or
release independence warrants the coordination cost.

## Agent host and execution-control boundary

The manual orchestration coordinator authenticates engineers and serializes
assignment epochs, accepted plan revisions and attempt claims. A reassigned or
expired attempt cannot resume with publication authority. Its late contribution
is retained for explicit adoption or rejection, with original attribution; it
never overwrites accepted results. Plan transfers preserve completed nodes and
explicitly assigned children. This authority applies to the coordinator's records,
not to an offline agent process or a source repository. The versioned HTTP/CLI
interface performs no generic execution or automatic merge.

The first milestone supports one authoritative coordinator per team project,
manual claims and source-plan imports. An import is a proposed execution snapshot
that needs coordinator review; collaborative revisions and source-plan changes
require explicit reconciliation. Continuity writeback, automatic runners, provider
budget enforcement and team deployment are not implemented. The coordinator is
optional for Continuity users and must not share its database through memory sync.

[Agent Host Protocol interoperability](agent-host-security.md) is a proposed host
integration. Qualify its exact version behind an adapter; protocol state must not
become Continuity's storage schema. No AHP adapter is part of the current MVP.

The layers remain separate:

- an agent host owns live sessions and normalized client-facing state;
- Continuity ingests allowlisted, redacted observations and preserves durable
  checkpoints, evidence, and audit records;
- a future Action Control policy/approval layer evaluates proposals and restrictions
  (the current component only validates commit messages); and
- a future execution broker launches approved actions inside an isolated worktree,
  container, VM, or native sandbox and returns attributable receipts.

A future coordination layer may also retain expiring, append-only agent presence
claims over roadmap nodes, paths, and symbols, then traverse reviewed roadmap
bindings and observed work-episode change sets to surface possible overlap. These
claims are advisory and cannot establish ownership or exclusion. Continuity may
recommend isolated worktrees; only a separately authorized host or broker may
create them or enforce an execution boundary. See
[roadmap provenance and coordination](../components/continuity/docs/roadmap-provenance-and-coordination.md).

Project-management interoperability follows the same separation. A third-party
adapter may call a versioned Llewm semantic interface with external references,
snapshots, proposals, events, plans, and receipts. The adapter—not Continuity
core—owns provider SDKs, credentials, network access, webhook delivery, and native
field translation. Initial adapters run out of process and call explicit local
file/stdio, CLI, or MCP surfaces; an HTTP service or supervised adapter host is
optional future infrastructure. Capability discovery never grants permission, and
provider writes still require an exact reviewed plan and separately authorized
execution. See the contract's
[integration boundary](../components/continuity/docs/roadmap-provenance-and-coordination.md#provider-neutral-project-management-integration).

AHP ordering and terminal command detection improve visibility but are not an
enforcement boundary. Strong claims require the agent to lack an alternative path
around the broker, including direct terminals, built-in file tools, MCP tools,
network clients, credentials, and privileged host services. Each integration must
publish a coverage matrix rather than imply that one event source observes every
action on the device.

## Planned action-control boundary

[Action control](action-control.md) is an implemented validator sibling.
Continuity retains attributable history and guidance; its former policy compiler
and incident/restriction stores were removed. Reviewed execution and hook
installation are not present. Future restrictions
must distinguish recorded policy from an externally enforced capability boundary.
See [history design](decision-history.md#agent-attribution-inspection-and-replay)
for shared action references, actor provenance, and non-destructive analysis runs.

## Project identity and instruction scope

The Git root identifies the user's project. Moving the executable's source into a
component does not change that identity. Project state lives in `.llewm/continuity/`;
legacy `.continuity/` stores use the explicit backup-gated layout migration. Existing
checkpoints and observations remain append-only at the original Git root.

The bootstrap compiler combines engineer-wide sources from the user's configuration
directory with project-root instruction files. See
[global guidance](../components/continuity/docs/global-guidance.md) for precedence,
source attribution, and budget behavior. This shares personal guidance, not databases.

For project instructions, the compiler discovers only root files; it does not
recursively load component instructions. Root guidance explicitly directs agents
to the relevant component contract before editing. Directory-scoped bootstrap
selection is future work, not a behavior implied by this reorganization.
