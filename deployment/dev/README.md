# Syntrix Development Environment

This directory contains Docker Compose configuration for running Syntrix with full monitoring stack locally.

The MongoDB container sets soft and hard `nofile` limits to 64,000. Data files,
journals, and connections consume file descriptors; a low inherited Docker limit
can stop WiredTiger during collection and index creation. These explicit limits
follow [MongoDB resource-limit guidance](https://www.mongodb.com/docs/manual/reference/ulimit/).
Recreate the MongoDB container after changing the limits so the new process
inherits them. The pipeline and devcontainer MongoDB services use the same limits.

## Quick Start

```bash
# Start all services
docker compose up -d

# View logs
docker compose logs -f

# Stop all services
docker compose down

# Stop and remove volumes (full reset)
docker compose down -v
```

## Services

| Service | Port | Description |
|---------|------|-------------|
| MongoDB | 27017 | Primary data store (replica set) |
| PostgreSQL | 5432 | User authentication store |
| NATS | 4222, 8222 | Message queue for triggers |
| Prometheus | 9090 | Metrics storage and alerting |
| Grafana | 3000 | Dashboards and visualization |

## Running Syntrix

### Option 1: Outside Docker (Recommended for Development)

```bash
# From the repository root
make -C packages/syntrix build
(cd packages/syntrix && ./bin/syntrix --standalone)
```

The package working directory supplies the default `configs/` directory and
keeps runtime data and logs with the server package.

Prometheus is pre-configured to scrape `host.docker.internal:8080`.

### Option 2: Inside Docker

Uncomment the `syntrix` service in `docker-compose.yml`.

## Accessing Services

| Service | URL | Credentials |
|---------|-----|-------------|
| Syntrix API | http://localhost:8080 | - |
| Prometheus | http://localhost:9090 | - |
| Grafana | http://localhost:3000 | admin/admin |
| NATS Monitoring | http://localhost:8222 | - |

## Grafana Dashboards

Dashboards are auto-provisioned from `grafana/dashboards/`:

- **Syntrix Overview** - Service health, RED metrics, resource usage

## Prometheus Alerts

Alert rules are defined in `prometheus/alerts.yml`:

- Service down
- High error rate (>5%)
- High latency (P95 > 500ms, P99 > 1s)
- Puller backpressure
- High goroutine count

## Directory Structure

```
deployment/dev/
├── docker-compose.yml       # Main compose file
├── postgres/
│   └── postgresql.conf      # PostgreSQL configuration
├── prometheus/
│   ├── prometheus.yml       # Scrape configuration
│   └── alerts.yml           # Alert rules
├── grafana/
│   ├── provisioning/
│   │   ├── datasources/     # Auto-configure Prometheus
│   │   └── dashboards/      # Auto-load dashboards
│   └── dashboards/          # Dashboard JSON files
└── scripts/
    ├── mongo-init.js        # MongoDB replica set init
    └── postgres-init.sql    # PostgreSQL schema init
```

## Troubleshooting

### Prometheus can't reach Syntrix

If running Syntrix outside Docker on Linux, you may need to use the actual host IP:

```yaml
# prometheus/prometheus.yml
- targets: ["172.17.0.1:8080"]  # Docker bridge gateway
```

### MongoDB replica set not initialized

```bash
docker compose exec mongodb mongosh --eval "rs.status()"
```

If not initialized:
```bash
docker compose exec mongodb mongosh --eval "rs.initiate()"
```

### Reset everything

```bash
docker compose down -v
docker compose up -d
```

### PostgreSQL connection issues

Check if PostgreSQL is ready:
```bash
docker compose exec postgres pg_isready -U syntrix -d syntrix
```

Connect to PostgreSQL:
```bash
docker compose exec postgres psql -U syntrix -d syntrix
```

Check if tables are created:
```sql
\dt
SELECT * FROM auth_users;
```
