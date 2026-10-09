# Syntrix Documentation

Syntrix provides document storage, queries, realtime subscriptions, and server-side
triggers. This guide links its implementation, design, reference, and workflow
material.

## Project Structure

```text
syntrix/
├── packages/
│   ├── syntrix/             # Go module and server tools
│   │   ├── cmd/             # Server, CLI, and benchmark entrypoints
│   │   ├── internal/        # Storage, services, gateways, and configuration
│   │   ├── pkg/             # Shared models and benchmark components
│   │   ├── api/             # Protocol definitions and generated code
│   │   ├── tests/           # Integration tests
│   │   ├── configs/         # Runtime configuration and rule examples
│   │   ├── scripts/         # Protocol generation and Go coverage helpers
│   │   ├── go.mod           # Go module dependencies
│   │   └── Makefile         # Go build, test, coverage, and generation targets
│   ├── sdks/client-ts/      # TypeScript SDK
│   ├── console/             # Web administration console
│   └── examples/realtime-demo/ # Browser replica example
├── deployment/              # Development and CI infrastructure
├── scripts/                 # Repository maintenance helpers
├── docs/                    # Designs, references, plans, and task tracking
└── .agents/                 # Repository skills and decision notes
```

Packages own their build configuration and dependency manifests. The Go module
is `github.com/codetreker/syntrix`, and the SDK package is `@syntrixbase/client`.
The [package layout decision](../.agents/notes/implemented/architecture/2026-10-09-package-layout.md)
records the build and working-directory boundaries.

## Getting Started

The Go module requires Go 1.24 or later. The supplied configuration uses MongoDB
for documents and PostgreSQL for users and database metadata. MongoDB must run
as a replica set for change streams. Distributed trigger delivery uses NATS;
standalone delivery uses in-memory pubsub.

See the [development environment](../deployment/dev/README.md) for infrastructure
setup and the [configuration](../packages/syntrix/configs/config.yml) for connection
settings. Run these commands from the repository root:

```bash
make -C packages/syntrix build
(cd packages/syntrix && ./bin/syntrix --standalone)
```

Standalone runs the services together with direct in-process calls. Distributed
mode uses gRPC between services, including when they share a process:

```bash
(cd packages/syntrix && ./bin/syntrix --all)
(cd packages/syntrix && ./bin/syntrix --api)
```

Run server binaries with `packages/syntrix` as the working directory to use the
supplied configuration, package-local data and logs, and console assets from
`packages/console/dist`. Build the console separately with `bun run build` from
`packages/console` before using its UI. Configuration loads
defaults, `configs/config.yml`, `configs/config.local.yml`, then supported
environment overrides. Select another directory with `--config-dir` or
`SYNTRIX_CONFIG_DIR`.

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
describes those services and the CI checks. SDK and console build scripts live
in their own `package.json` files. The SDK uses pnpm for its locked dependencies
and Bun for its scripts; the console and demo use Bun.

`make -C packages/syntrix coverage` reports coverage. Setting `CI=true` also
enables race detection and enforces the CI coverage thresholds.

## Design and Reference

- [System Architecture](architecture.md)
- [Design Documents](design/README.md)
- [Server Design](design/server/)
- [SDK Design](design/sdk/)
- [Monitoring Design](design/monitor/)
- [REST API](reference/api.md)
- [Filter Syntax](reference/filters.md)
- [Trigger Rules](reference/trigger_rules.md)
- [TypeScript SDK](reference/typescript_sdk.md)
- [Browser Replica Demo](../packages/examples/realtime-demo/README.md)
- [Replication](reference/replication.md)

Design documents include proposals as well as implemented mechanisms; read their
status and check the implementation before relying on a proposed capability.

## Plans, Decisions, and Agent Workflows

Execution plans live in `docs/plans/`; the [task board](tasks/BOARD.md) tracks
ownership and status. [Agent Notes](../.agents/notes/README.md) preserve decisions,
alternatives, and consequences. [Proposed notes](../.agents/notes/proposed/) record
deferred work with its evidence, alternatives, acceptance criteria, and risks;
proposal status does not imply an implementation commitment. Keep concise Why
alongside How in design discussions and link to the owning note for rationale.

[AGENTS.md](../AGENTS.md) defines the repository workflow and lists the skills in
`.agents/skills/`. Claude discovers the same skills through `.claude/skills`.
The note directory owns its [local instructions](../.agents/notes/AGENTS.md).
