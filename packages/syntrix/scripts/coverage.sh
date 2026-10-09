#!/usr/bin/env bash
# Run Go coverage tool
# Usage: ./scripts/coverage.sh

set -euo pipefail

cd "$(dirname "$0")/.."
exec go run github.com/codetreker/go-cov/cmd/go-cov@v0.1.0 --coverprofile=coverage.out --skip-result-packages tests/ "$@"
