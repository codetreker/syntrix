# Embedded Instance Administration UI

This package currently implements a React and TypeScript administration SPA served
by a Syntrix runtime at `/console/`. It owns its frontend dependencies and Vite
build. Its existing runtime login, logical-database, data, user, and rules views
describe the embedded UI, not an independent developer Console service.

The accepted [system architecture](../../docs/architecture.md) separates the
employee-facing Management platform, developer Console service, and application
instance runtimes. Developers use Console to create and manage one or more own
instances. Each instance owns projects with isolated enduser realms and multiple
logical databases per project; its Identity module owns both OAuth/OIDC issuing
and external-provider login. Application end users use the project runtime through
their application.

Those product boundaries, instance provisioning, project realms, and OAuth/OIDC
capabilities are not implemented by this package. The
[Console design](../../docs/design/server/console/01.console.md) distinguishes the
target from the current embedded UI; the
[boundary decision](../../.agents/notes/proposed/architecture/2026-10-10-platform-console-instance-boundaries.md)
owns the remaining work.

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
frontend origin through CORS. This endpoint selects an existing runtime; it does
not provision an instance or establish a project identity realm.

## Build and Serve

From `packages/console`:

```bash
bun run build
```

The build checks TypeScript and writes assets to `dist/`. Build and start the Go
runtime from the repository root:

```bash
make -C packages/syntrix build
(cd packages/syntrix && ./bin/syntrix --standalone)
```

Open `http://localhost:8080/console/`. With `packages/syntrix` as the runtime
working directory, the gateway reads `../console/dist`, serves generated assets,
and returns the SPA entry point for nested console routes. The Go build does
not generate frontend assets. The [package layout decision](../../.agents/notes/implemented/architecture/2026-10-09-package-layout.md)
owns working-directory boundaries; the [build and delivery proposal](../../.agents/notes/proposed/process/2026-09-07-console-build-delivery.md)
owns the remaining combined distribution work. These commands describe the
embedded UI and do not determine deployment of the future Console service.

## Checks

Run from `packages/console`:

```bash
bun run build
bun run lint
```

The package uses shared SDK source from `packages/sdks/client-ts`; keep that
sibling package present when building the console. Current runtime bearer-token
and browser-storage behavior is not a specified account protocol for the future
Console service.
