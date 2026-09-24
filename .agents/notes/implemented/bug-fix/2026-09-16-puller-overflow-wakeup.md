# Agent Note: Wake Puller Subscribers After Queue Overflow

Status: implemented

## Problem

A Puller subscriber could stop delivering retained events after its live queue
overflowed while the connection remained active. The subscriber manager rejected
the event, logged the overflow, and only then set a boolean flag. Both the local
and gRPC live loops observed that flag only while receiving another queued event.
If the consumer drained the queue before the flag was set, no event remained to
wake retained replay.

A later live event did not make this ordering safe. The consumer read the flag,
sent that newer event, advanced subscription delivery progress, and entered
replay from the newer position. An earlier rejected event from the same backend
could then be excluded permanently.

## Decision

Each `Subscriber` owns a serialized live-admission and recovery handoff. Live
admission and the recovery latch share one mutex:

- A successful admission places the event on the bounded live queue.
- The first rejected admission latches recovery and publishes a non-blocking,
  capacity-one notification.
- While recovery is latched, later events are rejected from live admission and
  remain available through retained history. They cannot advance delivery
  progress past the first missing event.

The local and gRPC live loops select the recovery notification alongside their
event, cancellation, and lifecycle channels. A dequeued prefix event may finish
delivery, after which the loop enters catch-up. Each replay attempt clears the
consumed recovery latch, and the recovery transition discards stale live entries
before returning to live delivery. Replay starts from the last successfully
delivered position. For empty-start subscriptions with no delivered position,
the shared runner filters retained history against post-registration broadcast
groups before coalescing, as defined by the
[subscription state machine](../architecture/2026-09-07-puller-subscription-state-machine.md).
Overflow during replay latches another notification and repeats catch-up. An
overflow immediately after the final check remains pending and wakes the live
loop.

The notification channel is never closed. Subscriber shutdown continues to use
the existing `done` channel, so non-blocking recovery publication cannot send to
a closed channel or delay cancellation.

## Alternatives

**Poll the boolean flag from heartbeat and health tickers.** Polling bounds the
idle delay but does not prevent a newer live event from advancing progress before
the next poll. It also ties correctness to configurable timer intervals.

**Add a wake channel without fencing live admission.** This wakes an idle
consumer, but a concurrent producer can enqueue a later event before recovery is
observed. Sending that event can still move progress beyond rejected history.

**Disconnect slow consumers.** Reconnection would reuse retained replay, but it
changes the adaptive-consumption contract and reintroduces reconnect flapping for
temporary backpressure.

## Consequences

- Queue overflow wakes idle local and gRPC subscribers without another source
  event, including when overflow logging or the broadcasting goroutine remains
  blocked.
- Recovery notifications coalesce while pending. Repeated overflow cannot erase
  required recovery, and later live events cannot cross the recovery fence.
- Delivery progress still advances only after successful transport delivery.
  Consumer-owned processed checkpoints remain outside Puller.
- Iterator, retention, transport, and boundary-validation failures continue to
  propagate through their existing paths. This decision does not extend retained
  history.
- The shared `Subscriber` handoff owns the admission fence, and the
  [Puller subscription state machine](../architecture/2026-09-07-puller-subscription-state-machine.md)
  now owns the common recovery transition used by local and gRPC delivery.
- Deterministic regressions block overflow logging after queue rejection and prove
  that retained replay completes before the broadcast returns. Race tests cover
  repeated recovery epochs, pending-recovery cancellation and closure, local
  delivery, and gRPC delivery.

The [adaptive consumption design](../../../../docs/design/server/puller/02.adaptive-consumption.md)
defines the runtime state machine. [Consumer catch-up settings](../../proposed/bug-fix/2026-09-07-puller-admission-and-catch-up-settings.md)
owns deferred lag and coalescing policy.
