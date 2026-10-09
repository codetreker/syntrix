# Syntrix Benchmark Tool Design

**Version**: 1.0
**Last Updated**: 2026-01-18
**Status**: Draft

## 1. Overview

### 1.1 Purpose

The Syntrix Benchmark Tool is designed to evaluate system performance under various load conditions, identify bottlenecks, and provide performance baseline data for the Syntrix realtime document database.

### 1.2 Goals

- Measure system throughput (QPS/TPS) and latency under different load scenarios
- Identify performance bottlenecks in Gateway, Query, Indexer, Streamer, and Trigger services
- Validate system stability and reliability under sustained high load
- Provide reproducible performance benchmarks for regression testing
- Support performance tuning and capacity planning

### 1.3 Non-Goals

- Not a general-purpose load testing tool (focused on Syntrix-specific scenarios)
- Not a replacement for production monitoring (designed for controlled testing environments)
- Not a distributed load generator (single-machine execution for simplicity)

## 2. Architecture

### 2.1 System Architecture

```
┌─────────────────────────────────────────────────────────┐
│                    Benchmark CLI                        │
│  ┌───────────┐  ┌──────────┐  ┌────────────────────┐  │
│  │  Config   │  │  Runner  │  │  Metrics Collector │  │
│  │  Loader   │  │  Engine  │  │                    │  │
│  └─────┬─────┘  └────┬─────┘  └──────────┬─────────┘  │
│        │             │                    │             │
│        └─────────────┼────────────────────┘             │
│                      │                                  │
│         ┌────────────┴────────────┐                    │
│         │                         │                    │
│    ┌────▼─────┐            ┌─────▼──────┐             │
│    │ Worker   │   ...      │  Worker    │             │
│    │  Pool    │            │   Pool     │             │
│    └────┬─────┘            └─────┬──────┘             │
└─────────┼────────────────────────┼─────────────────────┘
          │                        │
          └────────────┬───────────┘
                       │
              ┌────────▼─────────┐
              │   Syntrix API    │
              │   (HTTP/WS/SSE)  │
              └──────────────────┘
```

### 2.2 Component Design

#### 2.2.1 Runner Engine

**Responsibilities**:
- Orchestrate benchmark execution lifecycle
- Manage worker pool creation and coordination
- Control load ramping and warmup phases
- Coordinate metrics collection and reporting

**Key Interfaces**:
```go
type Runner interface {
    // Initialize prepares the benchmark environment
    Initialize(ctx context.Context, config *Config) error

    // Run executes the benchmark scenario
    Run(ctx context.Context) (*Result, error)

    // Cleanup performs post-benchmark cleanup
    Cleanup(ctx context.Context) error
}
```

#### 2.2.2 Worker Pool

**Responsibilities**:
- Execute operations concurrently according to scenario definition
- Maintain target operation rate (QPS) per worker
- Report operation results to metrics collector
- Handle errors and retries

**Key Interfaces**:
```go
type Worker interface {
    // Start begins worker execution
    Start(ctx context.Context) error

    // Execute performs a single operation
    Execute(ctx context.Context, op Operation) (*OperationResult, error)

    // Stop gracefully stops the worker
    Stop(ctx context.Context) error
}
```

#### 2.2.3 Scenario System

**Responsibilities**:
- Define benchmark scenarios (CRUD, Query, Realtime, etc.)
- Generate operation sequences based on scenario configuration
- Support weighted operation distribution

**Key Interfaces**:
```go
type Scenario interface {
    // Name returns scenario identifier
    Name() string

    // Setup prepares scenario prerequisites (e.g., seed data)
    Setup(ctx context.Context, env *TestEnv) error

    // NextOperation returns the next operation to execute
    NextOperation() (Operation, error)

    // Teardown cleans up scenario resources
    Teardown(ctx context.Context, env *TestEnv) error
}

type Operation interface {
    // Type returns operation type (e.g., "create", "read")
    Type() string

    // Execute performs the operation and returns metrics
    Execute(ctx context.Context, client Client) (*OperationResult, error)
}
```

#### 2.2.4 Metrics Collector

**Responsibilities**:
- Collect operation results from workers in real-time
- Calculate statistical metrics (latency percentiles, throughput, error rates)
- Support time-series data for trend analysis
- Export metrics in multiple formats (JSON, Prometheus, etc.)

