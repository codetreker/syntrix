# Agent Note: Isolate Puller Subscription Membership

Status: implemented

## Problem

The subscription registry used the diagnostic consumer ID as its key. Registering
a second connection with the same label replaced the first connection's entry,
stopping its broadcasts and excluding it from bulk cleanup. When the first
connection later exited, removal by label closed and deleted the second.

## Decision

The [subscription contract](../../../../docs/design/server/puller/01.architecture.md#25-subscriber)
keeps `consumer_id` as a logging label. Each actual subscription is registered
by its existing in-process object, including subscriptions with equal or empty
labels. Local and gRPC cleanup both remove their own object.

| Operation | Membership behavior |
|---|---|
| Add | Register the object; repeated Add of the same object is idempotent |
| Remove | Close and delete only that registered object; absent/repeated removal is harmless |
| Broadcast | Visit every registered object using the existing queue and overflow policy |
| Count / All | Report actual registrations, independently of labels |
| CloseAll | Close all currently registered objects and clear the registry |

The existing manager write lock serializes registration and cleanup, including
subscriber closure. Broadcast holds its existing read lock while enqueueing.
The unused unique-label lookup is removed because labels no longer identify a
single registration. No process-local object identity enters persisted state or
the wire protocol.

## Alternatives

**Only guard removal against replacing connections.** This prevents old cleanup
from closing a newer connection, but the overwritten subscription still loses
broadcasts and bulk cleanup membership.

**Reject duplicate labels or close the old connection.** Both change delivery
based on a label that the protocol explicitly reserves for diagnostics.

**Generate registration IDs.** Extra generated state is unnecessary when the
existing subscription object already distinguishes each live registration.

## Consequences

Overlapping same-label subscriptions retain independent queues, progress, and
cleanup. Counts include both registrations. Local and gRPC regression tests
wait for the canceled subscription's cleanup before checking survivor delivery;
manager tests cover duplicate/empty labels and concurrent broadcast/removal.

Queued or in-flight events can still complete after cancellation. CloseAll
retains its existing behavior and does not seal the manager against later Add.
The identity change preserves subscription loops, checkpoint and event formats,
and replay. [gRPC admission](2026-09-07-puller-grpc-admission.md) uses registration
counts and seals admission during Server shutdown. Local quota integration,
lag thresholds, and coalescing policy remain in the
[consumer settings proposal](../../proposed/bug-fix/2026-09-07-puller-admission-and-catch-up-settings.md).
