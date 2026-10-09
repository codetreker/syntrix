# Web Console

The administration console is a React and TypeScript application served under
`/console/`. It owns its frontend dependencies and Vite build in this package.
The [console design](../../docs/design/server/console/01.console.md) describes
both implemented capabilities and planned work.

## Development

Run from the repository root:

```bash
cd packages/console
bun install --frozen-lockfile
bun run dev
```

Open the Vite URL at `/console/`. The development server proxies `/auth`, `/api`,
and `/admin` requests to `http://localhost:8080`; start the backend separately
using the [development environment](../../deployment/dev/README.md).
API requests use the current origin by default. Set `VITE_API_URL` when building
or running Vite to select another API origin; that backend must allow the
frontend origin through CORS.

## Build and Serve

From `packages/console`:

```bash
bun run build
```

The build checks TypeScript and writes assets to `dist/`. Build and start the Go
server from the repository root:

```bash
make -C packages/syntrix build
(cd packages/syntrix && ./bin/syntrix --standalone)
```

Open `http://localhost:8080/console/`. With `packages/syntrix` as the server
working directory, the gateway reads `../console/dist`, serves generated assets,
and returns the SPA entry point for nested console routes. The Go build does
not generate frontend assets. The [package layout decision](../../.agents/notes/implemented/architecture/2026-10-09-package-layout.md)
owns working-directory boundaries; the [build and delivery proposal](../../.agents/notes/proposed/process/2026-09-07-console-build-delivery.md)
owns the remaining combined distribution work.

## Checks

Run from `packages/console`:

```bash
bun run build
bun run lint
```

The package uses shared SDK source from `packages/sdks/client-ts`; keep that
sibling package present when building the console.
