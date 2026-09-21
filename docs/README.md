# Syntrix Documentation

Syntrix provides document storage, queries, realtime subscriptions, and server-side
triggers. This guide links its implementation, design, reference, and workflow
material.

## Project Structure

```text
syntrix/
├── cmd/                     # Server, CLI, and benchmark entrypoints
├── internal/
│   ├── config/              # Configuration loading
│   ├── core/                # Storage, identity, database management, and pubsub
│   ├── gateway/             # REST, WebSocket, and SSE routes
│   ├── indexer/             # Secondary indexes
│   ├── puller/              # Storage change streams and event buffering
│   ├── query/               # Document operations and query execution
│   ├── server/              # Shared HTTP and gRPC servers
│   ├── services/            # Service composition and lifecycle
│   ├── streamer/            # Realtime subscription matching
│   └── trigger/             # Rule evaluation and webhook delivery
├── pkg/                     # Shared models and benchmark components
├── api/                     # Protocol definitions and generated code
├── sdk/syntrix-client-ts/    # TypeScript SDK
├── console/                 # Web administration console
├── example/                 # Example applications
├── tests/                   # Integration tests
├── configs/                 # Runtime configuration and rule examples
├── deployment/              # Development and CI infrastructure
├── scripts/                 # Build and validation helpers
├── docs/                    # Designs, references, plans, and task tracking
└── .agents/                 # Repository skills and decision notes
```

## Getting Started

The Go module requires Go 1.24 or later. The supplied configuration uses MongoDB
for documents and PostgreSQL for users and database metadata. MongoDB must run
as a replica set for change streams. Distributed trigger delivery uses NATS;
standalone delivery uses in-memory pubsub.

See the [development environment](../deployment/dev/README.md) for infrastructure
setup and the [configuration](../configs/config.yml) for connection settings.

```bash
make build
./bin/syntrix --standalone
```

Standalone runs the services together with direct in-process calls. Distributed
mode uses gRPC between services, including when they share a process:

```bash
./bin/syntrix --all
./bin/syntrix --api
```

Configuration loads defaults, `configs/config.yml`, `configs/config.local.yml`,
then supported environment overrides. Select another directory with
`--config-dir` or `SYNTRIX_CONFIG_DIR`.

## Validation

```bash
make test
make coverage
CI=true make coverage
```

These commands cover Go packages, including integration tests that require the
configured infrastructure. The [pipeline environment](../deployment/pipeline/README.md)
describes those services and the CI checks. SDK and console build scripts live in their own
`package.json` files and use Bun.

`make coverage` 报告覆盖率；`CI=true make coverage` 同时启用 race 检测，
并强制执行 CI 覆盖率门槛。

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
- [Browser Replica Demo](../example/realtime-demo/README.md)
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
