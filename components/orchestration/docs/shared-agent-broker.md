# Shared agent messaging

The shared broker lets already-running agents on different machines exchange
durable messages, replies, acknowledgments and advisory session state. It is a
separate service and database from both the machine-local broker and the shared
work coordinator. It never launches agents, executes messages, grants work claims
or interrupts clients. Clients explicitly poll; disconnected local brokers keep
working independently.

This version uses direct client connections. It does not forward local history,
run remote hooks, replicate SQLite databases or maintain an offline outbox. A
failed request leaves shared acceptance uncertain: retain its request ID and
exact body and retry. Do not report remote delivery from a local receipt.

## Authority and compatibility

Use one shared messaging authority per project. Initialize a new database with
`shared-broker-init`; local, shared-work and shared-messaging databases have
different application identities and refuse cross-opening. Existing databases,
commands, local hooks and default ports are unchanged. No migration is needed.
Older binaries do not support shared databases; recovery must use a compatible
shared-broker binary, not an older local-broker command.

All shared client connections require HTTPS, including loopback. Clients verify the TLS
certificate with platform roots or an explicitly supplied public CA bundle;
redirects and insecure verification bypasses are refused. Clients require
`--project` and check the server's shared scope and project before sending
message/session content. The configured origin and TLS establish server trust;
the returned labels alone are not authentication. A colocated reverse proxy can
terminate TLS with the server's explicit `--proxy-loopback` mode, which permits
HTTP only on literal loopback. The broker still checks bearer credentials and
ignores forwarded identity/scheme headers. For a proxy on another machine, retain
verified TLS on the upstream connection. See [reverse proxy deployment](../deploy/shared-broker/README.md).

Credentials authenticate engineers. Enroll each engineer separately with `work`
permission to publish state and messages. An enrollment with no permissions can
read identity but cannot establish a messaging session. Rotation and revocation
use local operator commands; never put token values in configuration, Git,
Continuity or logs. One engineer may use their credential on several machines.
Machine, agent and task labels remain caller assertions. Session IDs must be
globally unique; an established session cannot change engineer or machine label.

Routing selects recipients; it is not a confidentiality boundary between project
members. All participants see session state and project broadcasts, and a member
can choose a task label. Use separate authorities/projects for different trust
groups. Explicitly choose what to share. Send repository-relative references and
commit IDs, not absolute paths, raw hook data, transcripts or secrets. Known secret
patterns are redacted before persistence; arbitrary secrets cannot be recognized
reliably. The service has bounded records and reads but no automatic retention or
rate limiting; select network access and storage monitoring for the actual host.

## Host configuration templates

[The deployment guide](../deploy/shared-broker/README.md) provides a static binary
bundle builder, Docker image/Compose tooling, native unprivileged Proxmox LXC
provisioning, nginx configs, and the original direct-TLS systemd/MCP templates.
Builds do not activate a service. Deployment scripts make the documented changes
when explicitly invoked against the chosen host; Proxmox defaults to a preview.
Host selection, DNS, certificate issuance and firewall policy remain explicit.

For a Linux system service, prepare a dedicated `llewm` user/group, install the
verified binary at `/opt/llewm/bin/llewm-orchestrate`, and create
`/var/lib/llewm-shared-broker` owned by that account with mode 0700. Keep the
database and token files at 0600. Copy and edit `shared-broker.env.example` as
`/etc/llewm/shared-broker.env`; it contains paths and listener configuration only.
Provide a server certificate matching the chosen DNS/IP and a private key readable
by the service user at the configured TLS paths. Keep the private key at 0600.
The template defaults to loopback; select a reachable address and restrict network
access to intended clients when deploying. Its state directory is writable under
systemd's otherwise read-only filesystem policy. If changing the database path,
update that policy as well.

Initialize and enroll as the service account (substitute the chosen project,
host and engineer; these examples do not provision accounts or certificates):

```sh
llewm-orchestrate shared-broker-init \
  --db /var/lib/llewm-shared-broker/messages.db \
  --project project-id --machine server-host-label
llewm-orchestrate shared-broker-member \
  --db /var/lib/llewm-shared-broker/messages.db \
  --id engineer-id --permissions work \
  --token-file /var/lib/llewm-shared-broker/engineer.token
```

