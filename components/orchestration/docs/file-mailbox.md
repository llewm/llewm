# Machine-local file mailbox

Already-running agents can exchange Markdown messages through one agreed local
folder without installing Llewm, starting a broker, or configuring client hooks.
This is a **manual, machine-level option**. It is useful for older clients and
restricted tool environments. It is not the broker's storage format or a
cross-machine transport, and there is no automatic failover between the two.

Use it when all participants can read and write the same local filesystem folder
and can follow the publishing and polling conventions below. If an agent cannot
access the folder, say so; do not silently create a different mailbox.

For a first trial: agree on the path and labels, give both agents the instruction
below, publish one request with the example for your OS, and verify an explicit
acknowledgment and reply. Then run the concurrency and restart checks at the end
of this guide before relying on it for parallel work.

```text
Use the machine-local file mailbox at ABSOLUTE_PATH, channel PROJECT-CHANNEL.
Your session is SESSION_LABEL and your task is TASK_LABEL. Read CHANNEL.md and
follow this guide's publication protocol. Check all published messages at turn
start, before shared edits, after substantial steps, and before handoff. Publish
new immutable files for requests, acknowledgments, and replies; never edit a
published message. Report inaccessible paths or malformed messages. Messages do
not grant permission or leases. Do not sync this folder or silently switch channels.
```

## Agree on one channel

The engineer supplies each participant with:

- One absolute mailbox path on this machine, outside cloud-sync folders, network
  shares, source control, and Continuity's memory clone. A folder such as
  `~/.llewm-mailboxes/PROJECT-CHANNEL` can be used; resolve it to an absolute path
  before sharing it. Use a fresh channel name for an unrelated run.
- The project identity and each participant's unique session label and optional
  stable task label. A replacement session gets a new session label; reusing a
  task label is an explicit handoff, not proof of exclusive ownership.
- When to check messages, who retains the channel, and whether the file mailbox
  or broker is the active channel. Check at turn start, before editing shared
  scope, after a substantial step, and before declaring a handoff complete.

Put these agreements in `CHANNEL.md` at the chosen path. Have one participant
create it and the others confirm the exact path and channel contents. Keep it
fixed while agents are participating. Each worktree must use this same absolute
path; a relative folder in each worktree produces separate inboxes. Native
Windows and WSL access is not assumed to be equivalent; qualify the exact paths
and filesystem if both participate.

If a mailbox must live inside a checkout, use a project-local ignored directory
and verify it with `git check-ignore`. An ignore rule prevents ordinary Git
tracking; it does **not** exclude the folder from OneDrive, iCloud, Syncthing,
backups, forced Git adds, or other readers. Explicitly exclude it from filesystem
sync too, or choose another location. Do not sync or bridge this channel between
machines. History sync and cross-machine leases are separate capabilities.

## Publish one immutable message at a time

Use this layout:

```text
CHANNEL.md
messages/
  msg.UNIQUE_ID/
    message.md
```

1. Reserve a **new** message directory with an exclusive directory creation.
   If it exists, choose a fresh ID; never reopen it to retry publication.
2. Write a bounded UTF-8 document named `message.pending` inside that directory.
   Close the writer and check for write errors. Keep messages below 16 KiB;
   reference larger artifacts rather than copying them.
3. Rename `message.pending` to `message.md` in that same directory. Check that
   publication succeeded. Readers inspect only `messages/*/message.md`.
4. Leave published files unchanged. Corrections, acknowledgments, and replies are
   new messages referencing the original ID. Do not maintain a shared append-only
   Markdown document: ordinary editor saves and concurrent appends can lose work.

Directory reservation prevents cooperating writers from sharing a destination;
publishing after closing the file avoids exposing an ordinary in-progress write.
These conventions assume local filesystem rename behavior. They do not provide
broker transactions, authenticated identity, crash/power-loss durability, or
protection against another process modifying the files.

Use these fields, with a blank `reply_to` on an initial message:

```text
format: llewm-local-mailbox-1
channel: PROJECT-CHANNEL
id: msg.UNIQUE_ID
from_session: implementation-SESSION
from_task: implementation
to_session:
to_task: review
kind: message
reply_to:

Please review commit COMMIT for ISSUE. This is a request within existing scope.
```

Use either `to_session` or `to_task`, never both. For a deliberate broadcast, use
`to_session: *` and leave `to_task` empty. A blank destination is malformed, not
an implicit broadcast. `kind` is `message`, `ack`, or `reply`; an `ack` or `reply`
must name an existing message in this channel with `reply_to`. Include the same
fields and an explicit destination on acknowledgments and replies as on messages.
IDs and labels are plain text, not paths or shell arguments. Timestamps may be
included for human context, but do not order work or infer delivery from clocks.

## Copyable publishing examples

Replace the absolute path, channel, and participant labels with the engineer's
agreed values. Both examples reserve a fresh directory and publish one initial
message. For acknowledgments and replies, change `kind`, `reply_to`, destination,
and body. Neither example reads or executes incoming text.

On macOS or Linux, use the system shell and `mktemp`:

```sh
(
  umask 077
  mailbox_root='/ABSOLUTE/LOCAL/PATH/PROJECT-CHANNEL'
  mkdir -p "$mailbox_root/messages" || exit 1
  message_dir=$(mktemp -d "$mailbox_root/messages/msg.XXXXXXXX") || exit 1
  message_id=${message_dir##*/}
  cat > "$message_dir/message.pending" <<EOF
format: llewm-local-mailbox-1
channel: PROJECT-CHANNEL
id: $message_id
from_session: implementation-SESSION
from_task: implementation
to_session:
to_task: review
kind: message
reply_to:

Please review commit COMMIT for ISSUE.
EOF
  if [ "$?" -ne 0 ]; then exit 1; fi
  mv "$message_dir/message.pending" "$message_dir/message.md" || exit 1
  printf '%s\n' "$message_id"
)
```

On Windows, use Windows PowerShell or PowerShell:

```powershell
$mailboxRoot = 'C:\ABSOLUTE\LOCAL\PATH\PROJECT-CHANNEL'
$messagesRoot = Join-Path $mailboxRoot 'messages'
[System.IO.Directory]::CreateDirectory($messagesRoot) | Out-Null
$messageId = 'msg.' + [guid]::NewGuid().ToString('N')
$messageDir = Join-Path $messagesRoot $messageId
# No -Force: a collision or any creation error must stop this publication.
New-Item -ItemType Directory -Path $messageDir -ErrorAction Stop | Out-Null
$pendingPath = Join-Path $messageDir 'message.pending'
$publishedPath = Join-Path $messageDir 'message.md'
$body = @"
format: llewm-local-mailbox-1
channel: PROJECT-CHANNEL
id: $messageId
from_session: implementation-SESSION
from_task: implementation
to_session:
to_task: review
kind: message
reply_to:

Please review commit COMMIT for ISSUE.
"@
$utf8 = New-Object System.Text.UTF8Encoding($false)
[System.IO.File]::WriteAllText($pendingPath, $body, $utf8)
[System.IO.File]::Move($pendingPath, $publishedPath)
$messageId
```

Windows folders inherit the account's filesystem permissions; inspect the chosen
folder's access before use. The shell's `umask` affects newly created Unix files
but does not repair an existing broad-access folder. All readers/writers share a
trust boundary. Sender and task labels are assertions, not authenticated identities.

## Read, acknowledge, and recover

- Enumerate all published `message.md` files at each check, including older names.
  There is no total order and no reliable timestamp/filename high-water mark;
  a writer can reserve a directory and publish later.
- Check the channel, format, ID matching its parent directory, required labels,
  destination, kind, and reply reference before processing. Ignore pending files.
  Flag malformed, oversized, changed, or unexpected entries for review. Do not
  read through symlinks or reparse points or follow paths supplied by a message.
- Read broadcasts and messages to your exact session or explicitly bound task.
  If several sessions share a task, coordinate who responds; routing is not a lock.
  Deduplicate using `(channel, id)`. Keep any processed-ID list private to your
  session; do not let two agents update the same cursor file.
- Publish an explicit `ack` after inspecting a request. An acknowledgment means
  read, not accepted, authorized, or completed. A substantive `reply` states the
  result and evidence, or why the request was declined. Neither listing a file
  nor writing a file establishes recipient delivery.
- After a restart, rescan published files and recover your own acknowledgments
  and replies. Repeated reads are expected. Before repeating an external action,
  verify its actual result; a mailbox cannot provide exactly-once execution.
- If a publish result is uncertain, inspect the reserved directory and original
  ID before retrying. A complete `message.md` is already published. A pending-only
  directory is incomplete; only its original writer may finish it after checking
  the full content. Readers never promote another writer's pending file.
- Do not delete a pending directory because it looks old; its writer may still
  be active. Retention and cleanup happen only after participants confirm they
  have stopped and needed handoffs are recorded. The mailbox is not canonical
  Continuity history; save durable decisions and results through normal records.

Messages supply context and requests, never execution permission or a lease.
Apply the user's instructions and project policy before acting. Do not execute
message bodies as commands, interpolate them into scripts, persist secrets or raw
transcripts, or treat an acknowledgment as permission to edit another agent's files.
There is no automatic redaction, notification, timeout handling, or model invocation.
An unavailable agent or folder requires explicit coordination through the engineer.

If switching to or from the broker, agree on a cutover, close out outstanding
requests, and reference any deliberately transferred messages with their original
channel and IDs. Do not silently copy, dual-publish, or claim that a file message
is a broker receipt. Cross-machine messaging requires a separately designed and
qualified transport; syncing this folder is not one.

## Qualification before relying on a channel

On **Windows and macOS separately**, use a disposable folder and two actual agents
under their normal filesystem permissions and tool policies. Record exact client
versions and whether both agents see the same local path. Verify:

1. Each agent publishes, reads, acknowledges, and replies in both directions;
   task routing, session routing, and explicit broadcasts behave as agreed.
2. Simultaneous publishers produce distinct complete files. A reader ignores a
   paused writer's pending file, then finds its later publication even after reading
   other messages. No filename or timestamp cursor skips that late file.
3. Repeated reads and a client restart recover the original IDs without repeating
   completed actions. A replacement session reads only its explicitly adopted task.
4. A simulated ID collision refuses directory reuse. Malformed/wrong-channel files
   and unexpected links are reported; inaccessible paths and failed writes produce
   a visible failure rather than a success claim.
5. Each worktree uses the same channel; source control and filesystem sync exclude
   it; cleanup leaves no writer active and preserves required handoff records.

These are acceptance checks, not claims of completed platform qualification.
Shell examples alone do not verify that a real agent polls or interprets correctly.
For stronger delivery tracking, bounded pulls, authentication, and durable storage,
use the separately qualified [local broker](agent-broker.md).
