# Real-time Monitoring Quick Start

## Overview

The benchmark tool includes an embedded HTTP server for real-time monitoring. Simply start a benchmark and open the monitoring dashboard in your browser to see live metrics.

## Quick Start

### 1. Start a Benchmark

Run from the repository root:

```bash
make -C packages/syntrix build-benchmark
cd packages/syntrix

# Start with monitoring enabled (default)
./bin/syntrix-benchmark run --config configs/crud_mixed.yaml

# Monitoring server starts automatically on http://localhost:9090
# Console output will show:
#   Starting benchmark...
#   Monitoring server: http://localhost:9090
#   Press Ctrl+C to stop
```

### 2. Open Dashboard

Open your browser and navigate to:
```
http://localhost:9090
```

You'll see a live dashboard showing:
- Current benchmark status and progress
- Real-time throughput and latency metrics
- Operation breakdown (create, read, update, delete)
- Worker status
- Recent errors

The dashboard auto-refreshes every 2 seconds.

## Monitoring Endpoints

### REST API

| Endpoint | Description |
|----------|-------------|
| `GET /` | Dashboard UI (HTML) |
| `GET /api/status` | Current benchmark status |
| `GET /api/metrics` | Current aggregated metrics |
| `GET /api/metrics/timeline` | Historical metrics (Phase 2) |
| `GET /api/workers` | Worker status (Phase 2) |
| `GET /api/errors` | Error details (Phase 2) |

### Example: Query Metrics

```bash
# Get current status
curl http://localhost:9090/api/status | jq

# Get current metrics
curl http://localhost:9090/api/metrics | jq

# Watch metrics in real-time
watch -n 2 'curl -s http://localhost:9090/api/metrics | jq .total'
```

## Configuration

### CLI Flags

```bash
# Enable monitoring (default)
./bin/syntrix-benchmark run --monitor

# Disable monitoring
./bin/syntrix-benchmark run --no-monitor

# Custom address
./bin/syntrix-benchmark run --monitor-addr localhost:8080

# Expose to network (use with caution)
./bin/syntrix-benchmark run --monitor-addr 0.0.0.0:9090

# Auto-open browser
./bin/syntrix-benchmark run --monitor-open
```

### YAML Configuration

```yaml
monitor:
  enabled: true
  address: "localhost:9090"
  auto_open: true
  metrics_interval: 2s  # Update frequency
```

## Dashboard Preview

```
┌─────────────────────────────────────────────────────────┐
│  Syntrix Benchmark Monitor    Session: bench-xxx  [Stop]│
├─────────────────────────────────────────────────────────┤
│  Status: Running  │  Elapsed: 2m 05s / 5m 00s  │ 41%   │
├─────────────────────────────────────────────────────────┤
│  Overall Metrics                                        │
│  ────────────────────────────────────────────────────   │
│  Throughput:  2,547 ops/sec                             │
│  Success Rate: 99.94%                                   │
│  Total Ops:    254,700                                  │
│  Errors:       150                                      │
│                                                         │
│  Latency: P50: 12ms │ P90: 45ms │ P99: 125ms          │
└─────────────────────────────────────────────────────────┘
```

## Tips

1. **Performance**: The monitoring server has minimal overhead (<1% CPU/memory)
2. **Network Access**: By default, the server only listens on localhost for security
3. **Multiple Sessions**: Currently supports one benchmark session at a time
4. **Browser Support**: Works in all modern browsers (Chrome, Firefox, Safari, Edge)

## Troubleshooting

### Port Already in Use

```bash
# Use a different port
./bin/syntrix-benchmark run --monitor-addr localhost:9091
```

### Can't Access from Another Machine

```bash
# Expose to network (be careful in production environments)
./bin/syntrix-benchmark run --monitor-addr 0.0.0.0:9090

# Then access from another machine:
# http://<your-ip>:9090
```

### Monitor Not Starting

Check console output for error messages. Common issues:
- Port already in use
- Monitoring disabled with `--no-monitor`
- Network permissions

## Next Steps

- See [monitoring.md](../../../../docs/benchmark/monitoring.md) for complete API documentation
- See [DESIGN.md](../../../../docs/benchmark/DESIGN.md) for architecture details
- See [TASKS.md](../../../../docs/benchmark/TASKS.md) for planned enhancements (charts, WebSocket, etc.)
