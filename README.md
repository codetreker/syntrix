# Syntrix

Syntrix is a Firestore-like realtime document database built with Go and MongoDB.

## What is Syntrix?

Syntrix provides a document database with real-time capabilities, allowing clients to subscribe to data changes and receive instant updates. It's designed as a **splittable monolith** - run everything in one process for simplicity, or deploy as separate microservices for scale.

## Key Features

- **Document Storage**: CRUD operations with Optimistic Locking (CAS)
- **Query Engine**: Filter and sort documents with a powerful query syntax
- **Realtime Updates**: WebSocket/SSE-based real-time events (Create, Update, Delete)
- **Triggers**: Server-side event reactions (Webhooks, Cloud Functions) with CEL conditions
- **Flexible Deployment**: Standalone mode for development, distributed mode for production

## Documentation

See [docs/README.md](docs/README.md) for detailed documentation including:
- Project structure and how to run
- Architecture and design documents
- API reference and SDK guides

## Quick Start

Run from the repository root. The server runs from its package directory so
configuration, data, and logs resolve against that package.

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
