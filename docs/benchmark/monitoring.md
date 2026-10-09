# Real-time Monitoring HTTP Server

**Version**: 1.0
**Last Updated**: 2026-01-18

## 1. Overview

The Real-time Monitoring Server provides a web-based interface to monitor benchmark execution progress in real-time. It exposes metrics, status, and logs via HTTP endpoints for easy visualization.

## 2. Requirements

### 2.1 Functional Requirements

- **FR-1**: Display current benchmark status (running, completed, failed)
- **FR-2**: Show real-time metrics (throughput, latency, error rate)
- **FR-3**: Display worker status and distribution
- **FR-4**: Show operation breakdown by type
- **FR-5**: Provide timeline of metrics (last N minutes)
- **FR-6**: Support multiple concurrent benchmark sessions
- **FR-7**: Auto-refresh UI or use WebSocket for live updates

### 2.2 Non-Functional Requirements

- **NFR-1**: Minimal performance overhead (< 1% CPU/memory)
- **NFR-2**: No external dependencies (embedded UI)
- **NFR-3**: Accessible from browser on same machine or network
- **NFR-4**: Graceful degradation if monitoring server fails

## 3. Architecture

### 3.1 Component Design

```
┌─────────────────────────────────────────────┐
│         Benchmark Runner Process            │
│                                             │
│  ┌──────────┐         ┌─────────────────┐ │
│  │  Runner  │────────▶│ Metrics Store   │ │
│  │  Engine  │         │ (in-memory)     │ │
│  └──────────┘         └────────┬────────┘ │
│                                 │          │
│                                 │          │
│                        ┌────────▼────────┐ │
│                        │  HTTP Server    │ │
│                        │  (Monitoring)   │ │
│                        └────────┬────────┘ │
│                                 │          │
└─────────────────────────────────┼──────────┘
                                  │
                                  │ HTTP
                                  │
                        ┌─────────▼────────┐
                        │   Web Browser    │
                        │                  │
                        │  ┌────────────┐  │
                        │  │ Dashboard  │  │
                        │  │    UI      │  │
                        │  └────────────┘  │
                        └──────────────────┘
```

### 3.2 Data Flow

1. Runner and Workers collect metrics during execution
2. Metrics are pushed to a thread-safe Metrics Store
3. HTTP Server reads from Metrics Store on API requests
4. Browser fetches data via API and renders UI
5. UI auto-refreshes every N seconds (configurable)

## 4. API Design

### 4.1 REST API Endpoints

#### GET /api/status

Returns current benchmark status.

**Response**:
```json
{
  "session_id": "bench-20260118-153045",
  "status": "running",
  "start_time": "2026-01-18T15:30:45Z",
  "elapsed_seconds": 125,
  "config": {
    "duration": 300,
    "workers": 100,
    "scenario": "crud_mixed"
  }
}
```

**Status Values**: `starting`, `warmup`, `ramping`, `running`, `completed`, `failed`, `cancelled`

#### GET /api/metrics

Returns current aggregated metrics.

**Response**:
```json
{
  "timestamp": "2026-01-18T15:32:50Z",
  "total": {
    "operations": 254700,
    "errors": 150,
    "success_rate": 99.94,
    "throughput": 2037.6,
    "latency": {
      "min": 2,
      "max": 1250,
      "mean": 18.5,
      "p50": 12,
      "p90": 45,
      "p95": 67,
      "p99": 125
    }
  },
  "by_operation": {
    "create": {
      "count": 76410,
      "errors": 45,
      "throughput": 611.3,
      "latency": { /* ... */ }
    },
    "read": {
      "count": 127350,
      "errors": 60,
      "throughput": 1018.8,
      "latency": { /* ... */ }
    },
    "update": { /* ... */ },
    "delete": { /* ... */ }
  }
}
```

#### GET /api/metrics/timeline

Returns time-series metrics for charting.

**Query Parameters**:
- `window`: Time window in seconds (default: 300, max: 3600)
- `interval`: Data point interval in seconds (default: 5)

