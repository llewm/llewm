#!/usr/bin/env bash
set -euo pipefail
umask 077
here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
config=${1:?Usage: deploy.sh CONFIG build|init|up|check|enroll|down [arguments]}
action=${2:?Missing action}
shift 2
# A reviewed local shell configuration, not remote or untrusted input.
set -a
source "$config"
set +a
: "${COMPOSE_PROJECT_NAME:?}" "${TLS_DIR:?}" "${PROJECT_ID:?}" "${MACHINE_LABEL:?}"
[[ "$TLS_DIR" = /* ]] || { echo 'TLS_DIR must be absolute' >&2; exit 2; }
compose=(docker compose --project-name "$COMPOSE_PROJECT_NAME" --env-file "$config" -f "$here/compose.yaml")
case "$action" in
  build)
    bundle=${1:?Provide the built bundle directory}
    (cd -- "$bundle" && sha256sum -c SHA256SUMS >/dev/null)
    docker build --platform "$(cat "$bundle/PLATFORM")" -t "${BROKER_IMAGE:-llewm-shared-broker:local}" -f "$bundle/deploy/docker/Dockerfile" "$bundle"
    ;;
  init)
    "${compose[@]}" run --rm --no-deps broker shared-broker-init --db /data/messages.db --project "$PROJECT_ID" --machine "$MACHINE_LABEL"
    ;;
  up)
    # Namespace-sharing nginx must be recreated together with its broker.
    "${compose[@]}" up -d --force-recreate broker nginx
    "${compose[@]}" exec -T nginx nginx -t
    ;;
  check)
    "${compose[@]}" config --quiet
    "${compose[@]}" exec -T nginx nginx -t
    "${compose[@]}" ps
    ;;
  enroll)
    engineer=${1:?Provide engineer ID}; destination=${2:?Provide a new private output token path}
    [[ "$engineer" =~ ^[a-zA-Z0-9_-]+$ ]] || { echo 'Invalid engineer ID' >&2; exit 2; }
    [[ ! -e "$destination" && ! -L "$destination" ]] || { echo 'Token destination already exists' >&2; exit 2; }
    # Enroll after up. Issuance and transfer use files, never stdout or env vars.
    token="/data/$engineer-$(date +%s)-$RANDOM.token"
    "${compose[@]}" exec -T broker /usr/local/bin/llewm-orchestrate shared-broker-member --db /data/messages.db --id "$engineer" --permissions work --token-file "$token"
    (set -o noclobber; : > "$destination")
    "${compose[@]}" cp "broker:$token" "$destination"
    chmod 0600 -- "$destination"
    printf 'Credential saved to %s; retain the private volume copy for recovery if transfer failed.\n' "$destination"
    ;;
  down) "${compose[@]}" down ;; # Retain named volume; never use --volumes here.
  *) echo 'Unknown action' >&2; exit 2 ;;
esac
