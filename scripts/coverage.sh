#!/usr/bin/env bash
# Run Go coverage tool
# Usage: ./scripts/coverage.sh

set -e

cd "$(dirname "$0")/.."
go run github.com/codetreker/go-cov/cmd/go-cov@v0.1.0 --skip-result-packages tests/ "$@"