**Collected Metrics**:
- **Throughput**: Operations per second (OPS), Requests per second (RPS)
- **Latency**: Min, Max, Mean, Median, P90, P95, P99
- **Success Rate**: Successful operations / Total operations
- **Error Distribution**: Error types and counts
- **Resource Usage**: Client-side CPU, Memory (optional)

#### 2.2.5 Client Abstraction

**Responsibilities**:
- Abstract HTTP, WebSocket, and SSE communication
- Handle authentication and token management
- Implement connection pooling and reuse
- Provide retry logic for transient failures

**Key Interfaces**:
```go
type Client interface {
    // HTTP operations
    CreateDocument(ctx context.Context, collection string, doc map[string]interface{}) (*Document, error)
    GetDocument(ctx context.Context, collection, id string) (*Document, error)
    UpdateDocument(ctx context.Context, collection, id string, doc map[string]interface{}) (*Document, error)
    DeleteDocument(ctx context.Context, collection, id string) error
    Query(ctx context.Context, query Query) ([]*Document, error)

    // Realtime operations
    Subscribe(ctx context.Context, query Query) (*Subscription, error)
    Unsubscribe(ctx context.Context, subID string) error
}
```

### 2.3 Directory Structure

```
packages/syntrix/
├── cmd/
│   └── syntrix-benchmark/
│       └── main.go               # CLI entry point
├── pkg/benchmark/
│   ├── config/
│   │   ├── config.go             # Configuration structs
│   │   ├── loader.go             # Config file loading
│   │   └── validator.go          # Config validation
│   ├── runner/
│   │   ├── runner.go             # Benchmark runner implementation
│   │   ├── worker.go             # Worker pool management
│   │   ├── scheduler.go          # Operation scheduling
│   │   └── lifecycle.go          # Warmup, rampup, execution phases
│   ├── scenario/
│   │   ├── interface.go          # Scenario interfaces
│   │   ├── crud.go               # CRUD scenario
│   │   ├── query.go              # Query scenario
│   │   ├── realtime.go           # Realtime subscription scenario
│   │   ├── trigger.go            # Trigger scenario (future)
│   │   └── mixed.go              # Mixed workload scenario
│   ├── client/
│   │   ├── http.go               # HTTP client implementation
│   │   ├── websocket.go          # WebSocket client
│   │   └── pool.go               # Connection pool
│   ├── metrics/
│   │   ├── collector.go          # Metrics collection
│   │   ├── aggregator.go         # Statistical aggregation
│   │   ├── histogram.go          # Latency histogram
│   │   └── reporter.go           # Report generation
│   ├── generator/
│   │   ├── data.go               # Test data generation
│   │   └── document.go           # Document generator
│   └── utils/
│       ├── token.go              # Authentication token helpers
│       └── wait.go               # Wait/retry utilities
├── configs/
│   ├── crud_basic.yaml           # Basic CRUD benchmark
│   ├── crud_mixed.yaml           # Mixed CRUD workload
│   ├── query_simple.yaml         # Simple query benchmark
│   ├── query_complex.yaml        # Complex query benchmark
│   └── realtime.yaml             # Realtime subscription benchmark
└── examples/
    ├── custom_scenario.go        # Example custom scenario
    └── plugin.go                 # Plugin system example (future)
```

## 3. Benchmark Scenarios

### 3.1 CRUD Scenarios

#### 3.1.1 Write-Heavy Workload
- **Description**: High-volume document creation
- **Operation Mix**: 90% Create, 10% Read
- **Target Metrics**: Write throughput, indexer lag
- **Use Case**: Initial data loading, bulk imports

#### 3.1.2 Read-Heavy Workload
- **Description**: High-volume document reads
- **Operation Mix**: 10% Write, 90% Read
- **Target Metrics**: Read latency, cache hit rate
- **Use Case**: Content delivery, reporting

#### 3.1.3 Balanced Workload
- **Description**: Realistic mixed operations
- **Operation Mix**: 30% Create, 50% Read, 15% Update, 5% Delete
- **Target Metrics**: Overall throughput and latency
- **Use Case**: General application workload

### 3.2 Query Scenarios

#### 3.2.1 Simple Filtering
- **Description**: Single-field equality filters
- **Query Pattern**: `{field: "value"}`
- **Target Metrics**: Query latency, indexer efficiency
- **Use Case**: Lookup by ID, username, etc.

#### 3.2.2 Complex Filtering
- **Description**: Multi-field, range, and composite queries
- **Query Pattern**: Multiple filters with `AND`/`OR`, range operators
- **Target Metrics**: Query planning time, execution time
- **Use Case**: Advanced search, analytics

