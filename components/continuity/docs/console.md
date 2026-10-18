# Continuity console

Run `llewm tui` in an initialized project, or select a project explicitly:

```sh
llewm --project-root /path/to/project tui
```

The console reads existing storage without initializing, migrating, rebinding,
changing the selected task, or submitting reports. Browsing a handoff does not
resume its task or authorize its next action. Redirected or unsupported terminals
get an actionable CLI fallback. The minimum terminal size is 60 columns by 12 rows.

## Navigation

Tab and Shift-Tab move between views. The active tab and nearby destinations stay
visible in narrow terminals; number keys jump directly to a view.

| Key | View | Open a selection |
| --- | --- | --- |
| 1 | Tracks | Right opens history; `p` toggles the plan; `c` opens task checkpoints (a task picker when several are bound) |
| 2 | Timeline | Right follows the event's saved reference |
| 3 | Decisions | Right opens links, with rationale and authority retained |
| 4 | Work items | Right opens the saved item; `b` previews a selected bug report |
| 5 | History | Right opens a curated topic; `/` searches saved history |
| 6 | Project | Identity, checkout, database, workspace members and observed locations |
| 7 | Checkpoints | Paged project handoffs; Right opens the record, predecessor, task and evidence links |
| 8 | Health | Source, installed executable, schema, client setup warnings and unmet readiness gates |
| 9 | Releases | Saved release revisions and links to roadmap and narrative evidence |
| 0 | Delivery | Supplied reporting metadata and receipts |

Arrow keys and Home/End select rows. Right, `l`, or Space opens a selection;
Left, `h`, Backspace, or Escape returns to the previous screen. Breadcrumbs name
the saved references being inspected. Plan evidence and work items open from a
plan item. A `commit:HASH` link searches narratives anchored to that exact commit;
searching within it retains the anchor.

Use `n`/`p` for pages in Timeline, Decisions, History and Checkpoints, `[ ]` for
plan revisions, `f` for track/work-item filters, and `k` for timeline/history kinds.
PgUp/PgDn scroll the detail pane; `z` or Enter expands it. Escape first collapses
expanded detail. `r` refreshes saved data while staying in the current view or
navigation stack. `?` opens scrollable help; `q` or Ctrl-C exits.

Tracks opens with the `all` filter so imported proposals are visible alongside
current work. The current task's track remains selected and highlighted. Use `f`
to narrow the view. Proposed, accepted and historical tracks need no task binding;
only active or blocked nodes flag a missing task binding as an anomaly. Other recorded
inconsistencies, such as a completed node with unfinished plans, still appear.

A highlighted `FILTER` bar above the bottom key hints shows the applied filter
and its change key (`f` or `k`) in Tracks, Work items, Timeline and History results.
It remains visible when a long task name clips the header, while searching, and
when a status message or expanded detail is shown.

Empty views explain what is missing. A failed section shows the error and a retry
hint; the remaining views remain accessible. Health deliberately leaves the MCP
runtime unobserved: a terminal process cannot prove which server a client runs.
Use the connected `continuity_info` tool to inspect that process.

Reads are bounded. Checkpoints use time/ID cursors, not frozen snapshots. Tracks,
work items and plan discovery retain their existing 100-record inspection bounds;
counts expose omissions. Release browsing includes the latest 100 revisions;
`llewm release view history --id ID` narrows the record family. History bodies
use a 16 KiB detail budget and identify truncation. These bounds do not grant
permission to infer missing records or treat reported checks as observed checks.

## Report previews and delivery

`b` prepares the same redacted v1 draft as `llewm item report`, without saving
or submitting it. Source claims retain their authority, missing fields are named,
and reporter diagnostics describe the console process. Expected behavior and
hypothesis can be supplied through `item report`; the console leaves them blank
rather than inventing them.

Delivery belongs to the separate reporting adapter. Export its metadata snapshot
in a private local directory and pass that file explicitly:

```sh
llewm-report status --config /path/to/private-reporting.json > /private/report-status.json
llewm --project-root /path/to/project tui --report-status /private/report-status.json
```

The console reads neither the adapter's config nor its outbox, invokes no delivery,
and filters the supplied snapshot by permanent project ID. It shows pending or
delivered state, issue URL, receipt outcome/time, and the snapshot's observation
time/age. No supplied snapshot means unknown delivery, not an empty outbox.
Regenerate the snapshot, then press `r` to observe later changes. Receipt `recovered`
means the adapter observed a matching marker; it does not claim to have witnessed
the original submission. Snapshots are bounded to 1 MiB and 1,000 reports. They
contain private targets and issue URLs; keep them device-local and remove scratch
copies after use.

## Versioned reads for editor clients

The terminal and this read-only JSON interface use the same canonical projections:

```sh
llewm console view project
llewm console view checkpoints --task-id TASK_ID
llewm console view checkpoints --cursor NEXT_CURSOR
llewm console view checkpoint --id CHECKPOINT_ID
llewm console view health
llewm console view releases
llewm console view report --id BUG_ID
llewm console view delivery --report-status /private/report-status.json
```

Each response has `schema_version: 1`, `project_id`, `view`, and `data`. Data contains
the corresponding canonical project, checkpoint metadata/record, readiness,
release, bug report, or delivery projection. Missing checkpoint or unsupplied
snapshot returns `data: null`; read/validation failures go to stderr and fail the
command. Existing versioned work, plan, decision and history commands remain the
interfaces for those families. This adds no execution or reporting permission,
changes no MCP tool schema, and requires no database migration.

## Native qualification

Cross-builds and simulated screens verify compilation, layout and navigation;
they do not establish native terminal support. On each native Windows and macOS
host, use an isolated initialized fixture to exercise tab/number navigation,
arrow/back keys, task checkpoint chains, plan revisions, commit history, a bug
preview, a supplied pending/delivered snapshot, search, help scrolling, terminal
resize, Unicode, refresh and clean exit. Record OS/architecture, terminal/version,
exact clean commit, observed results and failures in Continuity. Until those runs
are observed, the console milestone's native acceptance criterion remains open.
