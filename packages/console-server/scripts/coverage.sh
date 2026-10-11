#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."
: "${CONSOLE_TEST_POSTGRES_DSN:?Set CONSOLE_TEST_POSTGRES_DSN to a dedicated PostgreSQL test database}"

# The module policy is authoritative; go-cov environment overrides can otherwise
# lower thresholds, omit packages, or disable race and uncovered-block checks.
unset COVERPROFILE MODULE_PREFIX PROJECT_NAME HTML_OUT \
    THRESHOLD_FUNC THRESHOLD_PACKAGE THRESHOLD_PRINT THRESHOLD_TOTAL UNCOVERED_LIMIT \
    EXCLUDE_PACKAGES SKIP_RESULT_PACKAGES EXCLUDE_FILES EXCLUDE_FUNCS \
    TEST_TIMEOUT BUILD_TAGS GENERATE_HTML RACE_DETECTION FAIL_ON_CRITICAL_BLOCKS

exec go run github.com/codetreker/go-cov/cmd/go-cov@v0.1.0 \
    --config .go-cov.toml --ci --coverprofile coverage.out
