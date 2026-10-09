#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
PACKAGE_ROOT="$PROJECT_ROOT/packages/syntrix"

# Colors for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

# Default values
CONFIG_FILE="$PACKAGE_ROOT/configs/benchmark.yaml"
TARGET=""
DURATION=""
WORKERS=""
NO_COLOR=""
REBUILD=false

# Print usage
usage() {
    echo "Syntrix Benchmark Runner Script"
    echo ""
    echo "Usage: $0 [options]"
    echo ""
    echo "Options:"
    echo "  -c, --config <file>     Configuration file (default: packages/syntrix/configs/benchmark.yaml)"
    echo "  -t, --target <url>      Target Syntrix URL (overrides config)"
    echo "  -d, --duration <time>   Benchmark duration (e.g., 10s, 1m) (overrides config)"
    echo "  -w, --workers <n>       Number of concurrent workers (overrides config)"
    echo "  --no-color              Disable colored output"
    echo "  --rebuild               Force rebuild before running"
    echo "  -h, --help              Show this help message"
    echo ""
    echo "Examples:"
    echo "  $0                                    # Run with default config"
    echo "  $0 --config packages/syntrix/configs/benchmark.yaml"
    echo "  $0 --target http://localhost:8080 --duration 30s --workers 10"
    echo "  $0 --rebuild                          # Force rebuild and run"
    echo ""
    exit 0
}

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        -c|--config)
            CONFIG_FILE="$2"
            shift 2
            ;;
        -t|--target)
            TARGET="$2"
            shift 2
            ;;
        -d|--duration)
            DURATION="$2"
            shift 2
            ;;
        -w|--workers)
            WORKERS="$2"
            shift 2
            ;;
        --no-color)
            NO_COLOR="--no-color"
            shift
            ;;
        --rebuild)
            REBUILD=true
            shift
            ;;
        -h|--help)
            usage
            ;;
        *)
            echo -e "${RED}Error: Unknown option $1${NC}"
            usage
            ;;
    esac
done

if [[ "$CONFIG_FILE" != /* ]]; then
    CONFIG_FILE="$PROJECT_ROOT/$CONFIG_FILE"
fi
cd "$PACKAGE_ROOT"

# Check if benchmark binary exists
BENCHMARK_BIN="bin/syntrix-benchmark"
if [ ! -f "$BENCHMARK_BIN" ] || [ "$REBUILD" = true ]; then
    echo -e "${YELLOW}Building benchmark tool...${NC}"
    make build-benchmark
    echo -e "${GREEN}✓ Build complete${NC}"
    echo ""
fi

# Build command arguments
CMD_ARGS=("run")

# Add config file if specified and exists
if [ -n "$CONFIG_FILE" ]; then
    if [ -f "$CONFIG_FILE" ]; then
        CMD_ARGS+=("--config" "$CONFIG_FILE")
    else
        echo -e "${YELLOW}Warning: Config file $CONFIG_FILE not found, using command-line options${NC}"
    fi
fi

# Add overrides
if [ -n "$TARGET" ]; then
    CMD_ARGS+=("--target" "$TARGET")
fi

if [ -n "$DURATION" ]; then
    CMD_ARGS+=("--duration" "$DURATION")
fi

if [ -n "$WORKERS" ]; then
    CMD_ARGS+=("--workers" "$WORKERS")
fi

if [ -n "$NO_COLOR" ]; then
    CMD_ARGS+=("$NO_COLOR")
fi

# Print execution info
echo -e "${GREEN}Starting Syntrix Benchmark${NC}"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
if [ -n "$CONFIG_FILE" ] && [ -f "$CONFIG_FILE" ]; then
    echo -e "${YELLOW}Configuration:${NC} $CONFIG_FILE"
fi
if [ -n "$TARGET" ]; then
    echo -e "${YELLOW}Target:${NC} $TARGET"
fi
if [ -n "$DURATION" ]; then
    echo -e "${YELLOW}Duration:${NC} $DURATION"
fi
if [ -n "$WORKERS" ]; then
    echo -e "${YELLOW}Workers:${NC} $WORKERS"
fi
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

# Execute benchmark
"./$BENCHMARK_BIN" "${CMD_ARGS[@]}"