#### 3.2.3 Large Result Sets
- **Description**: Queries returning thousands of documents
- **Query Pattern**: Broad filters with pagination
- **Target Metrics**: Streaming performance, memory usage
- **Use Case**: Batch processing, exports

#### 3.2.4 Sorted Queries
- **Description**: Queries with ORDER BY clauses
- **Query Pattern**: Filters + OrderBy
- **Target Metrics**: Sorting overhead, index usage
- **Use Case**: Leaderboards, time-series data

### 3.3 Realtime Scenarios

#### 3.3.1 Connection Scalability
- **Description**: Scale up WebSocket connections
- **Pattern**: Open N concurrent connections, idle
- **Target Metrics**: Connection establishment time, memory per connection
- **Use Case**: Determine max concurrent connections

#### 3.3.2 Subscription Scalability
- **Description**: Many subscriptions per connection
- **Pattern**: M subscriptions across N connections
- **Target Metrics**: Subscription overhead, event routing latency
- **Use Case**: Dashboard with multiple live widgets

#### 3.3.3 Event Throughput
- **Description**: High-frequency document changes
- **Pattern**: Rapid writes while subscriptions are active
- **Target Metrics**: Event propagation latency, streamer throughput
- **Use Case**: Chat applications, live feeds

#### 3.3.4 Filtered Subscriptions
- **Description**: Subscriptions with complex filters
- **Pattern**: Subscriptions with multi-field filters
- **Target Metrics**: Filter evaluation overhead
- **Use Case**: Personalized feeds

### 3.4 Trigger Scenarios (Future)

#### 3.4.1 Trigger Evaluation
- **Description**: High-volume trigger matching
- **Pattern**: Many documents matching trigger conditions
- **Target Metrics**: CEL evaluation latency, queue throughput

#### 3.4.2 Webhook Delivery
- **Description**: Trigger webhook delivery under load
- **Pattern**: Trigger actions with external HTTP calls
- **Target Metrics**: Delivery latency, retry behavior

### 3.5 Multi-Database Scenarios

#### 3.5.1 Database Isolation
- **Description**: Concurrent load on multiple databases
- **Pattern**: Parallel benchmarks on separate databases
- **Target Metrics**: Cross-database interference, resource isolation

## 4. Configuration System

### 4.1 Configuration Schema

```yaml
# Benchmark configuration
benchmark:
  name: "crud_mixed"              # Benchmark name for reporting
  target: "http://localhost:8080" # Target Syntrix instance
  duration: "5m"                  # Total benchmark duration
  warmup: "30s"                   # Warmup period (results excluded)
  rampup: "10s"                   # Gradual load increase period
  workers: 100                    # Number of concurrent workers

# Authentication configuration
auth:
  database: "default"             # Database for authentication
  username: "benchmark_user"      # Username (auto-created if needed)
  password: "benchmark_pass"      # Password

# Scenario configuration
scenario:
  type: "mixed"                   # Scenario type: crud, query, realtime, mixed

  # Operation weights (for mixed scenarios)
  operations:
    - type: "create"
      weight: 30                  # 30% of operations
      rate: 1000                  # Target 1000 ops/sec
      config:
        collection: "benchmark_docs"

    - type: "read"
      weight: 50
      rate: 2000
      config:
        collection: "benchmark_docs"
        random: true              # Read random existing documents

    - type: "update"
      weight: 15
      rate: 500
      config:
        collection: "benchmark_docs"
        patch: true               # Use PATCH instead of PUT

    - type: "delete"
      weight: 5
      rate: 100
      config:
        collection: "benchmark_docs"

# Data generation configuration
data:
  documentSize: "1KB"             # Target document size (approximate)
  fieldsCount: 10                 # Number of fields per document
  seedData: 10000                 # Pre-populate N documents before benchmark
  cleanup: true                   # Clean up data after benchmark

# Metrics configuration
metrics:
  interval: "10s"                 # Progress reporting interval
  histogram:
    buckets: [1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000] # ms

# Output configuration
output:
  console: true                   # Print to console
  file: "results/{{.Name}}_{{.Timestamp}}.json"
  format: "json"                  # json, prometheus, csv
```

### 4.2 Configuration Loading Priority

1. Default values (hardcoded)
2. Configuration file (`--config` flag)
3. Environment variables (`SYNTRIX_BENCHMARK_*`)
4. Command-line flags (highest priority)