Initialization refuses overwrites. Transfer the issued credential through a
private channel to that engineer, independently of the public CA certificate.
Use `credential-import --file RECEIVED_TOKEN --token-file NEW_PRIVATE_TOKEN`
on the client if needed to establish private file permissions. Copy the unit to
the host's systemd unit directory, inspect the resolved configuration, then enable
and start it only as part of the chosen host's deployment. The template restarts
on failure and receives a graceful stop before systemd's stop timeout.

## Client configuration and protocol

Edit the MCP example for the client harness's configuration format. Set the
endpoint, project, paths, machine and agent labels, and provide the actual session
ID through `LLEWM_SESSION_ID` in that MCP process's environment (or replace
`--session-env` with `--session ACTUAL_ID` when launching). A fixed ID reused across
sessions is invalid operational configuration. Add `--task ID` if needed. Omit
`--ca-file` when the certificate is already trusted by platform roots. The example
does not install client hooks. Configure a separate MCP server name alongside the
local broker; their cursors and destinations are independent.

Check the destination before use:

```sh
llewm-orchestrate shared-broker-info \
  --endpoint https://broker.example.invalid:9469 --project project-id \
  --token-file /path/to/private/engineer.token --ca-file /path/to/public/ca.pem
```

MCP exposes the same five broker tool names as the local adapter. Publish
`broker_state`, then use `broker_send`, `broker_pull` and `broker_ack`. The adapter
binds session, task and machine from its configuration; callers cannot override
them through tool input. Pull uses the existing bounded routing, reply correlation,
cursor and wait semantics from [the broker protocol](agent-broker.md#cursors-and-bounded-waits).

For CLI use, `shared-broker-request` and `shared-broker-pull` take the same JSON
files as their local equivalents plus `--project` and TLS configuration. Shared
state commands additionally require the caller's `machine` label:

```json
{
  "schema_version": 1,
  "request_id": "start-unique-session",
  "action": "state",
  "session_id": "globally-unique-session-id",
  "machine": "client-host-label",
  "agent": "codex",
  "state": "active",
  "ttl_seconds": 600
}
```

Messages and acknowledgments omit `machine`; their session's state carries that
label. `shared_stored` means the shared authority durably accepted a record, not
that a recipient read it. `acknowledged` means a recipient explicitly acknowledged
the referenced message, not that its requested work succeeded. Save `broker.id`
and `next_after` together per session/task/filter, separately from local cursors.
A replacement authority has a new identity; do not silently reuse old cursors or
resend old messages to it. Ended/expired session contact is advisory, never proof
that a process stopped or work finished.

## Deployment qualification and recovery

Before considering a selected host ready:

1. Verify private storage and service-account ownership, trusted TLS name/CA,
   expected project/scope, and rejection of wrong credentials and projects.
2. From two client machines, register unique sessions and exchange a targeted
   message, an acknowledgment and a correlated reply. Check authenticated actors,
   asserted machine labels, stable IDs, redaction and cursor continuation.
3. Retry the exact same request after an interrupted connection; it must return
   the original receipt. Revoke a temporary test credential and verify rejection.
4. Restart the service and check the same broker identity, saved cursors and
   messages. Verify service start after a host reboot under the selected policy.
5. Stop the service and all local membership/database writers. Make a protected
   backup of its database and any SQLite sidecars together, along with the exact
   compatible binary/version and host configuration. Keep TLS keys and engineer
   credentials in separate protected backups. Do not copy a running database.
6. Exercise restore into isolated private storage with a compatible binary and
   trusted test TLS endpoint. Keep the recovered service isolated from production
   clients. Verify identity, records, credential revocation and cursors before
   cutover. A backup older than accepted messages loses those messages; never run
   two writable copies of one authority. If rollback loses cursor history, stop
   clients and reconcile explicitly rather than silently resetting their cursors.

Automated tests cover TLS, CLI/MCP exchange, scope separation, attribution,
idempotency and database reopen. Actual two-host transport, host reboot, operating
system service policy and operational backup/restore require the selected host;
templates alone do not qualify them. Local brokers remain usable during shared
service outages. A future explicit bridge would need a durable outbox and origin
provenance; direct shared connections do not claim those guarantees.
