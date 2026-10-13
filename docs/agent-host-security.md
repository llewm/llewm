<!-- continuity:roadmap-node llmem-agent-host-security -->
# Agent host interoperability and security

Status: accepted direction for bounded research and contract design, recorded
2026-09-18. This document does not authorize command execution, host installation,
traffic interception, or security enforcement.

## Product thesis

Llewm can become a third-party control layer for agent harnesses: preserve durable
continuity, expose attributable activity, compile explicit policy, and eventually
broker execution into constrained environments. The useful product promise is not
"trust the agent." It is:

> Know what an agent attempted, what policy applied, what was approved, where it
> ran, what the operating system allowed, and what result came back.

"SELinux for agents" is a useful shorthand for this direction, but Llewm should not
pretend to replace a kernel security module. Llewm can be the policy decision,
approval, evidence, and orchestration plane; containers, virtual machines, operating
system sandboxes, Linux security modules, and network controls remain the enforcement
points.

## Where Agent Host Protocol fits

The [Agent Host Protocol](https://microsoft.github.io/agent-host-protocol/)
(AHP) is a state-synchronization protocol between a portable agent host and its
clients. The host owns authoritative session state, sequences actions, supports
reconnection/replay, and exposes URI-addressed channels for sessions, chats,
terminals, changesets, automation, telemetry, and other resources. The upstream
project publishes a Go client and a multi-host client, which fits Llewm's current
implementation language.

AHP complements rather than replaces other layers:

- AHP coordinates multiple clients over host-owned sessions and ordered state.
- An agent protocol such as ACP can connect the host to an individual agent.
- MCP exposes tools and context to the agent or host.
- Llewm can consume normalized host state, preserve durable evidence, evaluate
  policy, and expose review or execution-control APIs.

The upstream AHP guidance explicitly leaves agent implementation, tool definition,
model routing, and agent-to-agent communication outside AHP. Therefore AHP is not a
sandbox or authorization system. Its terminal channel can expose structured command
boundaries, command lines, output, exit codes, and duration when shell integration is
available, but clients must check the advertised capability before relying on those
boundaries. Tool calls and non-terminal actions require separate coverage.

The historical design considered specification 0.6.0; that is not a current
version claim. Upstream negotiates a shared protocol version at initialization;
see the [specification overview](https://microsoft.github.io/agent-host-protocol/specification/overview.html).
Qualify the selected version behind an adapter and capability matrix. Do not make
Continuity's database schema mirror the protocol one-for-one.

## Layered architecture

```text
AHP clients
    │
    ▼
Agent host ── ACP/provider adapters ── agents
    │
    ├── AHP ordered state/actions ──► Llewm host adapter
    │                                  │
    │                                  ├── Continuity: durable observations,
    │                                  │   checkpoints, evidence, audit
    │                                  └── Action Control: policy, proposals,
    │                                      approvals, restrictions, receipts
    │
    └── execution requests ─────────► isolated execution broker
                                       ├── container/VM/worktree
                                       ├── filesystem/network/process policy
                                       ├── secret brokerage
                                       └── attributable results
```

Continuity remains non-executing. The diagram describes a future host/broker
architecture; no AHP adapter or broker is implemented. Current Action Control
only validates commit messages; a broader review/policy plane remains proposed. A future execution broker is a separate component with narrower
privileges than the interactive agent and with no direct mutation of Continuity's
SQLite tables.

## Honest guarantee levels

Every report and UI should identify one of three coverage levels:

1. **Observed.** Llewm received an event from a client, hook, AHP host, terminal, or
   operating-system sensor. The agent may have other unobserved paths. This is
   visibility, not enforcement.
2. **Brokered.** An action was submitted through an Llewm-controlled API and has a
   proposal, policy decision, approval state, execution environment, and result
   receipt. Other execution paths may still exist.
3. **Confined.** The agent process cannot bypass the broker for the claimed action
   class because operating-system, container, VM, credential, and network boundaries
   deny alternative paths. This is the only level that supports a strong prevention
   claim.

"Logged every command" is valid only for the scoped execution paths covered by the
claim. An AHP terminal stream does not prove that built-in file tools, MCP servers,
browsers, network clients, extensions, child processes outside the terminal, or a
second host were observed. Maintain a capability-and-coverage matrix per host,
client version, tool class, and enforcement configuration.

## Execution API shape

Do not add a generic shell tool to Continuity's MCP server. A future execution API
should be proposal- and capability-oriented. A request binds at least:

- actor, invocation, project, task, and session identity claims;
- declared action type and effects;
- executable plus structured arguments, working directory, and timeout;
- environment allowlist, with secrets represented by opaque broker references;
- workspace/volume mounts and read/write modes;
- network policy and destination allowlist;
- container image or VM identity and immutable digest where applicable;
- policy revision, approval scope, expiry, and stale-input guards; and
- validations and expected postconditions.

Execution returns an append-only receipt containing the resolved request hash,
effective policy, approval, environment identity, start/end times, exit status,
bounded redacted output references, observed effects, and verification results.
Changing any bound input invalidates the approval.

Prefer named capabilities such as `test`, `build`, `format`, `git-commit`, or an
accepted scriptlet over unrestricted command strings. A tightly confined terminal
may still be offered as an explicit high-risk capability, but confinement does not
turn arbitrary commands into low-risk actions.

## Container and operating-system boundary

The first executable profile should be disposable and least-privileged:

- one task per isolated worktree and rootless container or VM;
- immutable base image plus explicit writable workspace and temporary mounts;
- no host home directory, SSH agent, Docker socket, package-manager credentials,
  cloud metadata, or ambient secrets;
- default-deny outbound network with task-specific destinations;
- dropped capabilities, no privilege escalation, bounded processes/resources, and
  platform-appropriate syscall and filesystem controls;
- short-lived scoped credentials injected by a broker only after approval; and
- deterministic teardown plus retained redacted receipts.

Linux deployments may compose namespaces/cgroups with seccomp and an LSM such as
SELinux, AppArmor, or Landlock. Other platforms need equivalent native sandbox or VM
profiles. Containerization alone is not a security boundary if the agent can mount
the host filesystem, reach a privileged daemon, obtain broad credentials, or request
an unrestricted fallback.

## Audit and privacy boundary

Default collection should retain structured metadata and bounded results, not a
surveillance transcript:

- record tool/action type, normalized command structure, cwd, actor/session claims,
  policy and approval, timestamps, outcome, affected paths/resources, and hashes;
- redact before persistence and never capture stdin, credentials, or the entire
  process environment;
- treat stdout/stderr, file contents, browser data, prompts, and responses as
  sensitive payloads with explicit size, retention, and redaction profiles;
- preserve raw observations append-only while allowing attributed corrections; and
- make missing coverage and dropped/truncated data visible.

An enterprise profile may add tamper-evident or remotely anchored receipts, but
local-first operation remains the default. A compromised host can tamper with purely
local logs unless the trust model includes an external verifier.

## Delivery path

1. **Read-only AHP adapter.** Use the upstream Go client to connect to one local
   host, negotiate an explicit protocol version, subscribe to bounded root/session/
   chat/terminal/changeset state, and translate allowlisted events into existing
   attributable observations. No execution or approvals.
2. **Coverage report.** Show exactly which action classes are observed, unsupported,
   or ambiguous for that host/client/version. Validate reconnect, replay, duplicate
   delivery, ordering, redaction, and project isolation.
3. **Policy preview.** Map selected AHP tool/terminal actions to Action Control
   proposals and effective-policy reports without executing or blocking them.
4. **Isolated broker proof of concept.** Execute a tiny allowlist of repository-local
   actions inside a disposable rootless environment, with exact approval binding and
   receipts. Do not expose arbitrary remote execution or production credentials.
5. **Enforced host profile.** Only after threat-model and bypass testing, run an agent
   whose available execution paths are all brokered or OS-confined. Report the
   guarantee per action class rather than claiming universal confinement.
6. **Third-party API and multi-host operation.** Stabilize versioned APIs for clients,
   hosts, policy providers, and receipt consumers; then evaluate remote workers,
   enterprise controls, and additional host protocols.

A bounded read-only AHP adapter remains a future host-integration direction.
Current portable-memory and retrieval interfaces provide boundaries that such an
adapter could consume. The historical protocol review below is not current host
qualification; verify the selected version before implementation. Execution,
interception, sandbox installation and enforcement require separately reviewed
scope after threat-model and coverage work.

## Primary references

- [AHP specification overview](https://microsoft.github.io/agent-host-protocol/specification/overview.html)
- [AHP repository and Go client](https://github.com/microsoft/agent-host-protocol)
- [AHP and ACP layering](https://github.com/microsoft/agent-host-protocol/blob/main/docs/guide/ahp-and-acp.md)
- [AHP terminal channel](https://microsoft.github.io/agent-host-protocol/specification/terminal-channel.html)
- [VS Code Agent Host](https://code.visualstudio.com/docs/agents/concepts/agent-host)
- [VS Code agent sandbox boundary](https://code.visualstudio.com/docs/agents/run/agent-sandboxing)
