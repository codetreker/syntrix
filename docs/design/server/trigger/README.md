# Trigger Design Documentation

## Document Index

| Document | Description |
|----------|-------------|
| [00.requirements.md](00.requirements.md) | Requirements and goals |
| [01.architecture.md](01.architecture.md) | High-level architecture |
| [02.interfaces.md](02.interfaces.md) | Contracts and safety constraints |
| [03.service_separation.md](03.service_separation.md) | Service separation summary |

## Service Documentation

### Evaluator Service

Watches document changes, evaluates trigger conditions, publishes tasks.

| Document | Description |
|----------|-------------|
| [evaluator/README.md](evaluator/README.md) | Service overview |
| [evaluator/01.checkpoint.md](evaluator/01.checkpoint.md) | Checkpoint design (global) |
| [evaluator/02.cel_evaluator.md](evaluator/02.cel_evaluator.md) | CEL condition evaluation |
| [evaluator/03.publisher.md](evaluator/03.publisher.md) | Task publishing |

### Delivery Service

Consumes tasks and executes HTTP webhook deliveries.

| Document | Description |
|----------|-------------|
| [delivery/README.md](delivery/README.md) | Service overview |
| [delivery/01.consumer.md](delivery/01.consumer.md) | NATS consumer |
| [delivery/02.worker.md](delivery/02.worker.md) | HTTP worker |
| [delivery/03.graceful_shutdown.md](delivery/03.graceful_shutdown.md) | Graceful shutdown |

## Key Design Decisions

1. **Service Separation**: Evaluator and Delivery are independent services
2. **Global Checkpoint**: Uses `sys/checkpoints/trigger_evaluator` (not per-database)
3. **NATS JetStream**: Tasks flow via `<stream>.<database>.<collection>.<docKey>`

## Implementation

- Evaluator: `packages/syntrix/internal/trigger/evaluator/`
- Delivery: `packages/syntrix/internal/trigger/delivery/`
- Watcher: `packages/syntrix/internal/trigger/watcher/`
- Worker: `packages/syntrix/internal/trigger/worker/`
