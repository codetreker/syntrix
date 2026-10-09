#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PACKAGE_ROOT="$(cd "$SCRIPT_DIR/../../../packages/syntrix" && pwd)"

echo "Checking Go code formatting..."

unformatted=$(find "$PACKAGE_ROOT" -type d -name vendor -prune -o -type f -name '*.go' -print0 | xargs -0 gofmt -l)

if [ -n "$unformatted" ]; then
    echo "Error: The following files are not formatted correctly:"
    echo "$unformatted"
    echo "Please run 'gofmt -w packages/syntrix' from the repository root."
    exit 1
fi

echo "Go code formatting check passed."