**Response**:
```json
{
  "interval_seconds": 5,
  "data_points": [
    {
      "timestamp": "2026-01-18T15:30:00Z",
      "throughput": 2450,
      "latency_p99": 120,
      "error_rate": 0.05
    },
    {
      "timestamp": "2026-01-18T15:30:05Z",
      "throughput": 2480,
      "latency_p99": 115,
      "error_rate": 0.03
    }
    /* ... */
  ]
}
```

#### GET /api/workers

Returns worker status and distribution.

**Response**:
```json
{
  "total_workers": 100,
  "active_workers": 98,
  "idle_workers": 2,
  "workers": [
    {
      "id": "worker-001",
      "status": "active",
      "operations_completed": 2547,
      "current_operation": "create",
      "last_error": null
    }
    /* ... first 20 workers */
  ]
}
```

#### GET /api/errors

Returns error breakdown.

**Response**:
```json
{
  "total_errors": 150,
  "by_type": [
    {
      "type": "409 Conflict",
      "count": 90,
      "percentage": 60.0,
      "sample_message": "Document version mismatch"
    },
    {
      "type": "Timeout",
      "count": 60,
      "percentage": 40.0,
      "sample_message": "Request timeout after 30s"
    }
  ],
  "recent_errors": [
    {
      "timestamp": "2026-01-18T15:32:45Z",
      "type": "Timeout",
      "message": "Request timeout after 30s",
      "operation": "update",
      "worker_id": "worker-042"
    }
    /* ... last 50 errors */
  ]
}
```

#### GET /api/logs

Returns recent log entries (optional, for debugging).

**Query Parameters**:
- `level`: Filter by log level (debug, info, warn, error)
- `limit`: Max number of entries (default: 100)

**Response**:
```json
{
  "logs": [
    {
      "timestamp": "2026-01-18T15:32:50Z",
      "level": "info",
      "message": "Worker pool started",
      "context": {"workers": 100}
    }
    /* ... */
  ]
}
```

#### POST /api/control/pause

Pause benchmark execution (if supported).

**Response**:
```json
{
  "status": "paused",
  "message": "Benchmark paused at worker request"
}
```

#### POST /api/control/resume

Resume paused benchmark.

#### POST /api/control/stop

Gracefully stop benchmark.

### 4.2 WebSocket API (Optional Enhancement)

#### WS /ws/metrics

Real-time metrics streaming.

**Server Messages**:
```json
{
  "type": "metrics_update",
  "timestamp": "2026-01-18T15:32:50Z",
  "data": { /* same as GET /api/metrics */ }
}
```

**Message Types**:
- `metrics_update`: Periodic metrics update (every 2s)
- `status_change`: Status changed (e.g., warmup → running)
- `error_occurred`: New error occurred
- `benchmark_completed`: Benchmark finished

## 5. UI Design

### 5.1 Dashboard Layout

```
┌─────────────────────────────────────────────────────────┐
│  Syntrix Benchmark Monitor    Session: bench-xxx  [Stop]│
├─────────────────────────────────────────────────────────┤
│                                                         │
│  Status: Running  │  Elapsed: 2m 05s / 5m 00s  │ 41%  │
│                                                         │
├─────────────────────────────────────────────────────────┤
│                                                         │
│  ┌─────────────────────────────────────────────────┐   │
│  │  Overall Metrics                                │   │
│  │  ────────────────────────────────────────────   │   │
│  │  Throughput:  2,547 ops/sec                     │   │
│  │  Success Rate: 99.94%                           │   │
│  │  Total Ops:    254,700                          │   │
│  │  Errors:       150                              │   │
│  │                                                  │   │
│  │  Latency Percentiles:                           │   │
│  │    P50:  12ms  │  P90:  45ms  │  P99:  125ms   │   │
│  └─────────────────────────────────────────────────┘   │
│                                                         │
├─────────────────────────────────────────────────────────┤
│                                                         │
│  ┌─────────────────────────────────────────────────┐   │
│  │  Throughput Timeline (last 5m)                  │   │
│  │  ────────────────────────────────────────────   │   │
│  │                                                  │   │
│  │  2.6k ┤    ╭─╮                                  │   │
│  │  2.4k ┤  ╭─╯ ╰─╮  ╭─╮                          │   │
│  │  2.2k ┤╭─╯     ╰──╯ ╰─╮                        │   │
│  │  2.0k ┼╯             ╰─────                     │   │
│  │       └──────────────────────────────────►      │   │
│  └─────────────────────────────────────────────────┘   │
│                                                         │
├─────────────────────────────────────────────────────────┤
│                                                         │
│  ┌────────────────────┐  ┌─────────────────────────┐   │
│  │ By Operation       │  │  Workers                │   │
│  │ ─────────────────  │  │  ──────────────────     │   │
│  │ Create:  611/s     │  │  Active:  98 / 100      │   │
│  │ Read:   1019/s     │  │  Idle:     2            │   │
│  │ Update:  407/s     │  │                         │   │
│  │ Delete:  102/s     │  │  [Worker List ▼]        │   │
│  └────────────────────┘  └─────────────────────────┘   │
│                                                         │
├─────────────────────────────────────────────────────────┤
│                                                         │
│  ┌─────────────────────────────────────────────────┐   │
│  │  Recent Errors (150 total)                      │   │
│  │  ────────────────────────────────────────────   │   │
│  │  [15:32:45] 409 Conflict: Document version...   │   │
│  │  [15:32:43] Timeout: Request timeout after 30s  │   │
│  │  [15:32:40] 409 Conflict: Document version...   │   │
│  │  ...                                             │   │
│  └─────────────────────────────────────────────────┘   │
│                                                         │
└─────────────────────────────────────────────────────────┘
```

