#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
DEMO_PORT=3000

usage() {
    printf '%s\n' \
        'Usage: ./start-demo.sh [--port PORT]' \
        '' \
        'Build the patched SDK and demo, then serve http://127.0.0.1:PORT/.' \
        'Configure the existing Syntrix endpoint and credentials in the page.' \
        '' \
        'Options:' \
        '  --port PORT  Frontend port, 1 through 65535 (default: 3000)' \
        '  -h, --help   Show this help' \
        '' \
        'Requires Bun and pnpm 10.34.5. Press Ctrl+C to stop the frontend.'
}

while (($#)); do
    case "$1" in
        -h|--help)
            usage
            exit 0
            ;;
        --port)
            if (($# < 2)) || [[ ! "$2" =~ ^[0-9]{1,5}$ ]] || ((10#$2 < 1 || 10#$2 > 65535)); then
                printf '%s\n' 'Error: --port requires an integer from 1 through 65535.' >&2
                exit 1
            fi
            DEMO_PORT=$((10#$2))
            shift 2
            ;;
        *)
            printf 'Error: unknown option %s. Use --help for usage.\n' "$1" >&2
            exit 1
            ;;
    esac
done

for tool in bun pnpm; do
    if ! command -v "$tool" >/dev/null 2>&1; then
        printf 'Error: %s is required.\n' "$tool" >&2
        exit 1
    fi
done

# Fail early without claiming or terminating an existing listener. The static
# server also fails if another process takes the port during the build.
bun -e 'const server = Bun.serve({ hostname: "127.0.0.1", port: Number(process.argv[1]), fetch: () => new Response() }); server.stop(true);' "$DEMO_PORT"

cd "$PROJECT_ROOT/sdk/syntrix-client-ts"
if [[ "$(pnpm --version)" != '10.34.5' ]]; then
    printf '%s\n' 'Error: the SDK build requires pnpm 10.34.5 to apply its pinned dependency patch.' >&2
    exit 1
fi
pnpm install --frozen-lockfile --ignore-scripts
bun run build

cd "$SCRIPT_DIR"
# Bun copies file dependencies. Reinstall after the SDK build so the demo uses
# the newly verified dist rather than an older installed package snapshot.
bun install --frozen-lockfile --force
bun run build

printf '\nFrontend: http://127.0.0.1:%s/\nConfigure the Syntrix endpoint in the page.\n\n' "$DEMO_PORT"
exec bun ./serve.ts "$DEMO_PORT"
