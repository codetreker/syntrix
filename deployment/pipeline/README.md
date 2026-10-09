# Pipeline Infrastructure

Lightweight Docker Compose setup for CI/CD pipelines.

## Services

| Service    | Port  | Description                         |
|------------|-------|-------------------------------------|
| MongoDB    | 27017 | Document storage (replica set)      |
| PostgreSQL | 5432  | User storage                        |
| NATS       | 4222  | Message broker with JetStream       |

## Usage

```bash
# Start all services
docker compose -f deployment/pipeline/docker-compose.yml up -d

# Check status
docker compose -f deployment/pipeline/docker-compose.yml ps

# Stop all services
docker compose -f deployment/pipeline/docker-compose.yml down
```

## Connection Strings

- **MongoDB**: `mongodb://localhost:27017`
- **PostgreSQL**: `postgres://syntrix:syntrix@localhost:5432/syntrix?sslmode=disable`
- **NATS**: `nats://localhost:4222`

## Differences from Dev Environment

This setup is optimized for CI/CD:
- No persistent volumes (ephemeral storage)
- No monitoring stack (Prometheus, Grafana)
- Faster health check intervals
- Minimal resource allocation

MongoDB has explicit soft and hard `nofile` limits of 64,000. MongoDB uses file
descriptors for data files, journals, and connections; relying on a Docker daemon
default of 1,024 can exhaust descriptors during integration-test collection and
index creation, causing WiredTiger to stop the server. The container limits
follow [MongoDB resource-limit guidance](https://www.mongodb.com/docs/manual/reference/ulimit/)
and keep the test environment independent of the host daemon's default. Recreate
the MongoDB container after changing these limits; changing the Compose file does
not update an already running process.

## Go CI checks

The server workflow runs build and race/coverage jobs in parallel, each with a
five-minute timeout. Only the test job starts the services above. Tests do not
require build artifacts; generated protocol sources are committed. Separate jobs
keep cold-cache build compilation out of the race-test execution budget.

The required `Syntrix Server (Go)` check passes only when both jobs succeed. It
runs even if dependencies fail, are cancelled, or are skipped, and rejects those
results. Coverage uses a pinned go-cov version to enforce race detection, package,
function, and overall coverage, and critical uncovered blocks.

From the repository root, `make -C packages/syntrix coverage` reports coverage;
`CI=true make -C packages/syntrix coverage` enforces the same CI thresholds.
