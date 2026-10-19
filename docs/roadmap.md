# Llewm roadmap

Continuity is the complete current MVP. The public roadmap describes product
direction; exact maintainer task state and delivery plans live in private
Continuity records. A roadmap entry does not authorize execution.

## Current delivery

Prepare a clear public source release with maintained documentation, reproducible
developer builds, matching client packages, and explicit qualification limits.
Documentation cleanup precedes the clean publication root and first push.
Remote version-pinned installation follows publication; native macOS and Windows
installation, client integration, and console acceptance require observed runs.
See [source publication](source-publication.md) and the
[Continuity roadmap](../components/continuity/docs/roadmap.md).

## Implemented components

- **Continuity:** local SQLite storage, bounded bootstraps, structured checkpoints,
  lifecycle observations, narratives and decisions, history retrieval, work items,
  roadmap/plan views, usage auditing, optional History sync, and a read-only console.
  See [Continuity](../components/continuity/README.md).
- **Action Control:** a standalone versioned Git commit-message validator.
  Hook installation and action execution are outside its implemented slice.
  See [Action Control](../components/action-control/README.md).
- **Orchestration:** a manual coordination pilot with authenticated work claims,
  collaborative plan revisions, context-preserving reassignment and explicit
  stale-result review. Local fixtures qualify its CLI/API; team deployment and
  automatic execution remain separate work. See
  [Orchestration](../components/orchestration/README.md).

## Qualification and distribution

Automated checks and cross-builds establish source behavior and compilation.
They do not establish native runtime compatibility, live client delivery, or broad
switching reliability. Preserve failures as well as successful observations, and
state the exact version and execution mode each observation covers.

After native qualification, prepare signed public macOS and Windows binaries.
A thin VS Code interface is a later consumer of the same versioned read surfaces.
Ordinary bugs and qualification findings use public GitHub or Forgejo issues.
Neither signed installers nor an editor extension are currently offered.

## Future project capabilities

Human-reviewed automation, automatic agent orchestration, agent-host interoperability, provider
adapters, and optional local-model integration remain future directions. Their
execution and approval boundaries must be designed explicitly; Continuity itself
does not expose general command execution or require a model.

See [vision](vision.md), [architecture](architecture.md),
[agent-host security](agent-host-security.md), and [action control](action-control.md).
Create shared infrastructure only when implemented consumers establish its interface.
