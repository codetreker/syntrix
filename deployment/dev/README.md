# Instance Development Environment

This Compose stack runs local instance infrastructure and development monitoring.
The [system architecture](../../docs/architecture.md) and
[boundary decision](../../.agents/notes/proposed/architecture/2026-10-10-platform-console-instance-boundaries.md)
separate employee Management, developer Console, and application instances.
This stack is not a deployment of the global Management platform or the separate
developer Console service. The current frontend embedded at `/console/` is
instance administration tooling.

## Services and data ownership

| Service | Port | Current local responsibility |
|---|---|---|
| MongoDB | 27017 | Business documents, replica-set change streams, current token revocation |
| PostgreSQL | 5432 | Instance user records and database metadata |
| NATS | 4222, 8222 | Distributed trigger delivery and broker monitoring |
| Prometheus | 9090 | Local scraping and metrics storage |
| Grafana | 3000 | Local dashboards |

In the accepted target, each developer owns one or more instances. Each instance
owns projects; one project has an isolated application identity realm and may
use multiple logical Syntrix databases. Instance PostgreSQL is private system
storage for projects, users, OAuth/providers/clients, sessions, and database
configuration/metadata. MongoDB holds developer business documents. Current
global users/JWTs and Mongo revocation do not implement that complete split.
Employee/developer accounts are distinct from instance application users.

## Start, inspect, and stop

From the repository root:

```bash
docker compose -f deployment/dev/docker-compose.yml up -d
docker compose -f deployment/dev/docker-compose.yml ps
docker compose -f deployment/dev/docker-compose.yml logs -f
docker compose -f deployment/dev/docker-compose.yml down
```

MongoDB uses soft/hard `nofile` limits of 64,000 because data files, journals, and
connections consume descriptors. An inherited limit of 1,024 can stop
WiredTiger during collection/index creation. The setting follows
[MongoDB resource-limit guidance](https://www.mongodb.com/docs/manual/reference/ulimit/)
and is shared by pipeline/devcontainer MongoDB. Recreate an existing container
after changing limits; the configuration alone does not change its process.

## Run the instance

```bash
make -C packages/syntrix build
(cd packages/syntrix && ./bin/syntrix --standalone)
```

The package working directory supplies default `configs/` and keeps runtime data
and logs with the server package. Standalone assembles services in one process;
it does not combine employee, developer, and application identity domains.
The commented application service in `docker-compose.yml` is an optional Docker
build template; it requires a matching application image/build recipe before it
can be used. The commands above run the current instance outside Docker.

## Local access and monitoring

| Surface | URL | Development credentials |
|---|---|---|
| Instance API | http://localhost:8080 | Current runtime authentication |
| Prometheus | http://localhost:9090 | Local development access |
| Grafana | http://localhost:3000 | admin/admin |
| NATS monitoring | http://localhost:8222 | Local development access |

Grafana auto-provisions the Syntrix Overview dashboard from
`grafana/dashboards/`. Prometheus is configured to scrape
`host.docker.internal:8080`; current runtime instrumentation is partial, so a
configured target does not prove all proposed `/metrics` collectors exist.
These local dashboards do not supply the future employee fleet monitor or
resource-authorized developer Console views.

Rules in `prometheus/alerts.yml` cover service loss, error rate above 5%, p95
latency above 500 ms/p99 above one second, Puller backpressure, and goroutine
pressure. Development `prometheus.yml` currently leaves rule evaluation disabled;
enabling the rule file is an explicit local operational action.

## Configuration locations

| Path under this directory | Purpose |
|---|---|
| `docker-compose.yml` | Infrastructure and monitoring services |
| `postgres/postgresql.conf` | PostgreSQL settings |
| `scripts/postgres-init.sql` | Current system-schema initialization |
| `scripts/mongo-init.js` | Replica-set initialization |
| `prometheus/prometheus.yml`, `prometheus/alerts.yml` | Scrapes and alert definitions |
| `grafana/provisioning/`, `grafana/dashboards/` | Datasource/dashboard provisioning |

## Troubleshooting

On Linux, if the configured host name is unavailable from Prometheus, use the
actual host/bridge address in the scrape target. For example, `172.17.0.1:8080`
can be appropriate for a Docker bridge; verify the local network rather than
assuming that address.

Inspect MongoDB's replica set:

```bash
docker compose -f deployment/dev/docker-compose.yml exec mongodb mongosh --eval 'rs.status()'
```

If it is uninitialized, initialize it according to this stack's replica-set host
configuration. The existing service health check handles the configured `rs0`
initialization; avoid replacing its advertised host with an unrelated address.

Inspect PostgreSQL readiness and current tables:

```bash
docker compose -f deployment/dev/docker-compose.yml exec postgres pg_isready -U syntrix -d syntrix
docker compose -f deployment/dev/docker-compose.yml exec postgres psql -U syntrix -d syntrix
```

Inside `psql`, `\dt` lists tables and `SELECT * FROM auth_users;` inspects current
user records. These records are current instance identities, not employee or
developer account storage.

A full development reset deletes volumes and their data:

```bash
docker compose -f deployment/dev/docker-compose.yml down -v
docker compose -f deployment/dev/docker-compose.yml up -d
```

Use reset only for disposable development data. It is not a backup/restore
operation or a target project/instance lifecycle implementation.
