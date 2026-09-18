#!/bin/bash
# TypeScript SDK test coverage check script for CI
# Mirrors the Go coverage script behavior with similar thresholds

set -o pipefail

# Coverage thresholds
# Note: TypeScript SDK has some WebSocket/realtime code that's harder to test
# Thresholds are set to match current coverage levels, to be increased over time
THRESHOLD_FUNC=30.0      # Function coverage threshold per file
THRESHOLD_LINE=30.0      # Line coverage threshold per file
THRESHOLD_TOTAL=85.0     # Total line coverage threshold
THRESHOLD_PRINT=90.0     # Only print files below this threshold

cd "$(dirname "$0")/../../../sdk/syntrix-client-ts"

echo "Running TypeScript SDK tests with coverage..."
echo "---------------------------------------------------------------------------------------------------------"

# Run tests with coverage and capture output
COVERAGE_OUTPUT=$(bun test --coverage 2>&1)
TEST_EXIT_CODE=$?

# Print test results
echo "$COVERAGE_OUTPUT" | grep -E "^\(pass\)|\(fail\)|pass|fail|tests|expect"

if [ $TEST_EXIT_CODE -ne 0 ]; then
    printf '%s\n' "$COVERAGE_OUTPUT"
    echo "::error::Tests failed with exit code $TEST_EXIT_CODE"
    exit $TEST_EXIT_CODE
fi

echo ""
echo "---------------------------------------------------------------------------------------------------------"
echo "Coverage Report:"
echo "---------------------------------------------------------------------------------------------------------"

# Extract coverage table (filter only src/ files, exclude dist/)
COVERAGE_TABLE=$(echo "$COVERAGE_OUTPUT" | grep -E "^[| ]*(All files|src/)" | grep -v "dist/")

# Track failures
FAILED=0

# Process All files line for total coverage
TOTAL_LINE=$(echo "$COVERAGE_OUTPUT" | grep "All files")
if [ -n "$TOTAL_LINE" ]; then
    TOTAL_FUNCS=$(echo "$TOTAL_LINE" | awk '{print $4}')
    TOTAL_LINES=$(echo "$TOTAL_LINE" | awk '{print $6}')
    
    echo ""
    printf "%-60s %-15s %-15s\n" "FILE" "% FUNCS" "% LINES"
    echo "---------------------------------------------------------------------------------------------------------"
fi

# Process each source file line
echo "$COVERAGE_OUTPUT" | grep -E "^ src/" | while read -r line; do
    FILE=$(echo "$line" | awk '{print $1}')
    FUNC_COV=$(echo "$line" | awk '{print $3}')
    LINE_COV=$(echo "$line" | awk '{print $5}')
    UNCOVERED=$(echo "$line" | awk '{$1=$2=$3=$4=$5=""; print $0}' | sed 's/^ *//')
    
    # Remove % sign for comparison
    FUNC_NUM=$(echo "$FUNC_COV" | sed 's/%//')
    LINE_NUM=$(echo "$LINE_COV" | sed 's/%//')
    
    # Check thresholds
    FUNC_CRITICAL=""
    LINE_CRITICAL=""
    
    if (( $(echo "$FUNC_NUM < $THRESHOLD_FUNC" | bc -l) )); then
        FUNC_CRITICAL=" (CRITICAL: < ${THRESHOLD_FUNC}%)"
        echo "::error file=${FILE}::Function coverage ${FUNC_COV} is below threshold ${THRESHOLD_FUNC}%"
        FAILED=1
    fi
    
    if (( $(echo "$LINE_NUM < $THRESHOLD_LINE" | bc -l) )); then
        LINE_CRITICAL=" (CRITICAL: < ${THRESHOLD_LINE}%)"
        echo "::error file=${FILE}::Line coverage ${LINE_COV} is below threshold ${THRESHOLD_LINE}%"
        FAILED=1
    fi
    
    # Only print files below print threshold
    if (( $(echo "$FUNC_NUM < $THRESHOLD_PRINT" | bc -l) )) || (( $(echo "$LINE_NUM < $THRESHOLD_PRINT" | bc -l) )); then
        printf "%-60s %-15s %-15s\n" "$FILE" "${FUNC_COV}${FUNC_CRITICAL}" "${LINE_COV}${LINE_CRITICAL}"
        if [ -n "$UNCOVERED" ]; then
            echo "   Uncovered lines: $UNCOVERED"
        fi
    fi
