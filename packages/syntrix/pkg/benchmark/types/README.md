# Core Types and Interfaces

This package defines the core types and interfaces for the Syntrix benchmark tool.

## Overview

The benchmark tool is built around several key abstractions:

- **Runner**: Orchestrates the entire benchmark lifecycle
- **Worker**: Executes operations concurrently
- **Scenario**: Defines the benchmark scenario (CRUD, Query, Realtime, etc.)
- **Operation**: Represents a single operation (create, read, update, delete, query)
- **Client**: Abstracts communication with Syntrix API
- **MetricsCollector**: Collects and aggregates benchmark metrics

## Interfaces

### Runner

The `Runner` interface orchestrates the benchmark execution:

```go
type Runner interface {
    Initialize(ctx context.Context, config *Config) error
    Run(ctx context.Context) (*Result, error)
    Cleanup(ctx context.Context) error
}
```

**Lifecycle**:
1. `Initialize`: Prepare environment, create workers, setup scenario
2. `Run`: Execute benchmark (warmup → rampup → run)
3. `Cleanup`: Clean up resources, close connections

### Worker

The `Worker` interface executes operations concurrently:

```go
type Worker interface {
    Start(ctx context.Context) error
    Execute(ctx context.Context, op Operation) (*OperationResult, error)
    Stop(ctx context.Context) error
}
```

**Responsibilities**:
- Execute operations from the scenario
- Report results to metrics collector
- Handle errors and retries
- Maintain target operation rate

### Scenario

The `Scenario` interface defines benchmark scenarios:

```go
type Scenario interface {
    Name() string
    Setup(ctx context.Context, env *TestEnv) error
    NextOperation() (Operation, error)
    Teardown(ctx context.Context, env *TestEnv) error
}
```

**Examples**:
- `CRUDScenario`: Mixed CRUD operations
- `QueryScenario`: Query-heavy workload
- `RealtimeScenario`: WebSocket subscriptions

### Operation

The `Operation` interface represents a single operation:

```go
type Operation interface {
    Type() string
    Execute(ctx context.Context, client Client) (*OperationResult, error)
}
```

**Operation Types**:
- `create`: Create document
- `read`: Read document
- `update`: Update document (PATCH)
- `delete`: Delete document
- `query`: Query documents

### Client

The `Client` interface abstracts Syntrix API communication:

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

    Close() error
}
```

### MetricsCollector

The `MetricsCollector` interface collects benchmark metrics:

```go
type MetricsCollector interface {
    RecordOperation(result *OperationResult)
    GetMetrics() *AggregatedMetrics
    GetSnapshot() *AggregatedMetrics
    Reset()
}
```

## Core Types

### Config

Complete benchmark configuration including:
- Target URL and duration
- Worker count and rampup settings
- Scenario definition
- Authentication settings
- Metrics and monitoring configuration

### Result

Complete benchmark results including:
- Summary metrics (throughput, latency, error rate)
- Per-operation metrics breakdown
- Error details
- Timeline data

### OperationResult

Result of a single operation execution:
- Operation type and duration
- Success/failure status
- Error details
- Network statistics (bytes sent/received)

### AggregatedMetrics

Statistical aggregation of operation results:
- Total operations and errors
- Success rate
- Throughput (ops/sec)
- Latency statistics (min, max, mean, percentiles)
- Error distribution

## Usage Example

```go
// Create configuration
config := &types.Config{
    Name:     "crud-benchmark",
    Target:   "http://localhost:8080",
    Duration: 5 * time.Minute,
    Workers:  100,
    Scenario: types.ScenarioConfig{
        Type: "crud",
        Operations: []types.OperationConfig{
            {Type: "create", Weight: 30, Rate: 300},
            {Type: "read", Weight: 50, Rate: 500},
            {Type: "update", Weight: 15, Rate: 150},
            {Type: "delete", Weight: 5, Rate: 50},
        },
    },
}

// Create and run benchmark
runner := runner.New(config)
defer runner.Cleanup(ctx)

if err := runner.Initialize(ctx, config); err != nil {
    log.Fatal(err)
}

result, err := runner.Run(ctx)
if err != nil {
    log.Fatal(err)
}

// Print results
fmt.Printf("Throughput: %.2f ops/sec\n", result.Summary.Throughput)
fmt.Printf("P99 Latency: %d ms\n", result.Summary.Latency.P99)
fmt.Printf("Success Rate: %.2f%%\n", result.Summary.SuccessRate)
```

## Design Principles

1. **Interface Segregation**: Small, focused interfaces
2. **Context Everywhere**: All long-running operations accept `context.Context`
3. **Error Handling**: Explicit error returns, no panics
4. **Concurrency Safe**: All shared state must be thread-safe
5. **Resource Cleanup**: Explicit cleanup methods, support `defer`

## Testing

All types include comprehensive unit tests. Run tests with:

From the repository root:

```bash
(cd packages/syntrix && go test ./pkg/benchmark/types/...)
```

## See Also

- [DESIGN.md](../../../../../docs/benchmark/DESIGN.md) - Architecture and design details
- [TASKS.md](../../../../../docs/benchmark/TASKS.md) - Implementation tasks
- Runner implementation: `packages/syntrix/pkg/benchmark/runner/`
- Scenario implementations: `packages/syntrix/pkg/benchmark/scenario/`
- Client implementation: `packages/syntrix/pkg/benchmark/client/`
