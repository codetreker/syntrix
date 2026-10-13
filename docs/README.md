# Syntrix Documentation

Syntrix provides document storage, queries, realtime subscriptions, and server-side
triggers. This guide links its implementation, accepted architecture, design,
reference, and workflow material.

## Architecture and Terminology

Read [Platform Architecture](architecture.md) first. It owns the employee-facing
Management platform, developer-facing Console, and developer-owned runtime
instances serving application end users. Every instance owns projects; each
project has an isolated Identity realm and can use multiple logical Syntrix
databases.

Instance PostgreSQL is private system storage for projects, application users,
OAuth and session state, and logical-database configuration. MongoDB stores
developer business documents. Private per-instance databases and scoped
credentials may use shared database servers. The planned
[Console instance lifecycle](design/server/console/03.instance_lifecycle.md)
allocates those databases and distributed trigger artifacts from
administrator-registered infrastructure. The architecture's implementation-status
table distinguishes this accepted target from current runtime accounts, schemas, APIs,
and the Console test-workload service.

## Project Structure

```text
syntrix/
├── packages/
│   ├── syntrix/             # Go instance runtime and server tools
│   │   ├── cmd/             # Server, CLI, and benchmark entrypoints
│   │   ├── internal/        # Storage, services, gateways, and configuration
│   │   ├── pkg/             # Shared models and benchmark components
│   │   ├── api/             # Protocol definitions and generated code
│   │   ├── tests/           # Integration tests
│   │   ├── configs/         # Runtime configuration and rule examples
│   │   ├── deployment/      # Standalone/distributed Kubernetes templates and values
│   │   ├── scripts/         # Protocol generation and Go coverage helpers
│   │   ├── go.mod           # Go module dependencies
│   │   └── Makefile         # Go build, test, coverage, and generation targets
│   ├── sdks/client-ts/      # TypeScript SDK
│   ├── console-server/      # Developer Console backend and Kubernetes provisioning
│   ├── console-client/      # Bilingual developer login/shell and static distribution
│   └── examples/realtime-demo/ # Browser replica example
├── deployment/              # Development and CI infrastructure
├── scripts/                 # Repository maintenance helpers
├── docs/                    # Designs, references, local plans, and retained reports
└── .agents/                 # Repository skills and decision notes
```

Packages own their build configuration and dependency manifests. The instance Go module
is `github.com/codetreker/syntrix`; Console owns an independent Go module and the
SDK package is `@syntrixbase/client`.
The [package layout decision](../.agents/notes/implemented/architecture/2026-10-09-package-layout.md)
records the build and working-directory boundaries.

## Getting Started

The commands below operate the current instance runtime. The Go module requires
Go 1.24 or later. The supplied configuration uses MongoDB for documents and
revocation, and PostgreSQL for users and database metadata. Project identity and
OAuth/session system records have not yet been implemented in the target form.
MongoDB must run as a replica set for change streams. Distributed trigger
delivery uses NATS; standalone delivery uses in-memory pubsub.

See the [development environment](../deployment/dev/README.md) for infrastructure
setup and the [configuration](../packages/syntrix/configs/config.yml) for connection
settings. Run these commands from the repository root:

```bash
make -C packages/syntrix build
(cd packages/syntrix && ./bin/syntrix --standalone)
```

Standalone runs an instance's services together with direct in-process calls.
When `deployment.mode` is `distributed`, services use gRPC, including when they
share a process:

```bash
(cd packages/syntrix && ./bin/syntrix --all)
(cd packages/syntrix && ./bin/syntrix --api)
```

Service-selection flags do not override a configured standalone mode. These
runtime modes do not describe the Management/Console/instance product layers;
see the [deployment design](design/server/03.deployment_modes.md).

The package-owned [Dockerfile](../packages/syntrix/Dockerfile) builds only the
instance runtime for Linux amd64/arm64. The [manual release guide](../packages/syntrix/deployment/RELEASE.md)
owns versioning, public GHCR publication, and deployment archives with matching
Linux CLIs and build metadata. The [instance deployment guide](../packages/syntrix/deployment/README.md)
owns standalone/distributed Kubernetes templates and the `syntrix-cli
render-kubernetes` command. Strict deployment values select namespace, workload
names, replicas, resource requests/limits, and existing backend connections. The
planned Console lifecycle selects the newest complete successfully published
Syntrix package at instance creation, uses its matching CLI/templates, and records
the immutable package and image digest for every initialization/deployment phase.
A main merge or PR build alone does not publish a runtime release.
Distributed mode also requires existing NATS JetStream and an operator-managed
shared signing-key Secret. Explicit bootstrap uses a temporary non-restarting Pod
while normal Deployments remain stopped. These artifacts do not add full-instance
provisioning to Console's API.

Run instance binaries with `packages/syntrix` as the working directory to use the
supplied configuration and package-local data and logs. Instance configuration loads
defaults, `configs/config.yml`, `configs/config.local.yml`, then supported
environment overrides. Select another directory with `--config-dir` or
`SYNTRIX_CONFIG_DIR`.

## Developer Console

