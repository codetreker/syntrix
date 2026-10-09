# Agent Note: Bound Puller Pending Writes

Status: implemented

## Problem

The write queue accepted events beyond its configured capacity while disk
commits were stalled. The batch size only triggered a flush; each flush took
the entire pending queue. Neither setting bounded the work its name described.

## Decision

The existing [buffer writer](../../../../packages/syntrix/internal/puller/buffer/writer.go)
enforces both limits while retaining its pending/flushing queues and batcher.

| Condition | Behavior |
|---|---|
| Admission | Check pending plus flushing count against `queue_size` before encoding, under the existing mutex |
| Queue full | Release the mutex and wait for capacity, cancellation, or closure/failure |
| Batch ready | Commit at most the oldest `batch_size` events with their final native token atomically |
| Queue smaller than batch size | Filling the queue also triggers a flush |
| Successful completion | Release committed request references and wake capacity waiters |
| Normal close | Reject waiting/new admission and drain all previously admitted events in capped batches |
| Batch failure | Preserve the original error, wake waiters, and stop before any later batch commits |

`Write` receives the existing capture context. Cancellation prevents a waiting
event from being admitted; it does not cancel a Pebble commit already in flight.
Successful `Write` still means admission, so live publication can precede commit.
Queued full batches continue without requiring another producer notification.

## Alternatives

**Return overload immediately.** This bounds memory but terminates capture under
the implemented [admission-failure handling](2026-09-08-puller-capture-failures.md).
Blocking admission keeps temporary capacity exhaustion within normal backpressure.

**Allocate a buffered channel of queue_size.** A channel simplifies waiting, but
its capacity alone excludes the in-flight batch and does not bound admission
before payload allocation. It is suitable only with matching capacity accounting.

## Consequences

Deterministic stalled-commit tests cover concurrent admission, queue and batch
limits, encoding only after capacity is available, cancellation, success/failure
wakeups, normal close draining, independent buffers, and FIFO native-token
commits verified after reopen. Capture tests verify context propagation while
retaining asynchronous publication.

Limits apply per backend to event counts. Payload sizes, caller-owned events,
upstream driver buffers, and subscriber queues still affect aggregate memory;
this is not a byte budget. Full capacity backpressures capture until writes
complete, so prolonged disk stalls can delay live delivery and exhaust retained
source history. This change adds no history-recovery mechanism.

Checkpoint bytes, cached event values, queue structure, consumer progress, and
batching defaults remain unchanged. No migration is needed.

## Dependencies

[Capture failure handling](2026-09-08-puller-capture-failures.md)
preserves existing atomic event/token commits and terminal write failures;
[history-gap recovery](../../proposed/architecture/2026-09-07-puller-history-gap-recovery.md)
still tracks consumer recovery after upstream history expiry.
