#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."
dependencies=$(go list -deps ./cmd/syntrix)
forbidden=$(printf '%s\n' "$dependencies" | sed -n \
  '\#^github\.com/codetreker/syntrix/\(tools/deployment-cli\|deployment\|internal/deploymentcli\)\(/\|$\)#p')
if [ -n "$forbidden" ]; then
  printf 'Runtime dependency graph includes deployment tooling:\n%s\n' "$forbidden" >&2
  exit 1
fi
