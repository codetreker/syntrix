#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PACKAGES_ROOT="$(cd "$SCRIPT_DIR/../../../packages" && pwd)"

echo "Checking Go code formatting..."

unformatted=$(find "$PACKAGES_ROOT/syntrix" "$PACKAGES_ROOT/console-server" -type d -name vendor -prune -o -type f -name '*.go' -print0 | xargs -0 gofmt -l)

if [ -n "$unformatted" ]; then
    echo "Error: The following files are not formatted correctly:"
    echo "$unformatted"
    echo "Please format the listed Go files with gofmt."
    exit 1
fi

echo "Go code formatting check passed."