### 5.2 UI Technology

**Option 1: Server-Side Rendering (Simpler)**
- Go HTML templates
- Inline JavaScript for auto-refresh
- No build step required
- Lightweight (~10KB total)

**Option 2: Modern SPA (Better UX)**
- React/Vue/Svelte
- Chart.js or Recharts for visualizations
- Requires build step
- Larger bundle size (~100-200KB)

**Recommendation**: Start with Option 1 (templates + vanilla JS) for MVP, upgrade to Option 2 in Phase 2 if needed.

## 6. Implementation Details

### 6.1 Metrics Store

```go
// MetricsStore holds real-time benchmark metrics
type MetricsStore struct {
    mu sync.RWMutex

    // Session info
    SessionID  string
    StartTime  time.Time
    Status     BenchmarkStatus
    Config     *Config

    // Current metrics
    Current    *AggregatedMetrics

    // Timeline (ring buffer)
    Timeline   *RingBuffer[*TimelinePoint]

    // Workers
    Workers    map[string]*WorkerStatus

    // Errors
    Errors     *RingBuffer[*ErrorEntry]
    ErrorStats map[string]int
}

// Update methods
func (s *MetricsStore) RecordOperation(result *OperationResult)
func (s *MetricsStore) RecordError(err *ErrorEntry)
func (s *MetricsStore) UpdateWorkerStatus(workerID string, status *WorkerStatus)
func (s *MetricsStore) SetStatus(status BenchmarkStatus)

// Query methods (read-only)
func (s *MetricsStore) GetStatus() *StatusResponse
func (s *MetricsStore) GetMetrics() *MetricsResponse
func (s *MetricsStore) GetTimeline(window time.Duration) *TimelineResponse
func (s *MetricsStore) GetWorkers() *WorkersResponse
func (s *MetricsStore) GetErrors() *ErrorsResponse
```

### 6.2 HTTP Server

```go
// MonitorServer provides HTTP interface for real-time monitoring
type MonitorServer struct {
    store  *MetricsStore
    server *http.Server
}

func NewMonitorServer(store *MetricsStore, addr string) *MonitorServer
func (s *MonitorServer) Start(ctx context.Context) error
func (s *MonitorServer) Shutdown(ctx context.Context) error
```

### 6.3 Integration with Runner

```go
type Runner struct {
    // ... existing fields

    metricsStore  *MetricsStore
    monitorServer *MonitorServer
}

func (r *Runner) Run(ctx context.Context) error {
    // Start monitoring server
    if r.config.Monitor.Enabled {
        go r.monitorServer.Start(ctx)
        defer r.monitorServer.Shutdown(context.Background())
    }

    // Run benchmark
    // Workers call r.metricsStore.RecordOperation() after each operation

    // ...
}
```

## 7. Configuration

### 7.1 YAML Configuration

