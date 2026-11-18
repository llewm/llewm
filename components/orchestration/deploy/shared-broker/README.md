# Shared broker deployment

The provisioning scripts initialize one project per deployment. For several
projects on one listener, use the [shared project registry](../../docs/shared-agent-broker.md#multiple-projects-on-one-service)
with separate databases and credentials. A build does not deploy anything. Select
the target host, network exposure, TLS certificate and backup destination before
running deployment commands.

## Pair two developers through a temporary HTTPS tunnel

Both developers need `llewm-orchestrate`. The host also needs `cloudflared`
for this example. Choose a tunnel operator you trust with the broker's bearer
credentials and messages, since it terminates HTTPS. Use a fresh project for
the pairing session. On the host, in a POSIX shell:

```sh
umask 077
state=$(mktemp -d "$HOME/llewm-pairing.XXXXXXXX")
llewm-orchestrate shared-broker-init --db "$state/messages.db" --project pairing --machine dev-host
for member in host guest; do
  llewm-orchestrate shared-broker-member --db "$state/messages.db" --id "$member" \
    --permissions work --token-file "$state/$member.token"
done
llewm-orchestrate shared-broker-serve --db "$state/messages.db" \
  --listen 127.0.0.1:19469 --proxy-loopback
```

In another terminal start the tunnel:

```sh
cloudflared tunnel --no-autoupdate --url http://127.0.0.1:19469
```

Give the guest the printed HTTPS origin and project ID. Confirm the origin
with the host before using a credential. Deliver only `guest.token` to the
intended guest over a trusted channel, such as an existing SSH connection.
Keep both token values out of chat, URLs and shell arguments. The guest can
import the received file into private storage with `credential-import`.
On each machine, set `endpoint` to the confirmed origin and `token_file` to
that developer's private credential path, then run:

```sh
llewm-orchestrate shared-broker-info --endpoint "$endpoint" --project pairing --token-file "$token_file"
```

Compare the returned broker IDs. Use the same endpoint, project and token file
for the [client setup and message exchange](../../docs/shared-agent-broker.md#client-configuration-and-protocol).
To qualify the session, register one session per machine, send a targeted
message, retry its exact request and compare receipts. Pull from a saved
cursor in a fresh client process, then send an acknowledgment and a reply.

At teardown, revoke the guest with `shared-broker-revoke --db PATH --id guest`
using the host's database path. Repeat the guest identity check to confirm
rejection. Stop the tunnel and broker with Ctrl-C in their respective
terminals. Remove disposable credentials and, when the messages are no longer
needed, the private trial directory. A new Quick Tunnel gets a new hostname.
Resuming against the original database requires checking the destination and
matching the saved broker ID. [Cloudflare Quick Tunnels](https://developers.cloudflare.com/tunnel/get-started/quick-tunnels/)

## Reverse proxy contract

Clients always use HTTPS and continue to verify the expected project. A broker
normally terminates TLS itself. `shared-broker-serve --proxy-loopback` instead
accepts HTTP only on a literal loopback listener, for a colocated TLS proxy.
Wildcard, LAN and hostname listeners are refused in this mode, as are TLS flags.
This flag applies only to the shared messaging server. Local brokers and client
HTTPS requirements are unchanged.

nginx forwards bearer credentials; the broker authenticates every request. It
never trusts identity, scheme or authorization claims in forwarded headers.
Proxy configuration does not create a new login mechanism. The nginx templates
expose identity, commands and pull under `/v1/broker` and
`/v1/projects/PROJECT`, preserve error
responses, disable proxy retries/cache and allow the protocol's 20-second waits.
They use a dedicated origin, not a URL subpath. Certificate issuance/renewal is
managed by the operator; HTTP redirects and an ACME responder are not installed.

The [native nginx config](nginx/native.conf) terminates TLS on 443 and connects to
loopback HTTP. Install [proxy-options.conf](nginx/proxy-options.conf) at
`/etc/nginx/llewm-proxy-options.conf`. For nginx on another machine, use
[the verified HTTPS upstream example](nginx/remote-upstream.conf.example) and
keep TLS enabled on the broker. Substitute the backend hostname and CA; do not
disable upstream certificate verification or expose a plaintext backend on a LAN.

## Build a portable bundle

On a Linux build workstation with Git, GNU coreutils and a compatible Go toolchain:

```sh
components/orchestration/deploy/shared-broker/build-bundle.sh \
  dist/shared-broker-linux-amd64 amd64
```

The output must be a new directory. `arm64` is also supported for Linux Docker
hosts. The bundle includes a static binary, public deployment assets, platform,
source commit/worktree status, and SHA-256 checksums. Review `SOURCE_STATUS`: a
bundle can intentionally contain uncommitted source. Only listed public assets
are copied; local config files and credentials are excluded. No Go toolchain is
needed on the target. Transfer the bundle over your chosen trusted channel and
verify `sha256sum -c SHA256SUMS` before installation. Checksums detect changes;
they are not a publisher signature.

## Docker Compose

The Docker image contains only the static binary and a private state directory,
runs as UID/GID 10001, and uses a named data volume. nginx is a separate non-root
container sharing the broker's network namespace. This keeps the backend on
127.0.0.1 without publishing plaintext or requiring privileged networking. Only
nginx's TLS port is published. There is no Docker socket mounted in either
container, no host networking, and no automatic initialization on startup.

Copy `docker/docker.env.example` outside the bundle and edit it. It is a reviewed
shell configuration sourced by the script, so do not use untrusted config files.
Set an absolute `TLS_DIR` with `fullchain.pem` and `privkey.pem`. nginx runs as
UID/GID 10001: the certificate directory and files must be readable by that UID
inside the container, while the key remains private (for example directory 0700
and key 0600 owned by UID 10001 on a normal rootful Docker host). Account for UID
mapping on rootless/user-remapped Docker. The entire TLS directory is mounted
read-only so atomic certificate replacement works after an nginx reload/restart.
Do not put credentials in environment variables.

```sh
bundle=/absolute/path/to/shared-broker-linux-amd64
config=/absolute/path/to/docker.env
"$bundle/deploy/docker/deploy.sh" "$config" build "$bundle"
"$bundle/deploy/docker/deploy.sh" "$config" init
"$bundle/deploy/docker/deploy.sh" "$config" up
"$bundle/deploy/docker/deploy.sh" "$config" enroll alice /private/path/alice.token
"$bundle/deploy/docker/deploy.sh" "$config" check
```

`init` refuses an existing database. `enroll` rotates an existing engineer's
credential and writes to a new output token path; issuance retains a private copy
in the volume so a failed transfer is recoverable. Give each engineer their own
credential through a private channel. `up` recreates both containers together so
nginx joins the correct network namespace after a broker image update. nginx is
checked with `nginx -t`; also perform authenticated client qualification below.
`down` removes containers/network but retains the data volume. Never add
`--volumes` when stopping a deployment you intend to retain.

Defaults publish TLS at `127.0.0.1:8443`. Select a reachable `BIND_ADDRESS` and port
when deploying, or keep the host loopback binding behind another local TLS proxy.
Set a unique `COMPOSE_PROJECT_NAME`, image tag and port for each deployment. Pin
the configurable `NGINX_IMAGE` to a reviewed digest for reproducible production
rollouts; the example's `stable-alpine` tag is a configurable starting point.

## Proxmox: unprivileged LXC

Use a fresh Debian system-container template already downloaded on the selected
Proxmox node. This path runs the static broker and nginx natively: no Docker,
nesting or privileged container is requested. Copy/edit `proxmox/lxc.env.example`
outside the bundle, selecting a free CTID, template volume ID, storage, network,
hostname, project and TLS directory. Configuration is reviewed shell code.
The network includes `firewall=1`; configure the node/guest firewall policy for
the intended HTTPS clients. The script does not change cluster-wide firewall
policy, bridges, storage configuration or template downloads.

On the chosen node, preview commands first; only `--apply` invokes `pct`:

```sh
bundle=/absolute/path/to/shared-broker-linux-amd64
config=/absolute/path/to/lxc.env
"$bundle/deploy/proxmox/deploy.sh" "$config" create
"$bundle/deploy/proxmox/deploy.sh" "$config" create --apply
"$bundle/deploy/proxmox/deploy.sh" "$config" install "$bundle"
"$bundle/deploy/proxmox/deploy.sh" "$config" install "$bundle" --apply
"$bundle/deploy/proxmox/deploy.sh" "$config" enroll alice /private/path/alice.token --apply
"$bundle/deploy/proxmox/deploy.sh" "$config" status --apply
```

Apply runs as root on the selected node. Existing CTIDs are never destroyed or
replaced. Installation verifies the bundle and requires a running, unprivileged
guest with the configured hostname and no prior broker binary/database. It
installs nginx packages, a dedicated service account, private state/TLS files,
the broker systemd unit and nginx site, initializes the database, and starts both
services. nginx is masked during package installation so its default HTTP site
is not briefly exposed. The guest needs package-repository access. No passwords,
SSH keys, automatic certificate issuer or new engineer credential are installed
until explicitly requested through the appropriate operation.

This is an initial-provisioning script. It refuses to overwrite an existing
deployment. If an installation fails, it retains the guest and files for diagnosis;
it does not delete the guest or roll back a partially created database. Inspect
the failure and use a reviewed recovery/upgrade procedure, rather than rerunning
initial installation over live state. The LXC unit avoids extra service mount
namespaces; the outer unprivileged LXC boundary remains in effect.

## Qualification, updates and recovery

Use `shared-broker-info` with the external HTTPS origin, expected project, token
file and public CA as needed. Register two clients and test targeted messages,
reply/ack, cursor continuation and a bounded wait. Verify missing/revoked tokens
and forged forwarded identity headers cannot authenticate. Check restart/reboot
and confirm the backend port is not reachable from another host.

Before an image/binary update or restore, stop all broker/database writers and
make a protected backup of the complete state directory/volume, its compatible
binary/image reference and public configuration. Keep keys/tokens separately
protected. Never run two writable copies of one authority. Docker `up` is not a
database backup or migration mechanism. For Proxmox, do not assume a live guest
snapshot independently proves application-consistent recovery. Follow the
[shared broker recovery checks](../../docs/shared-agent-broker.md#deployment-qualification-and-recovery).

The source includes proxy policy tests. To run the disposable Docker/nginx test:

```sh
python3 "$bundle/deploy/docker/smoke-test.py" "$bundle" \
  --work-dir /path/on/disk/to/test-scratch
```

It requires Python 3, OpenSSL and Docker access, pulls the configured nginx image,
uses a random Compose project and loopback port, and removes its own containers,
volume, broker image and temporary credentials afterward. It checks token/header
authentication, routing, bounded waits and recovery after recreating both
containers. Docker may retain its shared nginx image/build cache.

Local container validation can qualify
the Docker/nginx path; it cannot qualify a particular Proxmox version, template,
firewall or host reboot. Those require the selected host.

Implementation references: [Docker Compose networking](https://docs.docker.com/compose/how-tos/networking/),
[nginx proxy directives](https://nginx.org/en/docs/http/ngx_http_proxy_module.html),
[Proxmox container documentation](https://github.com/proxmox/pve-docs/blob/master/pct.adoc),
and [the pct CLI](https://github.com/proxmox/pve-container/blob/master/src/PVE/CLI/pct.pm).
