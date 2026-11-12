#!/usr/bin/env bash
# Build the fastclaw-sandbox runtime image used by the agent's exec
# sandbox. Bundles Python + Node + Camoufox (anti-detect Firefox) so
# the camoufox-cli skill works on the first turn without any pip/npm
# round-trips.
#
# Usage:
#   deploy/docker/sandbox/build.sh                      # local build, tag latest
#   deploy/docker/sandbox/build.sh -t v1                # custom tag
#   deploy/docker/sandbox/build.sh --push               # build + push
#   deploy/docker/sandbox/build.sh --platform linux/amd64,linux/arm64 --push
#                                                       # multi-arch buildx
#
# After building, point the gateway at it via Settings → Sandbox →
# Image, or during onboard. Default: thinkany/fastclaw-sandbox:<date>.
#
# Releasing a new image: push BOTH :latest and a new dated tag
# (-t YYYYMMDD), then bump sandbox.DefaultImage (internal/sandbox/
# executor.go) and the web placeholders. BoxLite Cloud caches images by
# reference and never re-pulls a moved :latest, so only a new tag
# reaches existing deployments.

set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd "$SCRIPT_DIR/../../.." && pwd)

IMAGE_NAME=${IMAGE_NAME:-thinkany/fastclaw-sandbox}
TAG=latest
PUSH=0
PLATFORM=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    -t|--tag)
      TAG="$2"; shift 2 ;;
    -i|--image)
      IMAGE_NAME="$2"; shift 2 ;;
    --push)
      PUSH=1; shift ;;
    --platform)
      PLATFORM="$2"; shift 2 ;;
    -h|--help)
      sed -n '2,18p' "$0"; exit 0 ;;
    *)
      echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

REF="${IMAGE_NAME}:${TAG}"

echo "==> building ${REF}"
echo "    context: ${SCRIPT_DIR}"

if [[ -n "$PLATFORM" ]]; then
  # buildx path — required for multi-arch and for --push to work
  # against a registry without first loading into the local daemon.
  docker buildx build \
    --platform "$PLATFORM" \
    $([[ $PUSH -eq 1 ]] && echo --push || echo --load) \
    -t "$REF" \
    "$SCRIPT_DIR"
else
  docker build -t "$REF" "$SCRIPT_DIR"
  if [[ $PUSH -eq 1 ]]; then
    echo "==> pushing ${REF}"
    docker push "$REF"
  fi
fi

echo "==> done: ${REF}"
echo
echo "Use it via Settings → Sandbox → Image, or set during onboard:"
echo "    ${REF}"
