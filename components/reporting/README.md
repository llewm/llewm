# Private bug reporting

`llewm-report` is the separate Forgejo adapter for selected Llewm bug reports.
It consumes Continuity's v1 JSON report, keeps a redacted local outbox and
flushes it to a private issue tracker when explicitly invoked. It has no access
to Continuity databases, memsync, hooks, shells or models. The local bug remains
the source record; delivery and receipts belong to this adapter. Generic bugs
in an adopting project should stay in that project's tracker.

## Build

From this directory:

```sh
go fmt ./...
go test ./...
go vet ./...
go build -buildvcs=true -o ../../dist/llewm-report ./cmd/llewm-report
```

Preview binary bundles include this optional executable beside `llewm`.
Installation does not activate reporting.

## Opt projects in

Create a device-local configuration file outside tracked project files:

```json
{
  "repository_url": "https://forge.example/owner/llewm",
  "allowed_project_ids": ["PROJECT_ID_FROM_CONTINUITY_PROJECT_SHOW"],
  "automatic_submission": true,
  "token_env": "LLEWM_FORGEJO_TOKEN",
  "outbox": "outbox"
}
```

Run `llewm --project-root /path/to/project project show` for each adopting
project. Add its permanent ID only after its owner authorizes automatic private
Llewm reporting. The outbox path is relative to this file, or can be absolute.
Do not derive the target from each reporting project's origin: that would send
product bugs to the wrong repository.

`automatic_submission: true` permits an agent to invoke delivery without
reviewing every report within this source/target scope. It starts no daemon or
hook and grants no public-submission permission. Project IDs are caller claims,
not cryptographic proof of origin; capture must remain accountable.

Provide a repository-restricted Forgejo token through the named environment
variable using your credential manager. Do not put it in this file, command
arguments, project memory or reports. It needs repository-read and issue-write
access appropriate to the instance version. See
[Forgejo API usage](https://forgejo.org/docs/latest/user/api/usage/) and your
instance's `/api/swagger` for its exact API and token requirements.

Before flushing, the adapter checks that the repository is private, unarchived
and has issues enabled. Public repositories are refused. Visibility can change
after the check; this is a checked delivery policy, not a server privacy lock.
If Llewm becomes public, use a separate private intake repository.

## Agent and human workflow

1. Capture a local `bug` for observed Llewm misbehavior, retaining source
   authority and reproduction. A tool error alone does not establish a product bug.
2. Prepare the selected report through CLI or MCP. Expected behavior and a
   hypothesis are report-only supplements; neither changes the source bug.
3. Queue only that artifact. In an opted-in own project, the agent can flush
   under the configured automatic-submission permission.
4. Retain the receipt. A failed request is pending, not delivered; retry the
   same queued report when connectivity returns.

```sh
llewm --project-root /path/to/project item report work_item:BUG_ID \
  --expected "The fresh session receives the task bootstrap" \
  --hypothesis "The client may not have approved its hooks" \
  --output json > selected-report.json

llewm-report queue --config /path/to/private-reporting.json --file selected-report.json
llewm-report deliver --config /path/to/private-reporting.json
llewm-report status --config /path/to/private-reporting.json
```

Use a private scratch directory and remove the selected artifact after queueing;
the outbox retains the redacted copy. Never include a live database, transcript,
entire environment or memory export. Pattern-based redaction cannot prove that
free text is safe to share. The owner must choose what content the automatic
permission covers. The ecosystem's user-reviewed public channel is not enabled.

## Receipts and retry recovery

Queue identity hashes the redacted payload and exact target. Requeueing reuses
the entry. Issue bodies carry a source marker derived from project ID and bug
reference, plus an exact report marker. Revised reports for that source become
comments; unrelated source bugs are not automatically merged. Receipts include
the issue, URL, time and `created`, `attached` or `recovered` outcome. Recovery
means a marker was observed, not that the original submission was witnessed.

Ambiguous responses and failed receipt writes leave reports pending. The next
flush checks issue/comment markers before writing. Scans are bounded to 100
issue pages and 100 comment pages per source; exhaustion refuses delivery.
Pagination respects next-page and total-count metadata. Separate machines can
still race to create an issue; this does not guarantee exactly-once distributed
delivery. Similarity-based duplicate proposals and maintainer triage are later.

Queue and receipt updates are staged, synced and renamed into place; operations
share `.delivery.lock`. After a crash, verify the old process
stopped before removing that file. Removing a project's opt-in stops pending
delivery; changing the target never silently reroutes old reports. Outbox files
and receipts are device-local and excluded from memsync.

This initial adapter is tested with an in-memory Forgejo API fixture. Live access,
repository privacy and instance compatibility need qualification before live
delivery can be claimed.

## Console delivery snapshot

`status` emits a v1 object with `schema_version`, `observed_at`, `authority` and
`reports`, replacing the initial unversioned array. Each report summary includes
its project ID, source reference, exact report ID, target, queued time, status
and optional receipt. Report bodies, config and credentials are absent. The
snapshot is limited to 1,000 summaries and 1 MiB; use `status --project-id PROJECT_ID --config FILE` to narrow larger outboxes
before console inspection.

Save this private device-local JSON and open `llewm tui --report-status FILE`
to inspect only the current project's reports. The console labels the observation
time and reads no adapter state directly. Export again and refresh the console
to observe changes. Neither status nor console inspection submits anything.
See [console navigation](../continuity/docs/console.md).

## Pre-release executable and marker rename

The executable and command directory are now `llewm-report`; rebuild/install it
and remove the identified old executable separately. Configuration, report IDs,
outbox entries and delivery receipts are retained. A token environment variable
is explicitly named in configuration; its existing spelling continues to work
without an alias or automatic credential change.

New deliveries use `llewm-source` and `llewm-report` markers. If an issue or comment
matches the selected report through an old `llmem-source` or `llmem-report`
marker, delivery refuses before posting. A maintainer must review and convert the
exact markers in the affected issue body and comments while retaining their IDs
and content. The adapter does not edit old issues automatically. Test both retry
recovery and updated-evidence attachment before resuming submission; the
memory-sync test box's disposability does not apply to the reporting tracker.
