# Syntrix

Syntrix is a realtime backend platform with document databases, queries,
subscriptions, and triggers. Its runtime uses Go, MongoDB for business documents,
and PostgreSQL for system records.

## What is Syntrix?

The [platform architecture](docs/architecture.md) separates three service and
account domains: an employee-facing Management platform, a developer-facing
Console, and developer-owned Syntrix instances serving application end users.
A developer can create multiple instances; each instance owns projects, and
each project has an isolated user identity realm and multiple logical Syntrix
databases. Instance PostgreSQL supports system operation; MongoDB stores the
developer's business documents.

The current repository provides the instance document runtime, SDK, and an
embedded instance administration frontend. The architecture document identifies
the remaining platform, Console, project-identity, and OAuth implementation work.
The runtime is a **splittable monolith**: services within an instance can run
together using direct calls or use distributed service communication.

## Key Features

- **Document Storage**: CRUD operations with Optimistic Locking (CAS)
- **Query Engine**: Filter and sort documents with a powerful query syntax
- **Realtime Updates**: WebSocket/SSE-based real-time events (Create, Update, Delete)
- **Triggers**: Server-side event reactions (Webhooks, Cloud Functions) with CEL conditions
- **Flexible Deployment**: Standalone mode for development, distributed mode for production

## Documentation

Start with [Platform Architecture](docs/architecture.md) for ownership and
terminology, then [docs/README.md](docs/README.md) for:
- Project structure and how to run
- Architecture and design documents
- API reference and SDK guides

## Quick Start

These commands start and test the current instance runtime. Run from the
repository root; the server runs from its package directory so configuration,
data, and logs resolve against that package. Infrastructure prerequisites are
described in the [development guide](deployment/dev/README.md).

```bash
# Build
make -C packages/syntrix build

# Run (standalone mode - all services in one process)
(cd packages/syntrix && ./bin/syntrix --standalone)

# Run tests
make -C packages/syntrix test
```

## Configuration

### Logging

Syntrix uses Go's `log/slog` for structured logging with support for file output and automatic rotation.

**Log Output:**
- Console (stdout) - enabled by default
- Main log file (`syntrix.log`) - all log levels
- Error log file (`errors.log`) - warnings and errors only

**File Structure:**
```
logs/
├── syntrix.log                # Current main log (all levels)
├── syntrix-20260119-001.log   # Rotated log (by size)
├── syntrix-20260119-002.log.gz # Compressed rotated log
├── errors.log                 # Current error log (warn + error)
├── errors-20260119-001.log
└── errors-20260119-002.log.gz
```

**Configuration (`packages/syntrix/configs/config.yml`):**
```yaml
logging:
  level: "info"           # debug, info, warn, error
  format: "text"          # text or json
  dir: "logs"             # log directory (relative or absolute)

  rotation:
    max_size: 100         # MB per file before rotation
    max_backups: 10       # number of old files to keep
    max_age: 30           # days to retain old files
    compress: true        # gzip rotated files

  console:
    enabled: true
    level: "info"
    format: "text"

  file:
    enabled: true
    level: "info"
    format: "text"
```

**Default Settings:**
- Log level: `info`
- Format: `text` (human-readable)
- Directory: `logs/` (relative to working directory)
- Rotation: 100MB per file, keep 10 backups, 30 days retention
- Compression: enabled for rotated files

## License

See [LICENSE](LICENSE) for details.
