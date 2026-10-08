# Trigger Evaluator

The evaluator turns Puller document changes into delivery tasks. Its watcher
subscribes across all logical databases and excludes its own checkpoint
document; each configured rule filters the database, event type, and collection
and may evaluate a CEL condition.
Matching rules produce tasks through the configured pubsub publisher.

The service processes one source event at a time. It advances checkpoint
progress only after all matching tasks have been published. A reported
evaluation or publication failure retries the same rule with cancellable
backoff and blocks later events. The rule's effective task timeout is captured
when the task is built.

| Document | Contract |
|----------|----------|
| [Checkpoint](01.checkpoint.md) | Source position, failure gate, and restart behavior |
| [CEL evaluator](02.cel_evaluator.md) | Rule matching, condition input, caching, and evaluation errors |
| [Task publisher](03.publisher.md) | Delivery task serialization and subject routing |

The [trigger architecture](../01.architecture.md) describes the delivery
service and the limits of publisher acceptance.
