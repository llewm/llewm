# Work queues for existing agent sessions

Keep the backlog in ordinary reviewed Continuity plans. A coordinator plan is an
explicitly reviewed execution snapshot pinned to a source revision, not a second
roadmap or a special Windows plan type. The optional stdio MCP adapter lets an
already-running agent discover, claim and report multiple jobs. Neither service
launches agents, runs commands, accesses provider accounts, installs software,
merges commits or publishes source. The agent client owns execution.

## Prepare explicit routing

Export a full accepted/active plan through Continuity's public interface. Run
`llewm-orchestrate prepare --file SOURCE.json --plan windows-work --request-id
initial-import --job-file ROUTING.json`. Preparation is local and performs no
network operation. Its proposal requires coordinator review and acceptance.

The optional routing file has this shape:

```json
{
  "schema_version": 1,
  "source_ref": "continuity:SOURCE_PROJECT/plan:PLAN_ID@7",
  "jobs": {
    "native-audit": {
      "os": "windows",
      "arch": "amd64",
      "capabilities": ["native-tests"],
      "priority": 10,
      "repository": "https://github.com/llewm/llewm.git",
      "base_revision": "commit:EXACT_REVIEWED_COMMIT",
      "allowed_changes": ["Run native tests in disposable fixtures; report failures"],
      "approval_required": ["Source edits, installation, permission changes and publication"]
    }
  }
}
```

The source reference must match the exported revision exactly. Unknown item IDs,
completed items, unknown fields, invalid requirements and oversized payloads are
refused. Unmapped items remain manual-only. This explicit projection keeps job
metadata out of Continuity's storage schema and does not infer routing or
permission policy from prose. Source dependencies, evidence, completed work and
blockers survive import. Routing and scope become part of the reviewed execution
revision; workers cannot replace them with a claim.

Priority is 0 (first) through 100 (last); ties use plan ID and source position.
Dependencies require reviewed completion. Assignment must be open or name the
authenticated engineer. Capability and machine labels are routing assertions,
not authentication, hardware attestation or OS confinement. The MCP adapter uses
its actual process OS/architecture and only capabilities configured at startup.
Local permissions and user instructions remain authoritative for execution.

## Configure the client deliberately

Use one authoritative coordinator database, reachable by operator and worker.
Never replicate its active SQLite state through History sync. Remote access needs
HTTPS with trusted certificates or an existing secure tunnel to a loopback
listener. Redirects, URL credentials and cleartext non-loopback endpoints are
refused. `--ca-file PUBLIC_CA.pem` adds trust only to this client; it never disables
hostname/chain verification or changes OS trust. A CA bundle must not contain a
private key. Provision the private engineer token through a separate secure
channel, never Git, Continuity, chat, command arguments or logs.

A received file can inherit ordinary Downloads permissions. Import it before
client registration with `llewm-orchestrate credential-import --file RECEIVED
--token-file NEW_PRIVATE_PATH`. This creates and verifies a new private file;
it refuses overwrites and leaves the source untouched. After authentication
succeeds, securely remove the transfer copy. Do not weaken the normal credential
checks or place the token value in a command or client configuration.

Register a separate MCP server through the coding client's supported manager,
reviewing the exact executable, endpoint, allowlist and permissions first:

```text
llewm-orchestrate mcp --endpoint https://COORDINATOR:9467
  --token-file PRIVATE_TOKEN_FILE --ca-file PUBLIC_CA.pem
  --allow-plan windows-work --capability native-tests
  --machine windows-workstation --agent claude-code
```

The example is one command split across lines for readability. Repeat
`--allow-plan` or `--capability` for additional reviewed values. The private token
is read from its file and is not a client configuration value or tool argument.
This adapter is separate from the Continuity MCP server; refreshing Continuity
alone does not enable it. Client registration and permission writes require the
normal human terminal review.

## Authorize a bounded work session

The adapter supplies MCP initialization instructions; the user must still
authorize the session's work and stopping conditions. For example:

> Work through ready, approved jobs in the configured plans within their reviewed
> scope and my local permissions. Inspect full accepted context before claiming.
> Renew attempts before expiry and publish concise progress. Pause an item needing
> input and move to independent work. Submit evidence for review, then take
> another eligible job. Stop when requested or when no work remains after a
> bounded wait. Ask before expanding scope, installing, changing permissions or
> publishing anything.

Tools: `work_info`, `work_queue`, `work_plan`, `work_claim`, `work_renew`, `work_release`,
`work_pause`, `work_checkpoint`, `work_submit`, `work_review`, `work_unblock`.
Review and unblock require reviewer/assignment permissions at the coordinator;
listing those tools grants no permissions. Every call is restricted to the
startup plan allowlist. No tool accepts credentials or generic shell commands.

Discovery never claims work. `work_queue` accepts a cancellable wait of 0..20
seconds. There is no background daemon or push notification: the agent must call
tools again to continue, and a finished session does not restart for new jobs.
Retry uncertain mutations with the same request ID and identical input. Refresh
after a conflict, reassignment or old receipt instead of inventing authority.

Results await review without holding up independent jobs. A pause records the
blocker and releases the attempt; an assignment-authorized operator clears the
blocker with a reason, then the worker claims a new attempt. Old attempts cannot
be revived. Reassignment fences publication authority, not offline processes;
late contributions remain available for explicit reconciliation.