```yaml
monitor:
  enabled: true                    # Enable monitoring server
  address: "localhost:9090"        # Listen address
  auto_open: true                  # Auto-open browser on start
  metrics_interval: 2s             # Metrics update interval
  timeline_retention: 1h           # How long to keep timeline data
  max_errors_retained: 1000        # Max errors in memory
```

### 7.2 CLI Flags

```bash
syntrix-benchmark run \
  --monitor                      # Enable monitoring (default: true)
  --monitor-addr localhost:9090  # Monitor address
  --no-monitor                   # Disable monitoring
  --monitor-open                 # Auto-open browser
```

## 8. Performance Considerations

### 8.1 Overhead Minimization

- **Sampling**: Only update timeline every N seconds (configurable)
- **Ring Buffers**: Fixed-size buffers for timeline and errors
- **Read/Write Locks**: Allow concurrent reads without blocking
- **Aggregation**: Pre-aggregate metrics in memory, not on-demand
- **Lazy Rendering**: Only render visible UI elements

### 8.2 Memory Usage

Estimated memory overhead:
- Metrics Store: ~10 MB (timeline + errors + workers)
- HTTP Server: ~5 MB (goroutines + buffers)
- **Total**: ~15 MB overhead (acceptable for benchmark tool)

## 9. Security Considerations

### 9.1 Access Control

**MVP (Phase 1)**:
- No authentication (localhost only)
- Bind to localhost by default
- Warning if binding to 0.0.0.0

**Future Enhancement**:
- Basic auth (username/password)
- API key authentication
- Read-only vs. control permissions

### 9.2 Network Exposure

- Default to `localhost:9090` (not exposed to network)
- Require explicit `--monitor-addr 0.0.0.0:9090` to expose
- Display warning when exposing to network

## 10. Testing Strategy

### 10.1 Unit Tests

- MetricsStore concurrent access
- API endpoint responses
- Timeline ring buffer behavior
- Error aggregation logic

### 10.2 Integration Tests

- Start benchmark with monitoring enabled
- Verify API endpoints return valid data
- Test graceful shutdown
- Test concurrent API requests

### 10.3 Manual Testing

- Visual verification of UI
- Test with different scenarios
- Test with high load (1000+ workers)

## 11. Implementation Phases

### Phase 1: Basic API (P1-55 to P1-59)
- MetricsStore implementation
- HTTP server with status and metrics endpoints
- Simple HTML template UI
- Auto-refresh every 5 seconds

### Phase 2: Enhanced UI (P2-50 to P2-54)
- Timeline endpoint and charting
- Worker status endpoint
- Error breakdown endpoint
- Improved UI with inline charts

### Phase 3: Advanced Features (Future)
- WebSocket streaming
- Control endpoints (pause/resume/stop)
- Log streaming
- SPA with React

## 12. Example Usage

### 12.1 Starting Benchmark with Monitor

```bash
cd packages/syntrix

# Default (monitor enabled on localhost:9090)
./bin/syntrix-benchmark run --config configs/crud_mixed.yaml

# Custom address
./bin/syntrix-benchmark run --monitor-addr :8080

# Disable monitor
./bin/syntrix-benchmark run --no-monitor

# Auto-open browser
./bin/syntrix-benchmark run --monitor-open
```

### 12.2 Accessing Monitor

```bash
# Open in browser
open http://localhost:9090

# Curl API endpoints
curl http://localhost:9090/api/status
curl http://localhost:9090/api/metrics
curl http://localhost:9090/api/metrics/timeline?window=300

# Monitor from another terminal
watch -n 2 'curl -s http://localhost:9090/api/metrics | jq .total'
```

## 13. Future Enhancements

- **Multiple Sessions**: Support monitoring multiple concurrent benchmarks
- **Historical Data**: Persist metrics to disk for post-analysis
- **Prometheus Export**: `/metrics` endpoint in Prometheus format
- **Distributed Monitoring**: Monitor multiple benchmark nodes
- **Alerting**: Configurable alerts for error rates, latency spikes
- **Export**: Download current metrics as JSON/CSV from UI

---

**Authors**: Claude + @jianjun
**Status**: Design Document
**Next Steps**: Update TASKS.md with monitoring implementation tasks
