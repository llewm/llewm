# Shared agent work

`llewm-orchestrate` is the first manual orchestration milestone: engineers share
reviewed plans and a work pool, run their own agents using their own provider
accounts, and return attributable context and results. An engineer with assignment
permission can transfer unfinished responsibility without losing completed work.

The coordinator is an explicitly started Go service with its own private SQLite
database. Use one authoritative coordinator database per team project. It supports
multiple engineers, machines and concurrent agents over a versioned HTTP API.
Continuity remains independently usable without this service.

This milestone coordinates manually launched work. It does not launch agents,
create worktrees, hold provider credentials, merge commits, or enforce spending
limits. There is no automatic runner, provider quota pooling, push notification,
or production deployment installed by building it. Workers poll `show` and call
`resume` before continuing; offline work is reconciled on submission.

## Build and start a coordinator

From this component:

```sh
go build -o ../../dist/llewm-orchestrate ./cmd/llewm-orchestrate
go fmt ./...
go test ./...
go vet ./...
```

From the repository root, using the built executable:

```sh
dist/llewm-orchestrate init --db .llewm/orchestration/team.db
dist/llewm-orchestrate member --db .llewm/orchestration/team.db \
  --id dave --permissions plan,accept,assign,review,work \
  --token-file .llewm/orchestration/dave.token
dist/llewm-orchestrate member --db .llewm/orchestration/team.db \
  --id alice --permissions plan,work \
  --token-file .llewm/orchestration/alice.token
dist/llewm-orchestrate member --db .llewm/orchestration/team.db \
  --id jerry --permissions plan,work \
  --token-file .llewm/orchestration/jerry.token
dist/llewm-orchestrate serve --db .llewm/orchestration/team.db
```

Initialization refuses to overwrite an existing database. The database directory
must be private (0700), and database and credential files private (0600). Member
credentials are written to new files and never printed; only their hashes are
stored in the coordinator. Distribute each credential through your team's secure
channel, separately from source and project memory. An engineer may use that
credential on multiple machines; do not share it between engineers.

Re-enrolling the same engineer rotates the credential and replaces permissions.
Use a new output file. `revoke --db PATH --id alice` disables the credential;
reassignment alone does not revoke membership. Database filesystem access is an
operator trust boundary: membership administration is a local CLI operation and
is not exposed through the API.

The default listener is `127.0.0.1:9467`. For another machine to connect, explicitly
configure a reachable listener and `--tls-cert PATH --tls-key PATH`, with a
certificate trusted by its client. Public listeners without TLS are refused.
An existing authenticated tunnel can instead expose the loopback listener locally.
The client permits cleartext only to literal loopback IPs and refuses redirects.
Tests qualify local HTTP behavior and TLS requirements, not a deployed team network.

## Permissions and attribution

All enrolled engineers may read this project's plans, context and history. The
following permissions are independent:

| Permission | Allows |
| --- | --- |
| `plan` | Propose a new plan or collaboratively revise a draft |
| `accept` | Accept the latest plan proposal and release its scope for claims |
| `assign` | Assign or reassign a whole plan or one item |
| `work` | Claim eligible work, resume/release an attempt, upload context and results |
| `review` | Accept current results, adopt stale results, or reject submissions |

An assignment never starts an agent. A worker explicitly claims an item using
their own authenticated credential. The engineer comes from authentication;
machine and agent labels are caller assertions. Review means an authorized
engineer attested to verification, not that the coordinator executed the tests.
These permissions are project-wide in this milestone; there are no per-plan
read ACLs or per-device credentials yet.

## Start from a Continuity plan

Export the full operative accepted revision through Continuity's public JSON
interface, then prepare a proposal for a new coordination plan:

```sh
llewm track-plan show PLAN_ID --revision REVISION --detail --output json > source-plan.json
dist/llewm-orchestrate prepare --file source-plan.json \
  --plan shared-plan --request-id initial-import > proposal.json
dist/llewm-orchestrate request --endpoint http://127.0.0.1:9467 \
  --token-file .llewm/orchestration/dave.token --file proposal.json
```

`prepare` performs no network operation. It reads schema-v1 exported JSON, retains
stable item IDs, dependencies, acceptance criteria, progress, blockers and evidence
references, and emits a redacted proposal. Completed items require evidence and
remain completed after the coordinator's reviewer accepts that baseline. Imported
blockers stay blocked until a reviewed revision removes them. Proposals and
outlines are refused. Hierarchical or superseded source items require an explicitly
reviewed flat execution projection; the importer refuses to silently flatten them.

