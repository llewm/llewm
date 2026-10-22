# Temporary LAN history exchange

Use this optional workflow to exchange curated history before configuring a
History-sync memory repository. It requires no daemon, cloud account, Git server,
or sync configuration. Every listener is explicit and temporary. Transfer and
semantic import are separate operations; receiving files never selects work,
adopts a lineage, runs a procedure, or executes a checkpoint's next action.

## Prepare and exchange

On the machine with history, create an export in a private directory outside the
source repository and inspect its manifest, omissions and selected contents:

```sh
llewm memory export --directory /private/history-out
llewm memory inspect --directory /private/history-out
```

The project must already have a portable identity. For genuinely new independent
history, `llewm sync init` without a remote creates one. Do not use that command
without `--lineage` on an install that is going to join existing history.

Start a listener on the actual LAN interface's private IP; substitute your address:

```sh
llewm memory lan serve \
  --directory /private/history-out \
  --receive-directory /private/history-back \
  --listen 192.168.1.10:0 \
  --pairing-file /private/lan-pairing.json
```

The listener reports its address and expiry, never the secret. Privately deliver
`lan-pairing.json` to the intended peer through an already trusted channel. Keep
its file permissions private (0600 on POSIX). Do not paste its contents into an
agent conversation, task record, source repository, or command argument. It is a
high-entropy bearer credential plus a pinned ephemeral TLS certificate; anyone
holding it can perform the one permitted exchange. A short numeric code without
a password-authenticated key exchange would not provide the same protection.

On the receiving machine:

```sh
llewm memory lan exchange \
  --pairing-file /private/lan-pairing.json \
  --receive-directory /private/history-in
```

A fresh install can run this command before `llewm init`, even outside a checkout.
It receives without sending. To exchange in both directions, export and inspect
the receiving machine's history first, then add `--directory /private/peer-out`.
Both outgoing bundles must have the same lineage. Each destination must be new,
with an existing real parent directory. Bundles retain the portable publication
rules: validation before publication, private files, and no replacement.

The sender removes its pairing file when the listener exits normally, on expiry,
or on handled interrupt/termination. Delete the receiver's copy after use. After
an uncatchable kill or machine crash, remove any leftover sender credential file;
its expiry still prevents reuse by these clients. A failed authenticated attempt
consumes the pairing. Start a new listener with a new pairing file to retry.

## New installs expecting existing history

Choose **join existing history** before trial, prose adoption, or a first local
checkpoint. Install the binary first; receive and inspect the bundle, then set up
the target checkout's Git exclusion and local state. Ordinary `llewm init` does
not create a lineage. A trial does create one, so starting a new trial is the
wrong onboarding path for a receiving clone of an existing project.

In the target checkout, after reviewing the received history and confirming that
this checkout is the intended project:

```sh
llewm init
llewm sync init --lineage LINEAGE_FROM_INSPECT
llewm memory plan --directory /private/history-in
llewm memory apply --directory /private/history-in \
  --confirm-lineage LINEAGE_FROM_INSPECT \
  --confirm-project LOCAL_PROJECT_ID_FROM_PLAN
llewm bootstrap --checkpoint-id EXPECTED_SOURCE_CHECKPOINT_ID
```

`sync init --lineage` without `--remote` explicitly associates local state with
the existing lineage and generates its own instance identity. It does not enable
History sync. Never copy another machine's database, local instance identity,
configuration, credentials, client permissions, or standing grants. Local project
IDs and paths remain local. Upgrade an older binary if validation reports an
unsupported format/schema; do not weaken validation to accept it.

An already-associated different lineage is refused, even if its database looks
empty. Do not delete or silently relabel it. Preserve it and decide explicitly
whether a separate checkout/store should receive the history. Existing local
records without a portable identity also deserve review before joining, since
explicit association applies to that local history as well.

Import remains transactional and append-only, with exact identity confirmations,
idempotent replay, and collision/dependency checks. A collision blocks the apply;
this transport does not select a winning revision. Inspect the resulting task
heads/forks and recover the exact expected checkpoint. Imported source
verification does not verify this machine's filesystem or running client.
Configure the plugin/MCP and verify fresh-client recovery after the history is in
place, before creating replacement plans or treating onboarding as complete.

## Limits and later memory-repository setup

- TLS 1.3 authenticates the sender through the privately delivered certificate
  pin and the receiver through the random credential. The local network alone
  does not establish trust. The listener expires after five minutes by default;
  `--lifetime` can set a positive duration up to ten minutes. Clocks must agree
  closely enough to evaluate pairing expiry.
- Only specific literal private or loopback IPs are accepted. There is no
  wildcard/public bind, DNS discovery, proxy, relay, Internet fallback, persistent
  service, background sync, or automatic firewall change. Private addresses can
  also be routed over VPNs or forwarded by a router; interface binding is not a
  firewall or proof that a peer is physically on the LAN.
- The listener handles at most sixteen unauthenticated connection attempts, with
  bounded handshakes and I/O. The first authenticated attempt consumes it. The
  protocol accepts bounded manifest/record byte strings, never remote file paths.
- Only an existing validated export travels. The manifest declares omissions;
  raw sessions/events, transcripts, credentials, SQLite/WAL/SHM files and backups
  are excluded by the curated export. Pattern redaction does not prove arbitrary
  free text safe; review outgoing data. Successful transport validation is not
  semantic import approval.
- Each machine publishes independently. A disconnect can leave one complete
  incoming bundle even when the other machine reports failure. Inspect both
  destinations before retrying; there is no distributed transaction or claim of
  mutual import completion.
- Bundles are complete snapshots subject to current portable size/count bounds,
  not incremental sync. Linux loopback qualification covers the paired protocol
  and receiving-install workflow; other native platforms, real LAN interfaces,
  routing and firewall configurations require separate verification. POSIX file
  permissions are currently required for reading the pairing file.

After applying exchanges, configure the desired memory remote with the same
lineage using the [History-sync setup](memory-repository.md). Existing record
identities and hashes allow already-shared records to deduplicate. This does not
migrate temporary bundle files or configure anything automatically. Full-stream
collision/fork behavior remains the receiving transport's policy; LAN publication
alone does not resolve conflicts. Keep any still-unapplied received bundles until
they have been explicitly reconciled, then remove scratch exports/credentials.
