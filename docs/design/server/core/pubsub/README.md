# Core PubSub Layer

## Overview

The pubsub layer provides a transport-agnostic abstraction for message-based communication between services.

## Why

The trigger system originally had NATS JetStream publisher/consumer implementations tightly coupled to trigger-specific types (`DeliveryTask`, `Metrics`). This coupling:

- Prevents reuse by other services
- Makes it harder to swap transport layers
- Complicates testing in isolation

## How

Extract a generic pubsub abstraction into `packages/syntrix/internal/core/pubsub/` that provides transport-agnostic interfaces. Services use this abstraction, with pluggable implementations:

- **NATS JetStream** - For distributed mode (production)
- **In-Memory** - For standalone mode (dev/test/edge)

## Goals

1. **Reusability** - Other services can use the same pubsub infrastructure
2. **Testability** - Interfaces can be easily mocked without external dependencies
3. **Decoupling** - Business logic doesn't depend on transport implementation
4. **Flexibility** - Can swap implementations based on deployment mode

## Non-Goals

1. Complex routing patterns (topic exchanges, etc.)
2. Exactly-once semantics (at-least-once is sufficient)

## Package Structure

```
packages/syntrix/internal/core/pubsub/
├── interfaces.go     # Core interfaces (Publisher, Consumer, Message)
├── options.go        # Configuration options
├── nats/             # NATS JetStream implementation (distributed mode)
│   ├── publisher.go
│   ├── consumer.go
│   ├── message.go
│   └── connection.go
├── memory/           # In-memory implementation (standalone mode)
│   ├── broker.go
│   ├── publisher.go
│   ├── consumer.go
│   └── message.go
└── testing/          # Test utilities and mocks
    └── mock.go
```

## Core Interfaces

### Message

```go
type Message interface {
    Data() []byte
    Subject() string
    Ack() error
    Nak() error
    NakWithDelay(delay time.Duration) error
    Term() error
    Metadata() (MessageMetadata, error)
}

type MessageMetadata struct {
    NumDelivered uint64
    Timestamp    time.Time
    Subject      string
    Stream       string
    Consumer     string
}
```

### Publisher

```go
type Publisher interface {
    Publish(ctx context.Context, subject string, data []byte) error
    Close() error
}
```

### Consumer

```go
type Consumer interface {
    Subscribe(ctx context.Context) (<-chan Message, error)
}
```

## Implementations

| Implementation | Mode | Use Case | Docs |
|----------------|------|----------|------|
| NATS JetStream | Distributed | Production at scale | [01.nats.md](01.nats.md) |
| In-Memory | Standalone | Dev/Test/Edge | [02.memory.md](02.memory.md) |

## Design Decisions

### 1. Raw Bytes vs Typed Messages

**Decision**: Use `[]byte` for message data, no serialization in core.

**Rationale**: Different consumers may use different serialization formats (JSON, protobuf, etc.). Core pubsub should be format-agnostic.

### 2. Connection Management

**Decision**: Accept connection/broker from caller, don't manage lifecycle.

**Rationale**: Connections may be shared across multiple components. Caller (ServiceManager) owns the lifecycle.

### 3. Stream Creation

**Decision**: Auto-create streams with sensible defaults, allow override via options.

**Rationale**: Simplifies usage for common cases. Advanced users can pre-create streams with custom settings.

### 4. Metrics Hooks

**Decision**: Use callback hook (`OnPublish`) for publisher metrics. Consumer metrics are handled by caller.

**Rationale**: More flexible - callers can map to their own metrics systems.

### 5. Subscribe Pattern vs Handler Pattern

**Decision**: Use Subscribe pattern (return channel) instead of Handler pattern (callback).

**Rationale**:
- Simpler core implementation - no internal worker pool management
- More flexible for callers - they control concurrency and worker pools
- Easier to test - just read from channel
- Caller can implement custom partitioning/ordering as needed

## Usage Pattern

```go
// Standalone mode
broker := memory.NewBroker()
pub, _ := memory.NewPublisher(broker, opts)
consumer, _ := memory.NewConsumer(broker, opts)

// Distributed mode
js, _ := natspubsub.NewJetStream(conn)
pub, _ := natspubsub.NewPublisher(js, opts)
consumer, _ := natspubsub.NewConsumer(js, opts)

// Both use the same interfaces
pub.Publish(ctx, "db1.users.doc123", jsonData)

msgCh, _ := consumer.Subscribe(ctx)
for msg := range msgCh {
    if err := processMessage(msg); err != nil {
        msg.Nak()
    } else {
        msg.Ack()
    }
}
```

## Subject Naming Convention

```
<stream_name>.<database>.<collection>.<docKey>
```

Example: `TRIGGERS.mydb.users.user123`

Wildcards (NATS-style):
- `*` matches single token
- `>` matches one or more tokens (must be last)