The proposal names the exact source project, plan and revision. A source snapshot
does not itself authorize execution. The coordinator reviewer must accept it.
This is an initial import, not a bidirectional sync adapter. Subsequent team plan
edits are reviewed execution-scope revisions here; source-plan changes must be
deliberately reconciled, and source progress is not silently written back. Node or
track scope may be named through `source_ref` and per-item `context_refs`.

Alternatively, propose a plan directly with this command JSON:

```json
{
  "schema_version": 1,
  "request_id": "plan-draft-1",
  "action": "propose",
  "plan_id": "shared-plan",
  "base_version": 0,
  "definition": {
    "title": "Deliver the feature",
    "source_ref": "roadmap_node:feature@1",
    "context": "Relevant decisions and constraints for the team",
    "items": [
      {
        "id": "implementation",
        "title": "Implement the feature",
        "detail": "Scope and interfaces agreed by the team",
        "acceptance_criteria": "Feature tests and integration checks pass",
        "depends_on": [],
        "context_refs": ["decision:feature-design"]
      }
    ]
  }
}
```

## Manual work cycle

Send each JSON command with `request --endpoint URL --token-file PATH --file FILE`.
Every command includes `schema_version: 1`, a new `request_id`, `plan_id`, and
`action`. Preserve the same body and request ID to retry an uncertain upload;
the coordinator returns the original receipt without duplicating work. A key
cannot be reused with changed input. Refresh after a retry: an old receipt records
the original outcome, not current authority.

Read `show --endpoint URL --token-file PATH --plan shared-plan`. Its `plan.version`
is the `base_version` for the next authoritative mutation. The receipt gives the
new version. Concurrent mutations with an old version fail; refresh and reconcile
before using a new request ID. This comparison includes progress uploads, so a
busy plan may require retries even when engineers work on different items.

| Action | Additional command fields | Effect |
| --- | --- | --- |
| `propose` | `base_version`, complete `definition` | Append a proposal; retain existing accepted scope |
| `accept-plan` | `base_version`, `revision` | Accept the latest proposal; invalidate attempts on older scope |
| `assign` | `base_version`, `engineer`, `reason`; optional `item_id` | Advance assignment epoch; empty engineer opens work to eligible members |
| `claim` | `base_version`, `item_id`, `machine`, `agent` | Reserve one ready item and return `attempt_id` |
| `resume` | `attempt_id` | Check current authority and renew a valid attempt for 30 minutes |
| `release` | `attempt_id` | Release a valid attempt so the item can be claimed again |
| `submit` | `attempt_id`, `kind`, `context`, references | Append context or a result, retaining stale contributions |
| `review` | `base_version`, `submission_id`, `decision`, `reason`, `verification_refs` | Accept/adopt a result or reject it |

For example, accepting the first proposal:

```json
{"schema_version":1,"request_id":"accept-1","plan_id":"shared-plan","action":"accept-plan","base_version":1,"revision":1}
```

An empty plan assignment is an open pool. Claim eligibility requires `work`
permission, no unfinished dependencies or explicit blocker, and either an open
assignment or assignment to that engineer. Only one current attempt can hold an
item. Different items can run concurrently on different agents and machines.
Exclusion is per plan/item; overlapping source scope across separate plans is not
automatically detected. Review interfaces and source overlap when splitting work.
Plan ownership is inherited by items unless an item has an explicit assignment;
an explicit empty item assignee makes that item open even under an assigned plan.

Run each attempt in an isolated checkout using your own provider account. Publish
branches/commits through your normal source workflow. Submit references, not code
archives or credentials. Keep worktree and provider access under local control.

`submit` has `kind: "checkpoint"` for partial handoff or `kind: "result"` for
completion review. Both require concise `context`; results also require nonempty
`artifact_refs` and `verification_refs`. These are attributable claims, not fetched
or executed by the server. A checkpoint can include current findings, attempts,
blockers, branch refs and next steps. Only successfully uploaded context transfers.
Optional `usage` includes `provider`, `model`, `input_tokens`, `output_tokens` and
`confidence: "reported"` or `"estimated"`. Omit it for unknown usage. Counts are
retained separately, never totaled across confidence classes or sold as capacity.