## 5. Metrics and Reporting

### 5.1 Real-time Console Output

```
Syntrix Benchmark - crud_mixed
Target: http://localhost:8080
Duration: 5m | Workers: 100 | Warmup: 30s | Rampup: 10s

[Warmup] 00:15 / 00:30
  Create: 850 ops/s | Latency: p50=12ms p95=45ms p99=89ms
  Read:   1420 ops/s | Latency: p50=8ms p95=32ms p99=67ms

[Running] 02:30 / 05:00 (50%)
  Total:  2547 ops/s | Success: 99.8% | Errors: 5 (0.2%)
  Create: 763 ops/s  | Latency: p50=15ms p95=52ms p99=105ms
  Read:   1273 ops/s | Latency: p50=9ms p95=38ms p99=78ms
  Update: 382 ops/s  | Latency: p50=18ms p95=67ms p99=142ms
  Delete: 129 ops/s  | Latency: p50=11ms p95=43ms p99=91ms

  Errors:
    - 409 Conflict: 3
    - Timeout: 2
```

### 5.2 JSON Report Format

```json
{
  "benchmark": {
    "name": "crud_mixed",
    "start_time": "2026-01-18T15:30:00Z",
    "end_time": "2026-01-18T15:35:00Z",
    "duration_seconds": 300,
    "config": { /* ... */ }
  },
  "summary": {
    "total_operations": 764100,
    "total_errors": 150,
    "success_rate": 99.98,
    "throughput": 2547.0,
    "latency": {
      "min": 2,
      "max": 1250,
      "mean": 18.5,
      "median": 12,
      "p90": 45,
      "p95": 67,
      "p99": 125
    }
  },
  "operations": {
    "create": {
      "count": 229230,
      "errors": 45,
      "throughput": 764.1,
      "latency": { /* ... */ }
    },
    "read": {
      "count": 382050,
      "errors": 60,
      "throughput": 1273.5,
      "latency": { /* ... */ }
    }
    /* ... */
  },
  "errors": [
    {"type": "409 Conflict", "count": 90},
    {"type": "Timeout", "count": 60}
  ],
  "timeline": [
    {
      "timestamp": "2026-01-18T15:30:10Z",
      "elapsed_seconds": 10,
      "throughput": 2450,
      "latency_p99": 120
    }
    /* ... one entry per metrics.interval */
  ]
}
```

### 5.3 Comparison Reports

Support comparing multiple benchmark runs:

```bash
syntrix-benchmark compare \
  results/baseline.json \
  results/optimized.json \
  --output comparison.html
```

Output shows:
- Throughput delta (%)
- Latency improvements/regressions per percentile
- Error rate changes
- Side-by-side charts

## 6. Implementation Phases

### Phase 1: MVP (Week 1-2)

**Goal**: Basic functional benchmark tool

**Deliverables**:
- CLI with `run` command
- Basic CRUD scenario (create, read, update, delete)
- Simple worker pool with fixed concurrency
- Console output with throughput and latency
- JSON result export

**Out of Scope**:
- Complex scenarios (query, realtime)
- Configuration files (hardcoded or flags only)
- Advanced metrics (histograms, timeline)

### Phase 2: Scenarios and Metrics (Week 3-4)

**Goal**: Comprehensive scenario coverage and detailed metrics

**Deliverables**:
- Query scenarios (simple, complex, sorted)
- Realtime scenarios (connections, subscriptions, events)
- Configuration file support (YAML)
- Latency histograms and percentiles
- Timeline metrics for trend analysis
- Error categorization

**Out of Scope**:
- Trigger scenarios
- HTML reports
- Comparison tool

### Phase 3: Polish and Tooling (Week 5-6)

**Goal**: Production-ready tool with advanced features

**Deliverables**:
- Trigger scenarios
- HTML report generation with charts
- `compare` command for benchmark comparison
- Pre-defined configuration templates
- CI/CD integration guide
- Performance regression detection

**Out of Scope**:
- Distributed load generation
- Plugin system
- GUI

## 7. Usage Examples

### 7.1 Quick Start

```bash
# Build the benchmark tool
make -C packages/syntrix build-benchmark
cd packages/syntrix

# Run a simple CRUD benchmark
./bin/syntrix-benchmark run \
  --target http://localhost:8080 \
  --duration 1m \
  --workers 50 \
  --scenario crud_basic

# Run with custom configuration
./bin/syntrix-benchmark run --config configs/crud_mixed.yaml

# Compare two benchmark results
./bin/syntrix-benchmark compare results/run1.json results/run2.json
```

