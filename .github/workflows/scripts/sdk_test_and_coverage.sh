#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
cd "$PROJECT_ROOT/packages/sdks/client-ts"

# Per-file thresholds accommodate realtime transport paths; total coverage must
# remain at least 85% across the SDK.
THRESHOLD_FUNC=30.0
THRESHOLD_LINE=30.0
THRESHOLD_TOTAL=85.0
THRESHOLD_PRINT=90.0

mkdir -p "$PROJECT_ROOT/.tmp"
COVERAGE_OUTPUT=$(mktemp "$PROJECT_ROOT/.tmp/sdk-coverage.XXXXXX")
trap 'rm -f "$COVERAGE_OUTPUT"' EXIT

printf 'Running TypeScript SDK tests with coverage...\n'
bun test --coverage 2>&1 | tee "$COVERAGE_OUTPUT"

awk -F '|' \
    -v func_threshold="$THRESHOLD_FUNC" \
    -v line_threshold="$THRESHOLD_LINE" \
    -v total_threshold="$THRESHOLD_TOTAL" \
    -v print_threshold="$THRESHOLD_PRINT" '
function trim(value) {
    gsub(/^[[:space:]]+|[[:space:]]+$/, "", value)
    return value
}
function check(value, threshold, metric, file) {
    if (value < threshold) {
        printf "::error file=packages/sdks/client-ts/%s::%s coverage %.2f%% is below threshold %.2f%%\n", file, metric, value, threshold
        failed = 1
    }
}
BEGIN {
    printf "\nCoverage thresholds: %.2f%% functions/file, %.2f%% lines/file, %.2f%% total lines\n", func_threshold, line_threshold, total_threshold
}
{
    if (NF == 1) {
        next
    }
    file = trim($1)
    if (file != "All files" && file !~ /^src\//) {
        next
    }
    funcs = trim($2)
    lines = trim($3)
    if (funcs !~ /^[0-9]+([.][0-9]+)?$/ || lines !~ /^[0-9]+([.][0-9]+)?$/) {
        printf "::error::Invalid coverage row for %s\n", file
        failed = 1
        next
    }
    if (file == "All files") {
        total_seen = 1
        check(funcs + 0, func_threshold, "Total function", "")
        check(lines + 0, total_threshold, "Total line", "")
        printf "TOTAL: %.2f%% functions, %.2f%% lines\n", funcs, lines
    } else {
        source_seen = 1
        check(funcs + 0, func_threshold, "Function", file)
        check(lines + 0, line_threshold, "Line", file)
        if (funcs + 0 < print_threshold || lines + 0 < print_threshold) {
            printf "%s: %.2f%% functions, %.2f%% lines\n", file, funcs, lines
        }
        full_funcs += funcs + 0 == 100
        full_lines += lines + 0 == 100
    }
}
END {
    if (!total_seen || !source_seen) {
        print "::error::Coverage report is missing SDK source or total coverage"
        failed = 1
    }
    printf "Files with 100%% function coverage: %d\n", full_funcs
    printf "Files with 100%% line coverage: %d\n", full_lines
    if (failed) {
        print "::error::Coverage check failed"
        exit 1
    }
    print "Coverage check passed!"
}' "$COVERAGE_OUTPUT"
