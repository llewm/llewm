#!/usr/bin/env bash
set -euo pipefail
umask 077
here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo=$(cd -- "$here/../../../.." && pwd)
out=${1:?Usage: build-bundle.sh NEW_OUTPUT_DIRECTORY [amd64|arm64]}
arch=${2:-amd64}
case "$arch" in amd64|arm64) ;; *) echo 'Unsupported Linux architecture' >&2; exit 2;; esac
[[ ! -e "$out" ]] || { echo 'Output already exists; choose a new bundle directory' >&2; exit 2; }
mkdir -p -- "$out"
out=$(cd -- "$out" && pwd)
(
  cd -- "$repo/components/orchestration"
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" GOWORK=off go build -trimpath -buildvcs=true -o "$out/llewm-orchestrate" ./cmd/llewm-orchestrate
)
# Explicit public asset list: never copy operator .env files, TLS keys or tokens.
for relative in build-bundle.sh README.md client.mcp.json.example \
  llewm-shared-broker.service shared-broker.env.example \
  docker/Dockerfile docker/compose.yaml docker/deploy.sh docker/docker.env.example docker/smoke-test.py \
  nginx/docker.conf nginx/native.conf nginx/proxy-options.conf nginx/remote-upstream.conf.example \
  proxmox/deploy.sh proxmox/lxc.env.example proxmox/llewm-shared-broker.service; do
  install -D -m 0644 -- "$here/$relative" "$out/deploy/$relative"
done
find "$out/deploy" -type d -exec chmod 0755 {} +
find "$out/deploy" -type f -name '*.sh' -exec chmod 0755 {} +
mkdir -m 0700 -- "$out/data"
touch "$out/data/.keep"
printf 'linux/%s\n' "$arch" > "$out/PLATFORM"
git -C "$repo" rev-parse HEAD > "$out/SOURCE_COMMIT"
git -C "$repo" status --porcelain > "$out/SOURCE_STATUS"
(
  cd -- "$out"
  find . -type f ! -name SHA256SUMS -print0 | LC_ALL=C sort -z | xargs -0 sha256sum > SHA256SUMS
)
printf 'Built %s (review SOURCE_COMMIT and SOURCE_STATUS before deployment)\n' "$out"