The [Console server](../packages/console-server/README.md) runs separately from the
instance runtime. Its PostgreSQL owns developer accounts, sessions, workload
ownership, accepted deployment intent, template snapshots, and operation records.
Its API currently creates, observes, and deletes predefined Kubernetes HTTP test
workloads. The independent [Console client](../packages/console-client/README.md)
delivers bilingual developer login, session restoration, logout, and an
authenticated Overview. Workload administration pages and Console-managed
instances remain target work. The
[managed-instance design and task sequence](design/server/console/03.instance_lifecycle.md)
cover administrator-registered shared MongoDB/PostgreSQL/NATS, private database
allocation, runtime lifecycle, revisioned configuration, and bilingual instance
UI in five implementation PRs. Database/NATS server deployment, employee
Management, and project/OAuth capabilities retain separate scope. The former
embedded administration frontend and Gateway static route are removed.

Local deployment uses an operator-provided Kubernetes cluster, such as k3d, with
an explicit kubeconfig/context and configured namespace. Console does not manage
cluster installation or lifecycle. Browser development uses a fixed
`http://localhost:5173` origin with a same-origin proxy to backend 8090; set
backend `server.public_origin` to that browser origin. Production static delivery
preserves the same cookie/Origin/CSRF contract behind the public HTTPS origin.
See the [Console design](design/server/console/01.console.md)
and [API reference](reference/console_api.md).

## Validation

From the repository root:

```bash
make -C packages/syntrix test
make -C packages/syntrix coverage
CI=true make -C packages/syntrix coverage
make -C packages/syntrix generate
```

These commands cover Go packages, including integration tests that require the
configured infrastructure. The [pipeline environment](../deployment/pipeline/README.md)
describes those services and the CI checks. SDK build scripts live in its
`package.json`; the SDK uses pnpm for locked dependencies and Bun for scripts.
The demo uses Bun. Console client owns pnpm-locked dependencies and Bun scripts
for catalogs, types, behavior/coverage checks, and production builds. Console
server owns separate Go build/test/coverage targets.
Its `make test` and `make coverage` share an enforced go-cov gate: total 90%,
package 85%, function 80%, race detection, and critical uncovered-block checks.
Provide `CONSOLE_TEST_POSTGRES_DSN` for that gate; the
[Console README](../packages/console-server/README.md) owns database setup and
commands. These checks run regardless of the ambient `CI` setting.

From `packages/console-client`, install with `pnpm install --frozen-lockfile`
and run `bun run check` for translation, type, behavior/coverage, and build gates.
`bun run test:browser` separately exercises real account flows against dedicated
PostgreSQL and the built same-origin web distribution.
`bun run test:browser:cleanup` verifies SIGTERM stops acquisition descendants
and prevents late resources after the nested run exits. Its
[README](../packages/console-client/README.md) owns fixture/browser prerequisites;
mockup evidence does not establish real-cookie or HTTPS acceptance.

`make -C packages/syntrix coverage` reports coverage. Setting `CI=true` also
enables race detection and enforces the CI coverage thresholds.

## Design and Reference

- [Platform Architecture and Implementation Status](architecture.md)
- [Syntrix Container, Kubernetes Templates, and Manifest Generation](../packages/syntrix/deployment/README.md)
- [Manual Syntrix Releases](../packages/syntrix/deployment/RELEASE.md)
- [Developer Console](design/server/console/01.console.md)
- [Console Managed-Instance Lifecycle and Implementation Tasks](design/server/console/03.instance_lifecycle.md)
- [Managed Runtime Contract](design/server/console/04.managed_runtime_contract.md)
- [Employee Management Platform](design/server/console/02.control_plane.md)
- [Instance Identity](design/server/core/identity/01.architecture.md)
- [Gateway Document Authorization](design/server/gateway/authorization.md)
- [Design Documents](design/README.md)
- [Server Design](design/server/)
- [SDK Design](design/sdk/)
- [Monitoring Design](design/monitor/)
- [Instance REST API](reference/api.md)
- [Console API](reference/console_api.md)
- [Filter Syntax](reference/filters.md)
- [Trigger Rules](reference/trigger_rules.md)
- [TypeScript SDK](reference/typescript_sdk.md)
- [Browser Replica Demo](../packages/examples/realtime-demo/README.md)
- [Replication](reference/replication.md)

Design documents distinguish accepted target boundaries, proposed mechanisms,
and implemented contracts. API and SDK references describe current consumer
interfaces; a target architecture does not establish new routes or methods.

## Plans, Decisions, and Agent Workflows

Local execution plans live in the gitignored `docs/plans/` directory.
Existing reports in `docs/tasks/` retain validation history.
[Agent Notes](../.agents/notes/README.md) preserve decisions,
alternatives, and consequences. [Proposed notes](../.agents/notes/proposed/) record
deferred work with its evidence, alternatives, acceptance criteria, and risks;
proposal status does not imply an implementation commitment. Keep concise Why
alongside How in design discussions and link to the owning note for rationale.

[AGENTS.md](../AGENTS.md) defines the repository workflow and lists the skills in
`.agents/skills/`. Claude discovers the same skills through `.claude/skills`.
The note directory owns its [local instructions](../.agents/notes/AGENTS.md).
