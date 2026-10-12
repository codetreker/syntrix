# Agent Note: Package-owned repository layout

Status: implemented

## Problem

The server, SDK, console, and example application have different dependency
manifests and build requirements. Keeping their sources at unrelated root paths
makes repository scripts and CI depend on an implicit root Go module and makes
cross-package references easy to break when a component moves.

## Decision

Each component owns its sources, dependencies, and build configuration under
`packages/`:

| Package | Responsibility |
|---|---|
| `packages/syntrix/` | Go server, CLI, benchmark, protocol definitions, runtime configuration, integration tests, and Go tooling |
| `packages/sdks/client-ts/` | TypeScript client SDK and its build, test, and packaging scripts |
| `packages/console-server/` | Independent developer Console Go module, PostgreSQL source data, and Kubernetes test-workload provisioning |
| `packages/console-client/` | Independent React/Lingui developer login/shell, package checks, and static web image |
| `packages/examples/realtime-demo/` | Browser replica demonstration |

Repository infrastructure, shared maintenance scripts, workflows, documentation,
and agent decisions remain at the root. The [documentation guide](../../../../docs/README.md)
owns navigation and commands; package READMEs own component usage.

Go commands execute from `packages/syntrix`, including the [Makefile](../../../../packages/syntrix/Makefile)
and protocol and coverage helpers. From the repository root, use
`make -C packages/syntrix <target>`. Build outputs belong to the package's `bin/`
directory. Run its server binary with the package as the working directory so
relative configuration, data, and logging paths resolve consistently.

The Go module is `github.com/codetreker/syntrix`, replacing
`github.com/syntrixbase/syntrix`; Go imports and generated protocol package
identities follow that module path without the repository's `packages/` prefix.
The SDK package is `@syntrixbase/client`, replacing `@syntrix/client`; package
imports, local dependencies, and package verification use the new identity.
The source layout and package identities do not change runtime API routes,
protocol formats, or stored data.

Protocol sources declare the new module identity in `go_package`. Their generated
Go files are regenerated from those sources because the module path also appears
in serialized file-descriptor options; changing import text alone would leave
runtime descriptors inconsistent with their source. Protocol field numbers,
message shapes, and service methods retain their existing wire contracts.

The earlier layout included an embedded administration SPA whose frontend build
was separate from the Go server. The [Console server decision](2026-10-11-console-server-postgres-kubernetes.md)
replaces that package and static route with a separate developer Go module.
Console owns its build/test/configuration and uses separate PostgreSQL source
data; no combined frontend/runtime artifact remains. The [Console client
decision](2026-10-12-console-client-session-and-distribution.md) delivers a sibling
frontend package with its own pnpm lockfile, Bun scripts, and static image. Its
same-origin proxy forwards auth/API traffic to the API-only Go service; neither
Go build reads sibling assets nor frontend build produces an instance artifact.
The [instance deployment decision](2026-10-12-instance-container-kubernetes.md)
adds a package-owned Docker build context and service-only Kubernetes manifest under
`packages/syntrix`. The image contains only the instance binary; Kubernetes mounts
configuration assets separately, keeping credentials and generated signing keys
outside the image.
The [rejected build
proposal](../../rejected/process/2026-09-07-console-build-delivery.md) preserves
the former distribution concern.

Workflows and local scripts resolve their repository and package directories
explicitly. Cross-package build inputs, local SDK dependencies, documentation
links, and CI path filters follow the new locations.

The package's `scripts/coverage.sh` and `scripts/coverage.cmd` own Go coverage
invocation. Both use pinned `go-cov` tooling, load the package-local coverage
configuration, and write `packages/syntrix/coverage.out`. The Makefile, pre-push
hook, and CI helper invoke these entry points so their report path and coverage
rules stay consistent. The root `scripts/uncovered_blocks.sh` helper is removed:
its `scripts/lib/uncovered_blocks` implementation was absent, and uncovered-block
reporting belongs to the pinned coverage tool.

Repository skills name the owning package when running Go build, test, or
coverage commands. The original frontend layout separated route wrappers and
toast contexts to satisfy Fast Refresh and hook lint rules; that frontend was
subsequently removed by the separate Console server decision.

Development, pipeline, and devcontainer MongoDB services set both `nofile` limits
to 64,000 according to
[MongoDB resource-limit guidance](https://www.mongodb.com/docs/manual/reference/ulimit/).
The inherited Docker default of 1,024 exhausted file descriptors during
integration-test collection and index creation, causing a WiredTiger panic.
Explicit container limits keep local and CI tests independent of that host
default. The [pipeline guide](../../../../deployment/pipeline/README.md) and
[development guide](../../../../deployment/dev/README.md) own infrastructure
setup and the process-recreation requirement.

## Alternatives

**A root Makefile forwarding package commands:** Forwarding targets would retain
a second command entry point that must track the package Makefile. Commands name
the owning package directly with `make -C`, keeping target definitions with the
Go module they operate on.

## Consequences

Packages can run their existing build and test tooling from their own dependency
roots. Repository-level automation must choose the relevant package directory
before invoking tools; the repository root is no longer a Go module.

Consumers must update Go module references and TypeScript package imports to the
new identities. Package manifests and local dependencies use those identities
consistently; directory names do not become part of public module imports.

Relative runtime paths continue to depend on the process working directory.
Launching the server from another directory requires explicit configuration and
appropriate data and log paths. Deployment infrastructure remains
repository-owned for shared development and pipeline dependencies, so package
tooling references those stacks through the repository root. Package deployment
artifacts remain with the service they distribute.

Coverage reports remain inside the Go package. Automation consuming a report
must use that location rather than assume a repository-root `coverage.out`.

Active documents and notes follow moved source locations. Commit-pinned external
references and historical archives retain their original paths as historical
evidence.