### 7.2 CI/CD Integration

```yaml
# .github/workflows/performance.yml
name: Performance Benchmark

on:
  pull_request:
    branches: [main]

jobs:
  benchmark:
    runs-on: ubuntu-latest
    defaults:
      run:
        working-directory: packages/syntrix
    steps:
      - uses: actions/checkout@v3

      - name: Start MongoDB
        run: docker-compose up -d mongodb

      - name: Start Syntrix
        run: ./bin/syntrix --standalone &

      - name: Run Benchmark
        run: |
          ./bin/syntrix-benchmark run \
            --config configs/ci_baseline.yaml \
            --output results/pr_${{ github.event.pull_request.number }}.json

      - name: Compare with Baseline
        run: |
          ./bin/syntrix-benchmark compare \
            results/baseline.json \
            results/pr_${{ github.event.pull_request.number }}.json \
            --threshold 10  # Fail if >10% regression
```

### 7.3 Custom Scenarios

```go
package main

import (
    "context"
    "github.com/codetreker/syntrix/pkg/benchmark/scenario"
    "github.com/codetreker/syntrix/pkg/benchmark/client"
)

// CustomScenario implements a custom benchmark scenario
type CustomScenario struct {
    collection string
}

func (s *CustomScenario) Name() string {
    return "custom_workload"
}

func (s *CustomScenario) Setup(ctx context.Context, env *TestEnv) error {
    // Pre-populate data, create indexes, etc.
    return nil
}

func (s *CustomScenario) NextOperation() (scenario.Operation, error) {
    // Return the next operation based on custom logic
    return &ReadOperation{
        Collection: s.collection,
        DocumentID: generateRandomID(),
    }, nil
}

func (s *CustomScenario) Teardown(ctx context.Context, env *TestEnv) error {
    // Cleanup resources
    return nil
}
```

## 8. Performance Targets (Draft)

These are preliminary targets to validate system design:

| Metric | Target (Standalone) | Target (Distributed) |
|--------|---------------------|----------------------|
| Write Throughput | 5,000 ops/sec | 20,000 ops/sec |
| Read Throughput | 10,000 ops/sec | 50,000 ops/sec |
| Query Latency (P99) | < 100ms | < 50ms |
| WebSocket Connections | 10,000 | 50,000 |
| Event Propagation (P99) | < 200ms | < 100ms |

*Note: Actual targets depend on hardware, MongoDB configuration, and workload characteristics.*

## 9. Open Questions

1. **Should we support distributed load generation?**
   - Single-node benchmark may not fully saturate server in distributed mode
   - Multi-node coordination adds significant complexity
   - **Decision**: Defer to future phase, focus on single-node first

2. **How to handle MongoDB performance vs. Syntrix performance?**
   - Some bottlenecks may be in MongoDB, not Syntrix services
   - Should we provide MongoDB-specific benchmarks?
   - **Decision**: Focus on end-to-end Syntrix API performance, provide guidance on MongoDB tuning separately

3. **Data cleanup strategy?**
   - Auto-cleanup may interfere with manual investigation
   - Leaving data pollutes test database
   - **Decision**: Default to cleanup, provide `--no-cleanup` flag for debugging

4. **Prometheus integration?**
   - Export metrics to Prometheus for historical tracking
   - Adds dependency on Prometheus instance
   - **Decision**: Support Prometheus export format, but don't require running Prometheus server

5. **Should benchmark tool reuse `tests/integration` helpers?**
   - Code reuse is good, but introduces coupling
   - Integration test helpers may change frequently
   - **Decision**: Extract common utilities to `packages/syntrix/internal/testutil`, shared by both integration and benchmark

## 10. References

- [Syntrix Architecture](../architecture.md)
- [Integration Tests](../../packages/syntrix/tests/integration/)
- [MongoDB Performance Best Practices](https://www.mongodb.com/docs/manual/administration/analyzing-mongodb-performance/)
- [Load Testing Best Practices](https://grafana.com/blog/2024/01/30/load-testing-best-practices/)

## 11. Changelog

| Date | Version | Changes |
|------|---------|---------|
| 2026-01-18 | 1.0 | Initial design document |

---

**Authors**: Claude + @jianjun
**Reviewers**: TBD
