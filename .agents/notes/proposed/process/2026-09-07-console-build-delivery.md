# Agent Note: Console Build and Delivery

Status: proposed

## Problem

The [gateway](../../../../packages/syntrix/internal/gateway/server.go) serves the
console from `../console/dist` relative to the `packages/syntrix` working
directory, while [make -C packages/syntrix build](../../../../packages/syntrix/Makefile)
only builds Go executables. [Server CI](../../../../.github/workflows/syntrix-server.yml)
invokes that target without building the console. The
[console package](../../../../packages/console/package.json) supplies
`tsc -b && vite build`, and its [README](../../../../packages/console/README.md)
documents separate frontend and Go builds. A successful server build therefore
does not establish that its console assets exist or match the source. The
[package layout decision](../../implemented/architecture/2026-10-09-package-layout.md)
defines development locations; a combined distribution contract remains open.

## Proposal

Make the delivered server-and-console artifact reproducible from a clean checkout. Add an explicit console build target using the repository's frontend tooling and lockfile, and make the full distribution target build and package those assets with the server. Keep a clearly named Go-only development target for consumers that intentionally need only the backend.

Define the runtime asset location independently of an accidental shell working directory. Package assets at that documented location or embed them after generation; choose one distribution contract and validate it in CI. A console-enabled distribution must fail its build if assets are absent or frontend type checking fails. Intentional API-only operation needs an explicit documented build or runtime mode.

Cover console source, configuration, lockfile, build scripts, and gateway static delivery in workflow triggers. Validate the packaged result by requesting the console entry point, a nested SPA route, and a generated asset from the actual artifact layout. Rewrite the console README and root build instructions around supported development and distribution commands.

## Alternatives

**Embed generated assets in the Go binary:** simplifies runtime placement but couples Go compilation to asset generation and increases binary size. It remains viable if the release contract is a single executable.

**Ship a static directory beside the binary:** keeps assets inspectable and independently cacheable, but the packaging and runtime path must be explicit. This is the closest fit to the existing gateway and is the proposed default pending distribution requirements.

## Acceptance Criteria

- A clean full build produces a server distribution that serves the console, its nested routes, and its generated assets without a manually prepared `dist` directory.
- A frontend type error or missing generated asset fails the relevant CI check.
- Console-only changes trigger the build and delivery checks.
- The documented API-only path and full distribution path both run from their declared layouts with bounded verification commands.

## Risks

Frontend generation adds build time and requires a pinned toolchain. Stale cached assets can produce mixed releases, so packaging must associate server and console outputs from the same build.
