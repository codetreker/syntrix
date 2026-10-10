# Instance Test Infrastructure

This lightweight Compose stack supplies dependencies for local integration tests
and CI. The [system architecture](../../docs/architecture.md) and
[boundary decision](../../.agents/notes/proposed/architecture/2026-10-10-platform-console-instance-boundaries.md)
separate employee Management, developer Console, and application instances.
Starting these services exercises the current instance runtime; it does not
provision those future platform products or project identity realms.

## Services

| Service | Port | Current responsibility |
|---|---|---|
| MongoDB | 27017 | Business documents, replica-set change streams, current token revocation |
| PostgreSQL | 5432 | Instance users and logical-database metadata |
| NATS | 4222, 8222 | Distributed trigger broker and monitoring |

The accepted target moves all private instance system data, including projects,
application identity/OAuth/provider/client/session state, and database
configuration/metadata, into instance PostgreSQL. Developer business documents
remain in MongoDB. Current global JWT users and Mongo revocation are explicit
implementation gaps; PostgreSQL is not a global employee/developer account store.

## Usage and connections

From the repository root:

```bash
docker compose -f deployment/pipeline/docker-compose.yml up -d
docker compose -f deployment/pipeline/docker-compose.yml ps
docker compose -f deployment/pipeline/docker-compose.yml down
```

- MongoDB: `mongodb://localhost:27017`
- PostgreSQL: `postgres://syntrix:syntrix@localhost:5432/syntrix?sslmode=disable`
- NATS: `nats://localhost:4222`

Wait for the configured service health checks before integration tests. This
stack has faster checks and smaller resource settings than development, and no
Prometheus/Grafana stack. No named persistence volumes are configured; image
anonymous volumes can still retain local test data. Stopping services is not a
coordinated backup or an isolation test for the accepted hierarchy.

## File-descriptor requirements

MongoDB sets soft/hard `nofile` limits to 64,000. Data files, journals, and
connections consume descriptors; inheriting a Docker soft limit of 1,024 can
exhaust them during test collection/index creation and stop WiredTiger. These
limits follow [MongoDB resource-limit guidance](https://www.mongodb.com/docs/manual/reference/ulimit/)
and keep test reliability independent of the daemon default. Development and
devcontainer MongoDB use the same setting. Recreate a running container after
changing limits so its process inherits them.

## Go CI checks

The current server workflow runs build and race/coverage jobs in parallel, each
with a five-minute timeout. Only the test job starts this stack. Tests do not
require build artifacts; generated protocol sources are committed. Separate
jobs keep cold-cache build compilation out of the race-test budget.

The required `Syntrix Server (Go)` check succeeds only when both jobs succeed;
it executes and rejects failed, cancelled, or skipped dependencies. Pinned
go-cov enforces race detection, package/function/overall coverage, and critical
uncovered blocks in CI.

```bash
make -C packages/syntrix coverage
CI=true make -C packages/syntrix coverage
```

The first command reports local coverage; the second enforces the CI thresholds.
Passing current runtime tests does not prove employee/developer token separation,
project realms, OAuth/OIDC flows, or separate Console/Management deployment.
Those contracts require validation with their future implementations.