done

echo "---------------------------------------------------------------------------------------------------------"

# Check total coverage
if [ -n "$TOTAL_LINE" ]; then
    TOTAL_FUNCS=$(echo "$TOTAL_LINE" | awk '{print $4}')
    TOTAL_LINES=$(echo "$TOTAL_LINE" | awk '{print $6}')
    
    TOTAL_FUNCS_NUM=$(echo "$TOTAL_FUNCS" | sed 's/%//')
    TOTAL_LINES_NUM=$(echo "$TOTAL_LINES" | sed 's/%//')
    
    TOTAL_FUNC_STATUS=""
    TOTAL_LINE_STATUS=""
    
    if (( $(echo "$TOTAL_FUNCS_NUM < $THRESHOLD_FUNC" | bc -l) )); then
        TOTAL_FUNC_STATUS=" (CRITICAL: < ${THRESHOLD_FUNC}%)"
        echo "::error::Total function coverage ${TOTAL_FUNCS} is below threshold ${THRESHOLD_FUNC}%"
        FAILED=1
    fi
    
    if (( $(echo "$TOTAL_LINES_NUM < $THRESHOLD_TOTAL" | bc -l) )); then
        TOTAL_LINE_STATUS=" (CRITICAL: < ${THRESHOLD_TOTAL}%)"
        echo "::error::Total line coverage ${TOTAL_LINES} is below threshold ${THRESHOLD_TOTAL}%"
        FAILED=1
    fi
    
    printf "%-60s %-15s %-15s\n" "TOTAL" "${TOTAL_FUNCS}${TOTAL_FUNC_STATUS}" "${TOTAL_LINES}${TOTAL_LINE_STATUS}"
fi

echo "---------------------------------------------------------------------------------------------------------"

# Statistics
echo ""
echo "Statistics:"
COUNT_100_FUNC=$(echo "$COVERAGE_OUTPUT" | grep -E "^ src/" | awk '$3 == "100.00"' | wc -l)
COUNT_100_LINE=$(echo "$COVERAGE_OUTPUT" | grep -E "^ src/" | awk '$5 == "100.00"' | wc -l)
echo "Files with 100% function coverage: $COUNT_100_FUNC"
echo "Files with 100% line coverage: $COUNT_100_LINE"

# Final check - re-parse to get failure status
FINAL_FAILED=0

# Check all src files for threshold violations
while IFS= read -r line; do
    if [ -z "$line" ]; then continue; fi
    
    FUNC_COV=$(echo "$line" | awk '{print $3}')
    LINE_COV=$(echo "$line" | awk '{print $5}')
    
    FUNC_NUM=$(echo "$FUNC_COV" | sed 's/%//')
    LINE_NUM=$(echo "$LINE_COV" | sed 's/%//')
    
    if (( $(echo "$FUNC_NUM < $THRESHOLD_FUNC" | bc -l) )); then
        FINAL_FAILED=1
    fi
    
    if (( $(echo "$LINE_NUM < $THRESHOLD_LINE" | bc -l) )); then
        FINAL_FAILED=1
    fi
done <<< "$(echo "$COVERAGE_OUTPUT" | grep -E "^ src/")"

# Check total
if [ -n "$TOTAL_LINE" ]; then
    TOTAL_LINES_NUM=$(echo "$TOTAL_LINE" | awk '{print $6}' | sed 's/%//')
    if (( $(echo "$TOTAL_LINES_NUM < $THRESHOLD_TOTAL" | bc -l) )); then
        FINAL_FAILED=1
    fi
fi

if [ $FINAL_FAILED -ne 0 ]; then
    echo ""
    echo "::error::Coverage check failed - some files are below threshold"
    exit 1
fi

echo ""
echo "Coverage check passed!"
exit 0
