#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKSPACE_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

GO_VERSION=$(awk '$1 == "go" { print $2 }' "$WORKSPACE_ROOT/packages/syntrix/go.mod")
IMAGE="golang:${GO_VERSION:?Go version is missing from packages/syntrix/go.mod}"

echo "Starting Docker container with 1 CPU (Image: $IMAGE)..."
printf 'Command:'
printf ' %q' "$@"
printf '\n'

docker run --rm \
    --cpus="1" \
    --net=host \
    -v "$WORKSPACE_ROOT:/workspace" \
    -v "${GOPATH:-$HOME/go}/pkg/mod:/go/pkg/mod" \
    -v "${GOCACHE:-$HOME/.cache/go-build}:/root/.cache/go-build" \
    -w "/workspace/packages/syntrix" \
    "$IMAGE" \
    "$@"
