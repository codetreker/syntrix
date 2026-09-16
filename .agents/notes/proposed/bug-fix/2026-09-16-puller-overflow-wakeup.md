# Agent Note: Wake Puller Subscribers After Queue Overflow

Status: proposed

## Problem

A Puller subscriber can stop delivering retained events after its live queue
overflows, even while the connection remains active. Recovery currently depends
on observing an overflow flag while handling another queued event. If that flag
is published after the consumer drains the queue, no subsequent event may arrive
to trigger replay.

The [subscriber manager](../../../../internal/puller/core/subscriber.go) selects
the full-queue branch, logs the overflow, and then sets the flag. The
[gRPC live loop](../../../../internal/puller/grpc/server.go) checks and clears the
flag when receiving an event, before sending it. Its heartbeat validates the
source boundary but does not check overflow. The
[local live loop](../../../../internal/puller/core/puller.go) also checks overflow
on event receipt, with no separate overflow wakeup.

| Step | Producer | Consumer |
|---|---|---|
| 1 | Event 2 fills the queue | Sending event 1 is blocked |
| 2 | Event 3 selects the full-queue branch; flag publication has not completed | Event 1 send resumes |
| 3 | Has not set the flag yet | Drains event 2, reads overflow as false, sends event 2 |
| 4 | Sets overflow to true; event 3 was not queued | Waits with an empty event queue |
| 5 | No more events arrive | Replay never starts despite retained event 3 |

A full-suite run of
[`TestServer_StateMachine_LiveDowngrade`](../../../../internal/puller/grpc/server_state_machine_test.go)
failed to observe the expected replay. Source inspection establishes the feasible
interleaving above; a deterministic regression controlling the late flag
publication remains required. The event-source handler enqueues asynchronous
broadcast work, so returning from `EmitEvent` does not prove overflow publication
has completed.

## Proposal

Make an overflow observable to an idle subscriber without requiring another live
event. Define the notification/observation handoff so a consumer cannot consume
the last queued event, miss a concurrent overflow, and remain in live mode.
The notification mechanism is not selected by this proposal.

- Trigger the existing replay path from the last successfully delivered position.
- Preserve queued-event handling, progress/checkpoint semantics, replay ordering,
  and the existing local/gRPC architecture.
- Cover overflow while sending, at the empty-queue transition, and while replay
  completes. Repeated overflow signals must not hide pending recovery.
- Coordinate notification with cancellation and subscriber closure; do not send
  to a closed channel or leave a blocked producer/consumer.

## Alternatives

**Notification mechanism remains open.** Choose the handoff implementation after
a deterministic test demonstrates that late overflow cannot leave a subscriber
idle. Retain the existing replay and checkpoint contract; a broader subscription
architecture change is not a prerequisite for this bugfix.

## Acceptance Criteria

- Deterministically pause overflow publication after the producer rejects an
  event, let the consumer drain the last queued event and observe no overflow,
  then publish overflow. With no further input, the subscriber enters replay and
  delivers the retained event within a bounded test deadline.
- The same notification guarantee holds for local and gRPC consumers, including
  repeated overflow and an overflow concurrent with return to live delivery.
- Already completed deliveries retain their progress; recovery never advances a
  checkpoint for an undelivered event.
- Cancellation and closure during the handoff terminate cleanly under race tests.
- The controlled late-publication regression fails against the original behavior;
  an ordinary overflow-before-send-resumes test is insufficient evidence.

## Risks

Notification and flag reset can themselves race; a one-time check before blocking
is insufficient unless the handoff excludes lost wakeups. Shared Subscriber
changes affect local and remote consumers. This fix does not extend retention or
restore unavailable history; existing replay failures must continue to propagate.

## Related Decisions

[Consumer catch-up settings](2026-09-07-puller-admission-and-catch-up-settings.md)
owns deferred lag and coalescing policy. This notification fix concerns the
existing overflow trigger and does not require adopting those policies.
[Local subscription replay](../architecture/2026-09-07-local-puller-subscription-replay.md)
records broader delivery integration work; this proposal does not select that
architecture.