Reviewers check the result and integration independently, then use `accept` for a
current submission, `adopt` to reconcile stale work, or `reject`. Accept/adopt must
cite verification against current criteria and integration. The coordinator marks
the item complete only after review; it never merges code. Combining partial work
means publishing a new result that references both contributions. Completed item
definitions cannot be changed or removed; represent rework as a new item.

## Reassignment and offline recovery

Dave assigns a plan to Alice. She completes three nodes and uploads a checkpoint
on a fourth. Dave can issue `assign` for the plan with Jerry as engineer and an
explanatory reason. Completed results and their authorship remain intact. Jerry's
plan read includes the partial context, references and attempts.

Plan reassignment transfers responsibility for unfinished inherited work. It
preserves explicit child assignments, even ones assigned to Alice. Transfer those
individually with `item_id` when intended. Completed items cannot be reassigned.
Every assignment operation advances its epoch, including assignment back to the
same engineer; old attempts do not regain authority through an Alice/Jerry/Alice
round trip.

On Alice's next `show` or `resume`, the coordinator explains that the assignment
changed and names the current owner. `resume` fails for her old attempt. A connected
manual worker must stop scheduling under it and preserve their partial work.
This service cannot stop an offline process or an uncooperative local agent.

If Alice discovers the transfer only while uploading, `submit` retains her work,
with her original attempt/revision/assignment attribution, and returns
`needs_reconciliation`. It neither changes ownership nor completes the item.
The reviewer must explicitly adopt or reject it. Submissions received before
reassignment are checked again at review time; a formerly current receipt never
bypasses a later transfer. Adopting work on an unfinished item invalidates any
other running attempt for that item. A completed item cannot be overwritten by
another submission. Redundant late work can be rejected with a reason.

Attempt expiry uses the coordinator's clock. Renew before the 30-minute deadline;
expired attempts cannot be revived. Claim a new attempt and recover from the saved
context. Offline results remain uploadable as stale contributions while membership
and `work` permission remain valid. A coordinator outage prevents new authoritative
claims, reassignments and renewals; it does not imply existing agents stopped.

## Protocol, audit and operating limits

- `POST /v1/commands`: the command schema above; actor identity is authenticated.
- `GET /v1/plans`: plan summaries.
- `GET /v1/plans/ID`: accepted/proposed revisions, assignments, attempts, handoffs,
  submissions and reviews; `ready` is caller-specific and `submissions` includes
  current reconciliation status.
- `GET /v1/plans/ID/events?after=N`: up to 100 redacted mutation events with actor,
  input, receipt and time. Continue from `next_after` until an empty page.
  CLI equivalent: `show ... --history --after N`.

API authentication uses `Authorization: Bearer CREDENTIAL`. Unknown JSON fields,
trailing values and bodies over 128 KiB are refused. Individual text values are
limited to 16 KiB, definitions to 256 items and a plan's retained state to 8 MiB.
Keep large artifacts outside the coordinator and split future scope into bounded
plans. This pilot returns full plan detail; it is not a high-volume scheduling
service. The immutable event ledger preserves prior assignments and mutations;
current plan state is a transactional projection. SQLite immediate transactions
serialize claims and prevent stale concurrent assignments across connections.

Only allowlisted structured fields are retained. Known credential patterns are
redacted before state, event or prepared-proposal persistence. Pattern redaction is
not a guarantee that arbitrary secrets can be recognized: submit curated context,
not transcripts, environment dumps, stdin or provider credentials. All members can
read this project; use another coordinator for a separate trust boundary.

The database is local to its coordinator host; do not copy an active database to
another independently writable coordinator or sync it through Continuity memory.
For a backup, stop the coordinator and other local writers, then preserve the
database and any SQLite sidecars together before restarting. Restore to one
authority. Automatic failover, multi-master sync, team deployment, detailed capacity
budgets, runner adapters, and automatic Continuity writeback are future work.

Automated tests cover the Dave/Alice/Jerry transfer with three completed nodes,
late adoption/rejection, preserved child assignments, plan collaboration, source
import, expiry, two-connection claim races, permissions, credential rotation,
idempotency, redaction, and CLI/HTTP end-to-end flows. They use disposable local
stores and do not spend provider tokens or change a live team deployment.
