#!/usr/bin/env bash
# Run on the chosen Proxmox node. Default is a command preview, with no pct calls.
set -euo pipefail
umask 077
apply=0
if [[ "${!#}" == --apply ]]; then apply=1; set -- "${@:1:$#-1}"; fi
config=${1:?Usage: deploy.sh CONFIG create|install|enroll|status [arguments] [--apply]}
action=${2:?Missing action}
shift 2
source "$config" # Reviewed local shell configuration; never accept remote input.
: "${CTID:?}" "${CT_HOSTNAME:?}" "${PROJECT_ID:?}" "${MACHINE_LABEL:?}"
[[ "$CTID" =~ ^[1-9][0-9]{2,8}$ ]] || { echo 'Invalid CTID' >&2; exit 2; }
[[ "$CT_HOSTNAME" =~ ^[a-zA-Z0-9][a-zA-Z0-9.-]*$ ]] || { echo 'Invalid hostname' >&2; exit 2; }
if (( apply )); then
  (( EUID == 0 )) || { echo 'Run --apply as root on the selected Proxmox node' >&2; exit 2; }
  command -v pct >/dev/null
fi
run() {
  printf '%q ' "$@"; printf '\n'
  if (( apply )); then "$@"; fi
}
require_guest() {
  if (( apply )); then
    guest_config=$(pct config "$CTID")
    grep -qx 'unprivileged: 1' <<< "$guest_config" || { echo 'Refusing a privileged guest' >&2; exit 2; }
    grep -Fxq "hostname: $CT_HOSTNAME" <<< "$guest_config" || { echo 'Guest hostname does not match config' >&2; exit 2; }
    [[ $(pct status "$CTID") == 'status: running' ]] || { echo 'Guest must be running' >&2; exit 2; }
  fi
}
case "$action" in
  create)
    : "${CT_TEMPLATE:?}" "${CT_STORAGE:?}" "${CT_NET0:?}"
    # pct create itself refuses an existing VMID. Never destroy/recreate a guest.
    run pct create "$CTID" "$CT_TEMPLATE" --hostname "$CT_HOSTNAME" --ostype debian \
      --unprivileged 1 --rootfs "$CT_STORAGE:${CT_DISK_GB:-8}" \
      --memory "${CT_MEMORY_MB:-512}" --cores "${CT_CORES:-1}" --net0 "$CT_NET0" --onboot 1
    run pct start "$CTID"
    ;;
  install)
    bundle=${1:?Provide the transferred Linux amd64 bundle directory}
    : "${TLS_DIR:?}"
    [[ $(cat "$bundle/PLATFORM") == linux/amd64 ]] || { echo 'Proxmox bundle must be linux/amd64' >&2; exit 2; }
    (cd -- "$bundle" && sha256sum -c SHA256SUMS >/dev/null)
    [[ -f "$TLS_DIR/fullchain.pem" && -f "$TLS_DIR/privkey.pem" ]] || { echo 'Missing TLS files' >&2; exit 2; }
    require_guest
    if (( apply )); then
      pct exec "$CTID" -- test ! -e /opt/llewm/bin/llewm-orchestrate || { echo 'Existing install: back up and use a reviewed upgrade procedure' >&2; exit 2; }
      pct exec "$CTID" -- test ! -e /var/lib/llewm-shared-broker/messages.db || { echo 'Existing database; refusing initial install' >&2; exit 2; }
    fi
    # Do not expose nginx's default HTTP site during package installation.
    run pct exec "$CTID" -- systemctl mask nginx.service
    run pct exec "$CTID" -- env DEBIAN_FRONTEND=noninteractive apt-get update
    run pct exec "$CTID" -- env DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends nginx ca-certificates
    run pct exec "$CTID" -- useradd --system --user-group --home-dir /nonexistent --shell /usr/sbin/nologin llewm
    run pct exec "$CTID" -- install -d -m 0755 /opt/llewm/bin /etc/llewm
    run pct exec "$CTID" -- install -d -m 0700 /etc/llewm/tls
    run pct exec "$CTID" -- install -d -m 0700 -o llewm -g llewm /var/lib/llewm-shared-broker
    run pct push "$CTID" "$bundle/llewm-orchestrate" /opt/llewm/bin/llewm-orchestrate --perms 0755
    run pct push "$CTID" "$bundle/deploy/proxmox/llewm-shared-broker.service" /etc/systemd/system/llewm-shared-broker.service --perms 0644
    run pct push "$CTID" "$bundle/deploy/nginx/native.conf" /etc/nginx/sites-available/llewm-shared-broker --perms 0644
    run pct push "$CTID" "$bundle/deploy/nginx/proxy-options.conf" /etc/nginx/llewm-proxy-options.conf --perms 0644
    run pct push "$CTID" "$TLS_DIR/fullchain.pem" /etc/llewm/tls/fullchain.pem --perms 0644
    run pct push "$CTID" "$TLS_DIR/privkey.pem" /etc/llewm/tls/privkey.pem --perms 0600
    run pct exec "$CTID" -- rm -f /etc/nginx/sites-enabled/default
    run pct exec "$CTID" -- ln -s /etc/nginx/sites-available/llewm-shared-broker /etc/nginx/sites-enabled/llewm-shared-broker
    run pct exec "$CTID" -- runuser -u llewm -- /opt/llewm/bin/llewm-orchestrate shared-broker-init \
      --db /var/lib/llewm-shared-broker/messages.db --project "$PROJECT_ID" --machine "$MACHINE_LABEL"
    run pct exec "$CTID" -- nginx -t
    run pct exec "$CTID" -- systemctl daemon-reload
    run pct exec "$CTID" -- systemctl unmask nginx.service
    run pct exec "$CTID" -- systemctl enable --now llewm-shared-broker.service nginx.service
    run pct exec "$CTID" -- systemctl is-active llewm-shared-broker.service nginx.service
    ;;
  enroll)
    engineer=${1:?Provide engineer ID}; destination=${2:?Provide a new private output token path}
    [[ "$engineer" =~ ^[a-zA-Z0-9_-]+$ ]] || { echo 'Invalid engineer ID' >&2; exit 2; }
    [[ ! -e "$destination" && ! -L "$destination" ]] || { echo 'Token destination already exists' >&2; exit 2; }
    require_guest
    token="/var/lib/llewm-shared-broker/$engineer-$(date +%s)-$RANDOM.token"
    run pct exec "$CTID" -- runuser -u llewm -- /opt/llewm/bin/llewm-orchestrate shared-broker-member \
      --db /var/lib/llewm-shared-broker/messages.db --id "$engineer" --permissions work --token-file "$token"
    if (( apply )); then (set -o noclobber; : > "$destination"); fi
    run pct pull "$CTID" "$token" "$destination" --perms 0600
    ;;
  status)
    require_guest
    run pct status "$CTID"
    run pct exec "$CTID" -- systemctl is-active llewm-shared-broker.service nginx.service
    ;;
  *) echo 'Unknown action' >&2; exit 2 ;;
esac
