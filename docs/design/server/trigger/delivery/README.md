# Delivery Service Design

## Overview

The Delivery Service consumes tasks from the NATS queue and executes HTTP webhook deliveries.

## Responsibility

- Consume DeliveryTask messages from NATS JetStream
- Partition tasks by document key for ordering
- Execute HTTP webhook requests
- Handle retries with exponential backoff
- Manage authentication (secrets, system tokens)

## Data Flow

```
┌──────────────────────────────────────────────────────────────────────┐
│                      Delivery Service                                 │
│                                                                       │
│  ┌──────────────┐    ┌─────────────┐    ┌──────────────────────┐    │
│  │   Consumer   │ -> │ Worker Pool │ -> │   HTTP Delivery      │    │
│  │  (NATS JS)   │    │ (partition) │    │ (auth+sign+request)  │    │
│  └──────────────┘    └─────────────┘    └──────────────────────┘    │
│         │                   │                      │                 │
│         v                   v                      v                 │
│  ┌─────────────────────────────────────────────────────────────┐    │
│  │                   Retry / Backoff                            │    │
│  └─────────────────────────────────────────────────────────────┘    │
└──────────────────────────────────────────────────────────────────────┘
```

## Service Interface

```go
// Service consumes delivery tasks and executes HTTP webhooks.
type Service interface {
    // Start begins consuming and processing delivery tasks.
    // Blocks until context is cancelled.
    Start(ctx context.Context) error
}

// ServiceOptions configures the delivery service.
type ServiceOptions struct {
    StreamName      string        // NATS stream name (default: "TRIGGERS")
    NumWorkers      int           // Number of worker goroutines
    ChannelBufSize  int           // Per-worker channel buffer size
    DrainTimeout    time.Duration // Drain timeout for graceful shutdown
    ShutdownTimeout time.Duration // Overall shutdown timeout
}

// Dependencies contains external dependencies for the delivery service.
type Dependencies struct {
    Nats    *nats.Conn
    Auth    identity.AuthN        // For system token generation
    Secrets types.SecretProvider  // For resolving SecretsRef
    Metrics types.Metrics
}
```

## Components

### 1. TaskConsumer

Consumes messages from NATS JetStream with partitioning.

**Key behaviors:**
- Subscribes to subject pattern: `<stream_name>.>`
- Partitions messages by docKey for ordering
- Manages acknowledgement and NAK

See: [01.consumer.md](01.consumer.md)

### 2. Worker Pool

Processes tasks with configurable concurrency.

**Key behaviors:**
- Each worker has its own channel
- Partitioning ensures same-document ordering
- Workers process until channel closed

### 3. HTTP Delivery Worker

Executes webhook HTTP requests.

**Key behaviors:**
- Builds HTTP request from DeliveryTask
- Adds authentication headers
- Signs requests with HMAC
- Handles response codes and retries

See: [02.worker.md](02.worker.md)

## Configuration

```go
type TriggerConfig struct {
    // Delivery-specific
    NumWorkers      int           // Worker pool size
    ChannelBufSize  int           // Per-worker buffer
    DrainTimeout    time.Duration // Graceful shutdown drain
    ShutdownTimeout time.Duration // Overall shutdown timeout

    // Shared
    StreamName      string        // NATS stream name
}
```

## Graceful Shutdown

The delivery service implements graceful shutdown in phases:

1. **Stop accepting new messages** - Call consumer Stop()
2. **Drain period** - Wait for in-flight dispatch() calls
3. **Close worker channels** - Workers drain remaining messages
4. **Wait for workers** - Wait with timeout for completion

See: [03.graceful_shutdown.md](03.graceful_shutdown.md)

## Implementation

See: `packages/syntrix/internal/trigger/delivery/`
- `service.go` - Service interface and implementation
- `factory.go` - Factory function

See: `packages/syntrix/internal/trigger/worker/`
- `worker.go` - HTTP delivery worker
- `interfaces.go` - Worker interfaces
