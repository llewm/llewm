# Windows and macOS broker qualification

Use three distinct engineer credentials on the deployment under test. The
Linux operator sends the research requests and records receipts. The Windows
and Mac operators each control their own client and server. Use one project
for this collaborative exercise. The Linux operator needs the external URL,
expected project ID and a private local token file. Transfer tokens separately
through a trusted channel. Keep tokens out of messages and command arguments.

## Join the LXC authority

With the local binary on PATH, import the credential into a private directory.
The examples use placeholders for each operator's own paths. For a publicly
trusted certificate omit the CA flag.

```text
llewm-orchestrate credential-import --file RECEIVED_TOKEN --token-file PRIVATE_TOKEN
llewm-orchestrate shared-broker-info --endpoint HTTPS_ORIGIN --project PROJECT --token-file PRIVATE_TOKEN --save-profile move.json
llewm-orchestrate shared-broker-request --profile move.json --token-file PRIVATE_TOKEN --file state.json
llewm-orchestrate shared-broker-pull --profile move.json --token-file PRIVATE_TOKEN --file pull.json
```

Create the JSON files as UTF-8 without a BOM. In state.json use a fresh session
ID and request ID, your actual machine label and agent label. In pull.json use
the same session ID. The broker's JSON field names are case sensitive in these
examples.

```json
{"schema_version":1,"request_id":"start-UNIQUE","action":"state","session_id":"SESSION","agent":"AGENT","machine":"MACHINE","state":"active","ttl_seconds":600}
```

```json
{"session_id":"SESSION","after":0,"wait_seconds":10}
```

Give Linux your session ID and the returned broker ID. Linux targets your
session with one of the assignments below. Save the returned event ID, broker
ID and next_after. Send an ack and the result with shared-broker-request and
a separate JSON file for each. Replace placeholders with actual IDs and text.

```json
{"schema_version":1,"request_id":"ack-UNIQUE","action":"ack","session_id":"SESSION","reply_to":"REQUEST_EVENT_ID"}
```

```json
{"schema_version":1,"request_id":"result-UNIQUE","action":"message","session_id":"SESSION","reply_to":"REQUEST_EVENT_ID","text":"Findings with primary-source URLs, limitations, and recommendation."}
```

- Windows assignment: Research Magic Wormhole as an optional credential handoff provider. Verify how peers authenticate, what an operator has to host, Windows support, and fit for a short-lived broker credential. Return two primary sources and one integration risk.
- Mac assignment: Research an alternative based on an existing tailnet. Verify how device identity, file delivery and recipient confirmation work on macOS. Return two primary sources, setup costs and an assessment of fit for a first-time collaborator.

Linux verifies the authenticated actor, correlation to the original request
and the result text. Retry one unchanged request and compare receipts.
Start a fresh client using the saved broker ID and next_after; verify old
results do not appear again.

## Serve from each desktop

On Windows and then on macOS, create a disposable shared authority in a fresh
private directory. Each desktop runs the native broker binary. Use a chosen
reachable TLS endpoint, or start a temporary HTTPS tunnel with the backend on
literal loopback. Use a distinct project ID, token, port and directory for
each desktop. Example host commands for tunnel mode:

```text
llewm-orchestrate shared-broker-init --db PRIVATE_DIR/messages.db --project desktop-qual --machine DESKTOP
llewm-orchestrate shared-broker-member --db PRIVATE_DIR/messages.db --id linux --permissions work --token-file PRIVATE_DIR/linux.token
llewm-orchestrate shared-broker-serve --db PRIVATE_DIR/messages.db --listen 127.0.0.1:19469 --proxy-loopback
cloudflared tunnel --url http://127.0.0.1:19469
```

Give Linux the HTTPS origin, expected project and Linux credential by secure
handoff. Register a desktop session with a separate member credential. Linux
checks identity, sends a targeted ping, verifies the desktop's ack, then
resumes its cursor after the desktop restarts its broker on the same database.
Revoke the temporary Linux credential and confirm refusal. Close the tunnel
and stop the disposable broker. Record OS version, architecture, binary
revision, broker ID and pass/fail for join and serve separately.

## Repeat through the reverse proxy

After frontend DNS, certificate and upstream verification are configured,
check the same broker ID, saved cursor and a round trip via the proxy origin.
Then exercise guest reboot and an isolated restore using the deployment
guide's recovery steps. Record the proxy and recovery results separately
from the desktop results.
